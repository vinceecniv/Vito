package stt

import (
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vito/internal/apierr"
	"vito/internal/config"
)

// request is what the fake endpoint saw: the multipart fields and the audio.
type request struct {
	fields map[string]string
	file   []byte
	name   string
	auth   string
	path   string
}

// serveAudio stands in for an OpenAI-compatible /audio/transcriptions endpoint.
// reply decides the answer per request, so a test can reject the first call.
func serveAudio(t *testing.T, reply func(n int, r request) (int, string)) (*httptest.Server, *[]request) {
	t.Helper()
	var seen []request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" && r.URL.Path != "/inference" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			t.Errorf("multipart: %v", err)
		}
		got := request{fields: map[string]string{}, auth: r.Header.Get("Authorization"), path: r.URL.Path}
		for k, v := range r.MultipartForm.Value {
			got.fields[k] = v[0]
		}
		if f, hdr, err := r.FormFile("file"); err == nil {
			got.file, _ = io.ReadAll(f)
			got.name = hdr.Filename
			f.Close()
		}
		seen = append(seen, got)
		status, body := reply(len(seen), got)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func openaiCfg(base string) config.STT {
	return config.STT{Provider: "openai", OpenAIBaseURL: base + "/v1", OpenAIModel: "whisper-large-v3-turbo", OpenAIKey: "k-1", Language: "nl"}
}

func wavFile(t *testing.T, seconds int) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "clip.wav")
	if err := os.WriteFile(p, make([]byte, 16000*2*seconds), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// Segments joined with newlines, as whisper.cpp returns them — one of them cut
// through a word ("geto\nond"); one line with the word intact comes out.
const dutchReply = `{"task":"transcribe","language":"dutch","duration":2.5,"text":" Dit is\n een geto\nond test. \n"}`

// The request carries exactly what the audio API defines: the file, the model,
// the pinned language, the keyterms as prompt, and verbose_json for the language
// and duration that come back.
func TestOpenAIRequestAndReply(t *testing.T) {
	srv, seen := serveAudio(t, func(int, request) (int, string) { return 200, dutchReply })
	c := newOpenAIClient(openaiCfg(srv.URL), []string{"Vito", "Soniox"})
	out, err := c.transcribeUpload(context.Background(), wavFile(t, 1), func(UploadProgress) {})
	if err != nil {
		t.Fatal(err)
	}
	// The language is pinned to nl, so what the server says about it is not
	// used (a Parakeet server claims "en" for everything); the daemon falls back
	// to the configured language.
	if out.Text != "Dit is een getoond test." || out.Language != "" || out.DurationMS != 2500 {
		t.Errorf("outcome = %+v", out)
	}
	r := (*seen)[0]
	want := map[string]string{"model": "whisper-large-v3-turbo", "language": "nl", "prompt": "Vito, Soniox", "response_format": "verbose_json"}
	for k, v := range want {
		if r.fields[k] != v {
			t.Errorf("field %s = %q, want %q", k, r.fields[k], v)
		}
	}
	if r.auth != "Bearer k-1" || r.name != "clip.wav" || len(r.file) != 32000 {
		t.Errorf("auth=%q name=%q size=%d", r.auth, r.name, len(r.file))
	}
}

// "auto" is Vito's word, not the API's: it must leave the field out, and an
// empty key must leave the header out — a local server has neither.
func TestOpenAIAutoLanguageAndNoKey(t *testing.T) {
	srv, seen := serveAudio(t, func(int, request) (int, string) { return 200, `{"text":"ok","language":"dutch"}` })
	cfg := openaiCfg(srv.URL)
	cfg.Language, cfg.OpenAIKey, cfg.OpenAIModel = "auto", "", ""
	out, err := newOpenAIClient(cfg, nil).transcribeUpload(context.Background(), wavFile(t, 1), func(UploadProgress) {})
	if err != nil {
		t.Fatal(err)
	}
	if out.Language != "nl" {
		t.Errorf("with auto-detect the reported language should be kept, got %q", out.Language)
	}
	r := (*seen)[0]
	for _, k := range []string{"language", "model", "prompt"} {
		if _, ok := r.fields[k]; ok {
			t.Errorf("field %s should be absent, got %q", k, r.fields[k])
		}
	}
	if r.auth != "" {
		t.Errorf("Authorization = %q, want none", r.auth)
	}
}

// A wrapper that only knows "json" says so with a 400; the one retry must go
// out with plain json and still yield the text.
func TestOpenAIRetriesWithoutVerboseJSON(t *testing.T) {
	srv, seen := serveAudio(t, func(n int, r request) (int, string) {
		if r.fields["response_format"] == "verbose_json" {
			return 400, `{"error":"unsupported response_format: verbose_json"}`
		}
		return 200, `{"text":"tweede poging"}`
	})
	out, err := newOpenAIClient(openaiCfg(srv.URL), nil).transcribeUpload(context.Background(), wavFile(t, 1), func(UploadProgress) {})
	if err != nil {
		t.Fatal(err)
	}
	if out.Text != "tweede poging" || len(*seen) != 2 || (*seen)[1].fields["response_format"] != "json" {
		t.Errorf("out=%+v requests=%d", out, len(*seen))
	}
	// Any other 400 is final: retrying an unrelated rejection would double it.
	srv2, seen2 := serveAudio(t, func(int, request) (int, string) { return 400, `{"error":"file too large"}` })
	if _, err := newOpenAIClient(openaiCfg(srv2.URL), nil).TranscribeFile(context.Background(), wavFile(t, 1)); err == nil || len(*seen2) != 1 {
		t.Errorf("err=%v requests=%d, want one failed request", err, len(*seen2))
	}
}

// A whisper-server started without --inference-path only knows /inference at
// its root; a 404 on the OpenAI path is retried there, once, with the same body.
func TestOpenAIFallsBackToWhisperCppPath(t *testing.T) {
	srv, seen := serveAudio(t, func(n int, r request) (int, string) {
		if r.path != "/inference" {
			return 404, "File Not Found (/v1/audio/transcriptions)"
		}
		return 200, dutchReply
	})
	out, err := newOpenAIClient(openaiCfg(srv.URL), nil).transcribeUpload(context.Background(), wavFile(t, 1), func(UploadProgress) {})
	if err != nil || out.Text != "Dit is een getoond test." {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	if len(*seen) != 2 || (*seen)[1].path != "/inference" || (*seen)[1].fields["language"] != "nl" {
		t.Errorf("requests = %+v", *seen)
	}
	if got := newOpenAIClient(config.STT{OpenAIBaseURL: "http://127.0.0.1:8080/v1/"}, nil).whisperCppEndpoint(); got != "http://127.0.0.1:8080/inference" {
		t.Errorf("whisperCppEndpoint = %q", got)
	}
}

// A recording's length trims Whisper's encoder window on a server that may be
// whisper.cpp, never on OpenAI or Groq, and never for a file of unknown length.
func TestAudioCtx(t *testing.T) {
	for seconds, want := range map[float64]int{0: 0, 1: 256, 5: 314, 13.2: 724, 28: 1464, 29: 0, 60: 0} {
		if got := audioCtx(seconds); got != want {
			t.Errorf("audioCtx(%v) = %d, want %d", seconds, got, want)
		}
	}
	srv, seen := serveAudio(t, func(int, request) (int, string) { return 200, dutchReply })
	s := newOpenAIStream(openaiCfg(srv.URL), nil, slog.Default())
	s.Send(make([]byte, 32000*5)) // five seconds
	if _, err := s.Finish(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := (*seen)[0].fields["audio_ctx"]; got != "314" {
		t.Errorf("audio_ctx = %q, want 314 for a local server", got)
	}
	// An uploaded file: length unknown, so the full window.
	if _, err := newOpenAIClient(openaiCfg(srv.URL), nil).TranscribeFile(context.Background(), wavFile(t, 1)); err != nil {
		t.Fatal(err)
	}
	if _, ok := (*seen)[1].fields["audio_ctx"]; ok {
		t.Error("audio_ctx should not be sent for an upload")
	}
	groq := openaiCfg(srv.URL)
	groq.OpenAIBaseURL = "https://api.groq.com/openai/v1"
	if c := newOpenAIClient(groq, nil); c.sendsAudioCtx() {
		t.Error("Groq must not be sent whisper.cpp's audio_ctx")
	}
}

func TestOpenAICreditAndName(t *testing.T) {
	srv, _ := serveAudio(t, func(int, request) (int, string) { return 402, `{"error":"insufficient credit"}` })
	_, err := newOpenAIClient(openaiCfg(srv.URL), nil).TranscribeFile(context.Background(), wavFile(t, 1))
	if p, ok := apierr.CreditProvider(err); !ok || p != "Local speech model" {
		t.Errorf("credit provider = %q,%v (err %v)", p, ok, err)
	}
	for base, want := range map[string]string{
		"https://api.groq.com/openai/v1": "Groq", "https://api.openai.com/v1": "OpenAI",
		"http://localhost:8080/v1": "Local speech model", "http://nas.lan:9000/v1": "Speech endpoint",
	} {
		if got := OpenAIName(config.STT{OpenAIBaseURL: base}); got != want {
			t.Errorf("OpenAIName(%s) = %q, want %q", base, got, want)
		}
	}
}

// Silence comes back as a subtitle credit; that alone is dropped, speech that
// merely ends in one is not.
func TestHallucinated(t *testing.T) {
	for text, want := range map[string]bool{
		"Ondertiteling door de Amara.org gemeenschap": true,
		"Bedankt voor het kijken!":                    true,
		"Thanks for watching.":                        true,
		"":                                            false,
		"Dit is een gewone zin over ondertitels.": false, // real speech mentioning the word
		"Ik heb gisteren de film gezien en de ondertiteling was slecht, bedankt voor het kijken naar mijn recensie.": false,
	} {
		if got := hallucinated(text); got != want {
			t.Errorf("hallucinated(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestIsoLang(t *testing.T) {
	for in, want := range map[string]string{"nl": "nl", "Dutch": "nl", "nl-NL": "nl", "english": "en", "klingon": "", "": ""} {
		if got := isoLang(in); got != want {
			t.Errorf("isoLang(%q) = %q, want %q", in, got, want)
		}
	}
}

// The session buffers everything and sends one WAV at the end, and the
// language it heard is available afterwards.
func TestOpenAIStreamSendsOneWAV(t *testing.T) {
	srv, seen := serveAudio(t, func(int, request) (int, string) { return 200, dutchReply })
	s := newOpenAIStream(openaiCfg(srv.URL), nil, slog.Default())
	for i := 0; i < 10; i++ {
		s.Send(make([]byte, 1600))
	}
	text, err := s.Finish(context.Background())
	if err != nil || text != "Dit is een getoond test." || s.Language() != "" { // pinned language: nothing reported back
		t.Fatalf("text=%q err=%v lang=%q", text, err, s.Language())
	}
	f := (*seen)[0].file
	if len(f) != 44+16000 || string(f[:4]) != "RIFF" || binary.LittleEndian.Uint32(f[24:]) != 16000 {
		t.Errorf("sent %d bytes, header %q", len(f), f[:4])
	}
	// Nothing recorded means nothing to send — not a request with an empty file.
	empty := newOpenAIStream(openaiCfg(srv.URL), nil, slog.Default())
	if text, err := empty.Finish(context.Background()); err != nil || text != "" || len(*seen) != 1 {
		t.Errorf("empty session: text=%q err=%v requests=%d", text, err, len(*seen))
	}
}

// The daemon's per-provider knobs: no partials and a long finish for the
// endpoint, its own file client as fallback for Soniox, none for the endpoint.
func TestProviderKnobs(t *testing.T) {
	ep := config.STT{Provider: "openai"}
	if HasPartials(ep) || FinishTimeout(ep) < time.Minute || Fallback(ep, nil) != nil {
		t.Error("endpoint provider: want no partials, a long finish, no fallback")
	}
	son := config.STT{Provider: "soniox"}
	if !HasPartials(son) || FinishTimeout(son) != 8*time.Second {
		t.Error("soniox: want partials and the short finish")
	}
	if _, ok := Fallback(son, nil).(*sonioxFileClient); !ok {
		t.Error("soniox: fallback should be Soniox's own file client")
	}
	if _, ok := Fallback(config.STT{Provider: "assemblyai"}, nil).(*AsyncClient); !ok {
		t.Error("assemblyai: fallback should be the async client")
	}
	if !strings.Contains(newOpenAIClient(config.STT{OpenAIBaseURL: "http://x/v1/"}, nil).endpoint(), "http://x/v1/audio/transcriptions") {
		t.Error("endpoint: trailing slash should not double up")
	}
}

func TestOpenAIRates(t *testing.T) {
	for _, tc := range []struct {
		base, model string
		want        float64
	}{
		{"https://api.groq.com/openai/v1", "whisper-large-v3-turbo", 0.04},
		{"https://api.groq.com/openai/v1", "whisper-large-v3", 0.111},
		{"https://api.openai.com/v1", "gpt-4o-mini-transcribe", 0.18},
		{"https://api.openai.com/v1", "whisper-1", 0.36},
		{"http://127.0.0.1:8080/v1", "", 0},
	} {
		cfg := config.STT{Provider: "openai", OpenAIBaseURL: tc.base, OpenAIModel: tc.model}
		if got := OpenAIRateUSD(cfg); got != tc.want {
			t.Errorf("rate(%s, %s) = %v, want %v", tc.base, tc.model, got, tc.want)
		}
		if got := UploadRateUSD(cfg); got != tc.want {
			t.Errorf("upload rate(%s) = %v, want %v", tc.base, got, tc.want)
		}
	}
}
