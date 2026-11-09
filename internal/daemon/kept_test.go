package daemon

import (
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"vito/internal/audio"
)

func keptSession(t *testing.T, d time.Duration) *session {
	t.Helper()
	time.Sleep(2 * time.Millisecond) // spool names carry the millisecond
	sp, err := audio.NewSpool()
	if err != nil {
		t.Fatal(err)
	}
	n := int(d.Seconds() * audio.CaptureSampleRate * audio.BytesPerSample * audio.CaptureChannels)
	if err := sp.Write(make([]byte, n)); err != nil {
		t.Fatal(err)
	}
	if err := sp.Close(); err != nil {
		t.Fatal(err)
	}
	return &session{spool: sp}
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

func TestKeptCancelled(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	d := &Daemon{log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	short := keptSession(t, 3*time.Second)
	if d.keep(short) {
		t.Fatal("a recording shorter than minKept was kept")
	}

	a := keptSession(t, 5*time.Second)
	a.heard.Store("hallo daar")
	if !d.keep(a) {
		t.Fatal("not kept")
	}
	if heard, ok := d.Cancelled(); !ok || heard != "hallo daar" {
		t.Fatalf("Cancelled() = %q, %v", heard, ok)
	}

	b := keptSession(t, 5*time.Second)
	d.keep(b)
	if exists(a.spool.Path()) {
		t.Error("the earlier kept recording was not deleted")
	}
	if heard, ok := d.Cancelled(); !ok || heard != "" {
		t.Fatalf("Cancelled() = %q, %v after a second keep", heard, ok)
	}

	d.dropKept()
	if exists(b.spool.Path()) {
		t.Error("dropKept left the recording")
	}
	if _, ok := d.Cancelled(); ok {
		t.Error("still something to restore after dropKept")
	}
	if err := d.RestoreCancelled(); err == nil {
		t.Error("restoring nothing did not fail")
	}
}
