package stt

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"vito/internal/config"
)

func TestLocalSegmented(t *testing.T) {
	for base, want := range map[string]bool{
		"http://127.0.0.1:34595/v1":      true,
		"http://localhost:8080/v1":       true,
		"http://[::1]:8080/v1":           true,
		"https://api.groq.com/openai/v1": false,
		"https://api.openai.com/v1":      false,
		"http://192.168.1.20:8080/v1":    false,
		"":                               false,
	} {
		if got := localSegmented(config.STT{Provider: "openai", OpenAIBaseURL: base}); got != want {
			t.Errorf("%q: got %v, want %v", base, got, want)
		}
	}
	if localSegmented(config.STT{Provider: "soniox", OpenAIBaseURL: "http://127.0.0.1/v1"}) {
		t.Error("another provider counted as a local endpoint")
	}
}

// tone is d of a 440 Hz tone (speech, to the segmenter) or of silence.
func tone(d time.Duration, loud bool) []byte {
	n := int(d.Seconds() * 16000)
	b := make([]byte, 2*n)
	for i := 0; i < n; i++ {
		v := 0.0
		if loud {
			v = 8000 * math.Sin(2*math.Pi*440*float64(i)/16000)
		}
		binary.LittleEndian.PutUint16(b[2*i:], uint16(int16(v)))
	}
	return b
}

// fakeEndpoint answers each request with the clip's length in whole seconds
// ("s7"), failing those that fail says to.
func fakeEndpoint(t *testing.T, fail func(sec float64) bool) (*httptest.Server, *[]float64) {
	var mu sync.Mutex
	var seen []float64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		b, _ := io.ReadAll(f)
		sec := float64(len(b)-44) / 32000
		mu.Lock()
		seen = append(seen, sec)
		mu.Unlock()
		if fail != nil && fail(sec) {
			http.Error(w, "boom", 500)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"text": fmt.Sprintf("s%.0f", math.Floor(sec)), "language": "nl"})
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func feed(s Streamer, parts ...[]byte) {
	for _, p := range parts {
		for off := 0; off < len(p); off += 3200 {
			s.Send(p[off:min(off+3200, len(p))])
		}
	}
}

func TestLocalStreamSegments(t *testing.T) {
	srv, _ := fakeEndpoint(t, nil)
	cfg := config.STT{Provider: "openai", OpenAIBaseURL: srv.URL + "/v1"}
	s := NewStream(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if _, ok := s.(*whistleStream); !ok {
		t.Fatalf("a local endpoint got %T", s)
	}
	// Two pauses after more than localSegMin of speech, one after less: two cuts.
	feed(s, tone(7*time.Second, true), tone(600*time.Millisecond, false),
		tone(2*time.Second, true), tone(600*time.Millisecond, false),
		tone(5*time.Second, true), tone(600*time.Millisecond, false),
		tone(3*time.Second, true))
	text, err := s.Finish(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Fields(text); len(got) != 3 {
		t.Fatalf("want three pieces, got %q", text)
	}
	if s.Language() != "nl" {
		t.Errorf("language %q", s.Language())
	}
}

func TestLocalStreamFallsBackToWhole(t *testing.T) {
	// Every piece of 6 to 9 s fails; the whole recording (17 s) does not.
	srv, seen := fakeEndpoint(t, func(sec float64) bool { return sec >= 6 && sec < 9 })
	cfg := config.STT{Provider: "openai", OpenAIBaseURL: srv.URL + "/v1"}
	s := NewStream(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	feed(s, tone(7*time.Second, true), tone(600*time.Millisecond, false),
		tone(7*time.Second, true), tone(600*time.Millisecond, false), tone(2*time.Second, true))
	text, err := s.Finish(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if text != "s17" {
		t.Fatalf("want the whole recording's text, got %q (requests %v)", text, *seen)
	}
}

func TestRemoteEndpointStaysWhole(t *testing.T) {
	s := NewStream(config.STT{Provider: "openai", OpenAIBaseURL: "https://api.groq.com/openai/v1"}, slog.Default(), nil)
	if _, ok := s.(*openaiStream); !ok {
		t.Fatalf("a remote endpoint got %T", s)
	}
}

// TestLocalStreamLive plays a recording into a session against a running
// endpoint in real time, and logs the live text and how long the final text
// takes after the stop.
//
//	VITO_LOCAL_STREAM=path/to/clip.wav VITO_LOCAL_URL=http://127.0.0.1:PORT/v1 go test ./internal/stt -run TestLocalStreamLive -v
func TestLocalStreamLive(t *testing.T) {
	path, base := os.Getenv("VITO_LOCAL_STREAM"), os.Getenv("VITO_LOCAL_URL")
	if path == "" || base == "" {
		t.Skip("set VITO_LOCAL_STREAM and VITO_LOCAL_URL")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pcm, err := pcmFromWAV(b)
	if err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	s := NewStream(config.STT{Provider: "openai", OpenAIBaseURL: base, OpenAIModel: "parakeet"}, slog.Default(), func(p string) {
		t.Logf("%5.1fs live: %s", time.Since(began).Seconds(), p)
	})
	for off := 0; off < len(pcm); off += 3200 {
		s.Send(pcm[off:min(off+3200, len(pcm))])
		time.Sleep(100 * time.Millisecond)
	}
	stop := time.Now()
	text, err := s.Finish(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("after the stop: %.2fs\n%s", time.Since(stop).Seconds(), text)
}

func TestJoinPieces(t *testing.T) {
	for _, c := range []struct {
		in   []string
		want string
	}{
		{[]string{"kleur moet zijn als...", "De tekst of rood."}, "kleur moet zijn als de tekst of rood."},
		{[]string{"we parakiet…", "Bijvoorbeeld na tien."}, "we parakiet Bijvoorbeeld na tien."},
		{[]string{"Eén zin.", "Vito doet het."}, "Eén zin. Vito doet het."},
		{[]string{"zegt...", "NL is klein."}, "zegt NL is klein."},
		{[]string{"en dan...", ""}, "en dan..."},
		{[]string{"Op...", "Én verder"}, "Op én verder"},
	} {
		if got := joinPieces(c.in); got != c.want {
			t.Errorf("%q: got %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPiecesOnlyWithCleanup(t *testing.T) {
	local := config.STT{Provider: "openai", OpenAIBaseURL: "http://127.0.0.1:4000/v1"}
	if !localSegmented(Pieces(local, true)) || !LiveText(Pieces(local, true)) {
		t.Error("with the cleanup on, a local endpoint should go piece by piece")
	}
	if localSegmented(Pieces(local, false)) || LiveText(Pieces(local, false)) {
		t.Error("without the cleanup, a local endpoint should get the recording whole")
	}
	if _, ok := NewStream(Pieces(local, false), slog.Default(), nil).(*openaiStream); !ok {
		t.Error("without the cleanup, NewStream should send the recording whole")
	}
	if !LiveText(Pieces(config.STT{Provider: "local"}, true)) || LiveText(Pieces(config.STT{Provider: "local"}, false)) {
		t.Error("configured Vito local follows the cleanup too")
	}
	if !LiveText(Pieces(config.STT{Provider: "soniox"}, false)) {
		t.Error("a streaming service has live text regardless")
	}
}
