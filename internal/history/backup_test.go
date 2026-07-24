package history

import (
	"testing"
	"time"
)

// A snapshot taken from a populated store and restored into an empty one must
// reproduce the data exactly — this is what a backup relies on.
func TestSnapshotRestoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AppData", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)

	src, err := NewStore(500, 0)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	now := time.Now()
	if err := src.Append(Entry{Timestamp: now.Add(-time.Hour), Raw: "Hallo wereld.", Cleaned: "Hallo wereld.", CleanupUsed: true, DurationMS: 3000, Language: "nl", CleanupInTokens: 12, CleanupOutTokens: 8}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := src.AppendUpload(Entry{Timestamp: now.Add(-2 * time.Hour), Raw: "Upload text.", DurationMS: 5000, Language: "en"}); err != nil {
		t.Fatalf("AppendUpload: %v", err)
	}
	if _, err := src.RecordAchievements([]string{"first", "words-100"}); err != nil {
		t.Fatalf("RecordAchievements: %v", err)
	}

	snap, err := src.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	src.Close()
	if len(snap.History) != 2 || len(snap.Achievements) != 2 || len(snap.DayStats) == 0 {
		t.Fatalf("snapshot sizes: history=%d ach=%d days=%d", len(snap.History), len(snap.Achievements), len(snap.DayStats))
	}

	// A fresh store in a different dir, restored from the snapshot.
	dir2 := t.TempDir()
	t.Setenv("AppData", dir2)
	t.Setenv("XDG_CONFIG_HOME", dir2)
	t.Setenv("HOME", dir2)
	dst, err := NewStore(500, 0)
	if err != nil {
		t.Fatalf("NewStore dst: %v", err)
	}
	defer dst.Close()
	if err := dst.Restore(snap); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	got, err := dst.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot dst: %v", err)
	}
	if len(got.History) != 2 {
		t.Fatalf("restored history = %d, want 2", len(got.History))
	}
	if len(got.Achievements) != 2 {
		t.Fatalf("restored achievements = %d, want 2", len(got.Achievements))
	}
	// The first history entry should carry its text and token counts through.
	var found bool
	for _, e := range got.History {
		if e.Raw == "Hallo wereld." {
			found = true
			if e.CleanupInTokens != 12 || e.CleanupOutTokens != 8 {
				t.Fatalf("tokens not preserved: in=%d out=%d", e.CleanupInTokens, e.CleanupOutTokens)
			}
			if !e.CleanupUsed {
				t.Fatal("cleanup_used flag not preserved")
			}
		}
	}
	if !found {
		t.Fatal("restored history missing the dictation entry")
	}

	// Unlocked achievements come back with their ids.
	un, err := dst.UnlockedAchievements()
	if err != nil {
		t.Fatalf("UnlockedAchievements: %v", err)
	}
	if _, ok := un["first"]; !ok {
		t.Fatal("achievement 'first' not restored")
	}
}

// Restoring must replace, not merge: data already in the store is gone
// afterwards if it isn't in the backup.
func TestRestoreReplacesExisting(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AppData", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)

	s, err := NewStore(500, 0)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer s.Close()
	if err := s.Append(Entry{Timestamp: time.Now(), Raw: "will be gone", Cleaned: "will be gone", DurationMS: 1000, Language: "nl"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := s.Restore(BackupData{}); err != nil {
		t.Fatalf("Restore empty: %v", err)
	}
	got, err := s.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(got.History) != 0 || len(got.DayStats) != 0 {
		t.Fatalf("expected empty after restoring empty backup, got history=%d days=%d", len(got.History), len(got.DayStats))
	}
}
