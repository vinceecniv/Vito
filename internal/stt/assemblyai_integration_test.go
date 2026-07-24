package stt

// Thin integration tests, guarded by VITO_TEST_AAI_KEY:
//
//	VITO_TEST_AAI_KEY=<key> go test ./internal/stt/ -run Integration -v

import (
	"context"
	"encoding/binary"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"vito/internal/config"
)

func integrationCfg(t *testing.T) config.STT {
	t.Helper()
	key := os.Getenv("VITO_TEST_AAI_KEY")
	if key == "" {
		t.Skip("VITO_TEST_AAI_KEY not set")
	}
	return config.STT{Provider: "assemblyai", APIKey: key, Language: "nl", EUEndpoint: true}
}

// tonePCM generates n seconds of a quiet tone: enough to exercise the
// session without expecting any transcript.
func tonePCM(seconds float64) []byte {
	n := int(16000 * seconds)
	out := make([]byte, n*2)
	for i := 0; i < n; i++ {
		v := int16(3000 * math.Sin(2*math.Pi*440*float64(i)/16000))
		binary.LittleEndian.PutUint16(out[i*2:], uint16(v))
	}
	return out
}

func TestIntegrationStream(t *testing.T) {
	cfg := integrationCfg(t)
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	s := NewStream(cfg, nil, log, nil)
	pcm := tonePCM(1.5)
	for off := 0; off < len(pcm); off += 1600 {
		end := min(off+1600, len(pcm))
		s.Send(pcm[off:end])
		time.Sleep(50 * time.Millisecond) // pace like real capture
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	text, err := s.Finish(ctx)
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	t.Logf("transcript: %q", text) // a tone should yield ~empty text; no error is the assertion
}

func TestIntegrationAsync(t *testing.T) {
	cfg := integrationCfg(t)

	// Minimal WAV around the same tone.
	pcm := tonePCM(1.5)
	h := make([]byte, 44)
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(36+len(pcm)))
	copy(h[8:], "WAVE")
	copy(h[12:], "fmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1)
	binary.LittleEndian.PutUint16(h[22:], 1)
	binary.LittleEndian.PutUint32(h[24:], 16000)
	binary.LittleEndian.PutUint32(h[28:], 32000)
	binary.LittleEndian.PutUint16(h[32:], 2)
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(len(pcm)))
	path := filepath.Join(t.TempDir(), "tone.wav")
	if err := os.WriteFile(path, append(h, pcm...), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	text, err := NewAsyncClient(cfg, nil).TranscribeFile(ctx, path)
	if err != nil {
		t.Fatalf("TranscribeFile: %v", err)
	}
	t.Logf("transcript: %q", text)
}
