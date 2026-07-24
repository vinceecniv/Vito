package audio

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The recordings folder hangs off os.UserCacheDir, so point that at a temp dir.
func tempCacheDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// UserCacheDir reads LOCALAPPDATA on Windows, XDG_CACHE_HOME elsewhere.
	t.Setenv("LOCALAPPDATA", dir)
	t.Setenv("XDG_CACHE_HOME", dir)
	t.Setenv("HOME", dir)
	return dir
}

func TestValidRecordingID(t *testing.T) {
	for _, id := range []string{"deadbeef", "0123456789abcdef"} {
		if !ValidRecordingID(id) {
			t.Errorf("%q should be valid", id)
		}
	}
	// Anything that could escape the folder or name a different file must not be.
	for _, id := range []string{"", "../etc/passwd", "a/b", "zz", "dead beef", "dead.wav"} {
		if ValidRecordingID(id) {
			t.Errorf("%q should be rejected", id)
		}
	}
}

func TestSaveRecordingMovesTheSpoolFile(t *testing.T) {
	tempCacheDir(t)
	src := filepath.Join(t.TempDir(), "vito-spool.wav")
	if err := os.WriteFile(src, []byte("RIFF"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst, err := SaveRecording(src, "abc123", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("the spool file should have been moved away, not copied")
	}
	p, ok := RecordingPath("abc123")
	if !ok || p != dst {
		t.Errorf("RecordingPath = %q,%v, want %q,true", p, ok, dst)
	}
	if _, err := SaveRecording(src, "../escape", nil); err == nil {
		t.Error("an invalid id must be refused")
	}
}

func TestPruneKeepsTheNewest(t *testing.T) {
	tempCacheDir(t)
	dir, err := RecordingsDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Oldest first, one minute apart, so "newest" is unambiguous.
	base := time.Now().Add(-time.Hour)
	ids := make([]string, 0, MaxRecordings+3)
	for i := 0; i < MaxRecordings+3; i++ {
		id := fmt.Sprintf("aabbccdd%08x", i)
		p := filepath.Join(dir, id+".wav")
		if err := os.WriteFile(p, []byte("RIFF"), 0o600); err != nil {
			t.Fatal(err)
		}
		ts := base.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(p, ts, ts); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	// Star the oldest recording: it must survive the cap even though it would
	// otherwise be pruned, and it doesn't count towards MaxRecordings.
	pruneRecordings(MaxRecordings, map[string]bool{ids[0]: true})

	if _, ok := RecordingPath(ids[0]); !ok {
		t.Errorf("%s is a favorite and should have been kept", ids[0])
	}
	for _, id := range ids[1:3] {
		if _, ok := RecordingPath(id); ok {
			t.Errorf("%s should have been pruned", id)
		}
	}
	for _, id := range ids[3:] {
		if _, ok := RecordingPath(id); !ok {
			t.Errorf("%s should still be there", id)
		}
	}

	have := HaveRecordings(ids)
	if len(have) != MaxRecordings+1 { // the newest 10 plus the starred favorite
		t.Errorf("HaveRecordings reported %d kept, want %d", len(have), MaxRecordings+1)
	}

	RemoveRecording(ids[len(ids)-1])
	if _, ok := RecordingPath(ids[len(ids)-1]); ok {
		t.Error("RemoveRecording left the file behind")
	}
	if err := RemoveAllRecordings(); err != nil {
		t.Fatal(err)
	}
	if len(HaveRecordings(ids)) != 0 {
		t.Error("RemoveAllRecordings left files behind")
	}
}
