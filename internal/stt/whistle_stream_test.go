package stt

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"vito/internal/config"
	"vito/internal/whistle"
)

func TestWhistleLang(t *testing.T) {
	for in, want := range map[string]string{"nl": "nl", "en": "en", "auto": "", "": "", "ja": ""} {
		if got := whistleLang(config.STT{Language: in}); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

func TestPCMFromWAV(t *testing.T) {
	pcm := make([]byte, 3200)
	got, err := pcmFromWAV(whistle.WAV(pcm))
	if err != nil || len(got) != len(pcm) {
		t.Fatalf("got %d bytes, %v", len(got), err)
	}
	if _, err := pcmFromWAV([]byte("not a wav file")); err == nil {
		t.Fatal("garbage accepted")
	}
}

// TestWhistleStreamLive plays a recording into a session in real time and logs
// when the live text arrives and how long the final text takes after the stop.
// Manual: needs Whistle downloaded (Settings → Speech recognition).
//
//	VITO_WHISTLE_STREAM=path/to/clip.wav go test ./internal/stt -run TestWhistleStreamLive -v
func TestWhistleStreamLive(t *testing.T) {
	path := os.Getenv("VITO_WHISTLE_STREAM")
	if path == "" {
		t.Skip("set VITO_WHISTLE_STREAM to a 16 kHz mono WAV")
	}
	base, _ := os.UserCacheDir()
	dir := filepath.Join(base, "vito", "whistle")
	bins, _ := filepath.Glob(filepath.Join(dir, "*", "needle*"))
	models, _ := filepath.Glob(filepath.Join(dir, "whistle-*.cact"))
	var bin string
	for _, b := range bins {
		if filepath.Ext(b) != ".ok" {
			bin = b
		}
	}
	if bin == "" || len(models) == 0 {
		t.Skip("Whistle not downloaded")
	}
	WhistleEngine = func() (whistle.Engine, bool) { return whistle.Engine{Bin: bin, Model: models[0]}, true }
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pcm, err := pcmFromWAV(b)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	s := newWhistleStream(config.STT{Provider: "whistle", Language: "nl"}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), func(p string) {
		t.Logf("%5.1fs  %s", time.Since(start).Seconds(), p)
	})
	for off := 0; off < len(pcm); off += 3200 { // 100 ms at a time, in real time
		s.Send(pcm[off:min(off+3200, len(pcm))])
		time.Sleep(100 * time.Millisecond)
	}
	stop := time.Now()
	text, err := s.Finish(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("audio %.1fs; final text %.2fs after the stop:\n%s", float64(len(pcm))/32000, time.Since(stop).Seconds(), text)
}
