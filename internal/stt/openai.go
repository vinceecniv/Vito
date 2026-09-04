package stt

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"vito/internal/apierr"
	"vito/internal/config"
)

// Any endpoint that speaks the OpenAI audio API — POST /audio/transcriptions
// with a multipart body — is one provider here: OpenAI itself, Groq's hosted
// Whisper, or a server on this machine (whisper.cpp's whisper-server, Speaches,
// a Parakeet wrapper). That last case is the point: with a local endpoint a
// dictation never leaves the computer, and the engine behind it is the user's
// choice rather than Vito's. The API is batch-only, so there is no live
// transcript; a recording is sent whole when it ends (see openaiStream).

type openaiClient struct {
	cfg      config.STT
	keyterms []string
	http     *http.Client
}

func newOpenAIClient(cfg config.STT, keyterms []string) *openaiClient {
	// No client-level timeout: a local model on a slow CPU, or an hour of
	// uploaded audio, takes as long as it takes — the caller's context decides.
	return &openaiClient{cfg: cfg, keyterms: keyterms, http: &http.Client{}}
}

// OpenAIConfigured reports whether the endpoint provider has what it needs,
// which is only an endpoint: a key is optional, a local server usually has none.
func OpenAIConfigured(cfg config.STT) bool {
	return strings.TrimSpace(cfg.OpenAIBaseURL) != ""
}

// OpenAIName is the endpoint's display name for errors and the out-of-credit
// UI, derived from the base URL the way the cleanup side does it.
func OpenAIName(cfg config.STT) string {
	switch b := strings.ToLower(cfg.OpenAIBaseURL); {
	case strings.Contains(b, "groq.com"):
		return "Groq"
	case strings.Contains(b, "openai.com"):
		return "OpenAI"
	case strings.Contains(b, "localhost"), strings.Contains(b, "127.0.0.1"), strings.Contains(b, "0.0.0.0"):
		return "Local speech model"
	default:
		return "Speech endpoint"
	}
}

// OpenAIRateUSD is the published per-hour price of the endpoint's transcription
// model, and exactly 0 for a server of your own — which the cost card must show
// as free rather than fall back to some default rate.
func OpenAIRateUSD(cfg config.STT) float64 {
	b := strings.ToLower(cfg.OpenAIBaseURL)
	m := strings.ToLower(cfg.OpenAIModel)
	switch {
	case strings.Contains(b, "groq.com"):
		if strings.Contains(m, "turbo") {
			return 0.04 // whisper-large-v3-turbo
		}
		return 0.111 // whisper-large-v3
	case strings.Contains(b, "openai.com"):
		if strings.Contains(m, "mini") {
			return 0.18 // gpt-4o-mini-transcribe, $0.003/min
		}
		return 0.36 // whisper-1 and gpt-4o-transcribe, $0.006/min
	}
	return 0
}

func (c *openaiClient) endpoint() string {
	return strings.TrimRight(strings.TrimSpace(c.cfg.OpenAIBaseURL), "/") + "/audio/transcriptions"
}

// whisperCppEndpoint is where whisper.cpp's whisper-server listens unless it
// was started with --inference-path: at /inference on the server root, not
// under the base URL. It is the local server people are most likely to run,
// and the request it takes is the same multipart body, so a 404 on the OpenAI
// path is worth one try there before giving up.
func (c *openaiClient) whisperCppEndpoint() string {
	base := strings.TrimSpace(c.cfg.OpenAIBaseURL)
	if i := strings.Index(base, "://"); i >= 0 {
		if j := strings.Index(base[i+3:], "/"); j >= 0 {
			base = base[:i+3+j]
		}
	}
	return strings.TrimRight(base, "/") + "/inference"
}

// TranscribeFile makes the client a Transcriber for a file Vito recorded.
func (c *openaiClient) TranscribeFile(ctx context.Context, path string) (string, error) {
	out, err := c.transcribeUpload(ctx, path, func(UploadProgress) {})
	return out.Text, err
}

// transcribeUpload transcribes a file the user supplied. Decoding is the
// server's job — OpenAI, Groq and Speaches take the usual formats; a bare
// whisper.cpp server wants WAV unless started with --convert.
func (c *openaiClient) transcribeUpload(ctx context.Context, path string, onProgress func(UploadProgress)) (UploadOutcome, error) {
	// One request does both halves, so the moment the body is fully handed to
	// the transport is the moment the bar becomes a spinner.
	progress := func(p UploadProgress) {
		onProgress(p)
		if p.Phase == PhaseUpload && p.Frac >= 1 {
			onProgress(UploadProgress{Phase: PhaseTranscribe, Frac: -1})
		}
	}
	open := func() (io.ReadCloser, error) {
		src, closeFile, _, err := openWithProgress(path, progress)
		if err != nil {
			return nil, err
		}
		return &readCloser{Reader: src, close: closeFile}, nil
	}
	return c.transcribe(ctx, open, filepath.Base(path), 0)
}

// transcribe posts one audio body and reads the transcript back. open is asked
// for the body afresh on the one retry (see below), which is why it is a
// function rather than a reader. seconds is the audio length when the caller
// knows it (a recording Vito made), 0 when it doesn't (an uploaded file).
func (c *openaiClient) transcribe(ctx context.Context, open func() (io.ReadCloser, error), filename string, seconds float64) (UploadOutcome, error) {
	// verbose_json is what carries the detected language and the duration;
	// everything real speaks it, but a thin wrapper around some other engine may
	// only know "json", and says so with a 400 naming the parameter. One retry
	// with plain json then costs the language and nothing else.
	url := c.endpoint()
	out, err := c.post(ctx, url, open, filename, "verbose_json", seconds)
	if isNotFound(err) {
		url = c.whisperCppEndpoint()
		out, err = c.post(ctx, url, open, filename, "verbose_json", seconds)
	}
	if err != nil && isFormatRejection(err) {
		out, err = c.post(ctx, url, open, filename, "json", seconds)
	}
	return out, err
}

// Whisper hears 30 seconds at a time: a shorter recording is padded with
// silence and the encoder — the bulk of the work on a CPU — runs over all of it
// regardless, so a five-second dictation costs as much as a thirty-second one.
// whisper.cpp exposes the encoder's context length (1500 frames = 30 s), and
// trimming it to what the audio actually fills is what makes a short dictation
// cheap: measured here, a 13-second clip went from 9.8 s to 5.3 s and a 9-second
// one to 3.8 s, with the text unchanged. It is whisper.cpp's own parameter, not
// the OpenAI API's, so it is sent only where whisper.cpp may be listening: never
// to OpenAI or Groq, which would reject a field they don't know.
const (
	whisperFramesPerSecond = 50
	whisperMaxAudioCtx     = 1500
	whisperMinAudioCtx     = 256 // below this Whisper starts to lose the end of the audio
	whisperAudioCtxPad     = 64  // ≈ 1.3 s of headroom past the end of the recording
)

func audioCtx(seconds float64) int {
	if seconds <= 0 {
		return 0
	}
	ctx := int(seconds*whisperFramesPerSecond) + whisperAudioCtxPad
	if ctx < whisperMinAudioCtx {
		ctx = whisperMinAudioCtx
	}
	if ctx >= whisperMaxAudioCtx {
		return 0 // the full window: nothing to trim, so nothing to send
	}
	return ctx
}

// sendsAudioCtx reports whether the endpoint may be a whisper.cpp server.
func (c *openaiClient) sendsAudioCtx() bool {
	switch OpenAIName(c.cfg) {
	case "OpenAI", "Groq":
		return false
	}
	return true
}

func (c *openaiClient) post(ctx context.Context, url string, open func() (io.ReadCloser, error), filename, format string, seconds float64) (UploadOutcome, error) {
	src, err := open()
	if err != nil {
		return UploadOutcome{}, err
	}
	defer src.Close()

	// Streamed through a pipe rather than buffered, as for Soniox: an uploaded
	// hour of audio is far too much to hold just to wrap it in multipart.
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		fields := map[string]string{"response_format": format}
		if m := strings.TrimSpace(c.cfg.OpenAIModel); m != "" {
			fields["model"] = m
		}
		// "auto" is not a language code to OpenAI (HTTP 400); leaving the field
		// out means auto-detect everywhere. Whisper detects on the first seconds
		// and, on a short clip, misses — the settings page says to pin it.
		if l := strings.TrimSpace(c.cfg.Language); l != "" && l != "auto" {
			fields["language"] = l
		}
		// The dictionary's keyterms, as Whisper's prompt: the model leans towards
		// spellings it has just seen, which is exactly what names and jargon
		// need. Endpoints that don't prompt (a Parakeet wrapper) ignore the field.
		if len(c.keyterms) > 0 {
			fields["prompt"] = strings.Join(c.keyterms, ", ")
		}
		if n := audioCtx(seconds); n > 0 && c.sendsAudioCtx() {
			fields["audio_ctx"] = strconv.Itoa(n)
		}
		for k, v := range fields {
			if err := mw.WriteField(k, v); err != nil {
				pw.CloseWithError(err)
				return
			}
		}
		part, err := mw.CreateFormFile("file", filename)
		if err != nil {
			pw.CloseWithError(err)
			return
		}
		if _, err := io.Copy(part, src); err != nil {
			pw.CloseWithError(err)
			return
		}
		pw.CloseWithError(mw.Close())
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, pr)
	if err != nil {
		return UploadOutcome{}, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if k := strings.TrimSpace(c.cfg.OpenAIKey); k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return UploadOutcome{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return UploadOutcome{}, err
	}
	if resp.StatusCode/100 != 2 {
		if e := apierr.FromHTTP(OpenAIName(c.cfg), resp.StatusCode, string(body)); e != nil {
			return UploadOutcome{}, e
		}
		return UploadOutcome{}, &httpError{status: resp.StatusCode, body: truncate(string(body), 300)}
	}
	var out struct {
		Text     string  `json:"text"`
		Language string  `json:"language"`
		Duration float64 `json:"duration"` // seconds
	}
	if err := json.Unmarshal(body, &out); err != nil {
		// A server that answered plain text to a json request is still an answer.
		if format == "json" && len(strings.TrimSpace(string(body))) > 0 && !strings.HasPrefix(strings.TrimSpace(string(body)), "{") {
			out.Text = string(body)
		} else {
			return UploadOutcome{}, fmt.Errorf("unexpected response: %s", truncate(string(body), 200))
		}
	}
	// whisper.cpp joins its segments with newlines — which, typed at the cursor,
	// would break a dictated sentence across lines. The newline carries no
	// spacing of its own: a segment that starts a new word starts with a space
	// (" motion"), one that continues a word does not ("geto\nond" is one word),
	// so the newline is dropped, not replaced, and the spaces are then tidied.
	text := strings.Join(strings.Fields(strings.ReplaceAll(out.Text, "\n", "")), " ")
	if hallucinated(text) {
		text = ""
	}
	// The language the server reports is only worth having when Vito asked it
	// to detect one. With a pinned language every server echoes that back at
	// best — and a Parakeet server, which never detects, says "en" regardless,
	// which would flag every Dutch dictation in the history as English.
	lang := ""
	if l := strings.TrimSpace(c.cfg.Language); l == "" || l == "auto" {
		lang = isoLang(out.Language)
	}
	return UploadOutcome{
		Text:       text,
		Language:   lang,
		DurationMS: int64(out.Duration * 1000),
	}, nil
}

// httpError is a non-2xx answer that is not a billing problem, kept as a type
// so the verbose_json retry can look at the status without parsing a message.
type httpError struct {
	status int
	body   string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("speech endpoint: HTTP %d: %s", e.status, e.body)
}

func isNotFound(err error) bool {
	e, ok := err.(*httpError)
	return ok && e.status == 404
}

func isFormatRejection(err error) bool {
	e, ok := err.(*httpError)
	return ok && (e.status == 400 || e.status == 422) && strings.Contains(strings.ToLower(e.body), "response_format")
}

// hallucinated recognises what Whisper says when it hears nothing. Trained on
// subtitles, it fills silence with a subtitle credit, in the language it was
// told to expect — which here would be typed straight at the cursor. Only a
// transcript that is nothing but such a line is dropped; one that merely ends
// in it after real speech is left alone, because that cut would be worse.
func hallucinated(text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	if t == "" || len(t) > 80 {
		return false
	}
	// Whole phrases, not words: someone can dictate a sentence about subtitles.
	for _, p := range []string{
		"amara.org", "ondertiteling door", "ondertiteld door", "ondertitels door", "bedankt voor het kijken",
		"thank you for watching", "thanks for watching", "subtitles by", "subtitled by",
		"untertitelung von", "untertitel von", "sous-titres par", "sous-titrage", "subtítulos por", "sottotitoli di",
	} {
		if strings.Contains(t, p) {
			return true
		}
	}
	return false
}

// isoLang normalises the language a server reports. whisper.cpp and Speaches
// answer with a code ("nl"); OpenAI answers verbose_json with the English name
// ("dutch"). The history stores codes, so the names are mapped for the
// languages Vito offers and anything unrecognised is dropped rather than
// stored as a flag that cannot be drawn.
func isoLang(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	if i := strings.IndexAny(s, "-_"); i > 0 {
		s = s[:i] // "nl-NL"
	}
	if len(s) == 2 {
		return s
	}
	if code, ok := whisperLangNames[s]; ok {
		return code
	}
	return ""
}

var whisperLangNames = map[string]string{
	"english": "en", "dutch": "nl", "german": "de", "french": "fr", "spanish": "es", "italian": "it",
	"portuguese": "pt", "polish": "pl", "russian": "ru", "ukrainian": "uk", "czech": "cs", "slovak": "sk",
	"swedish": "sv", "danish": "da", "norwegian": "no", "finnish": "fi", "greek": "el", "hungarian": "hu",
	"romanian": "ro", "bulgarian": "bg", "croatian": "hr", "serbian": "sr", "slovenian": "sl", "bosnian": "bs",
	"albanian": "sq", "macedonian": "mk", "belarusian": "be", "lithuanian": "lt", "latvian": "lv", "estonian": "et",
	"catalan": "ca", "basque": "eu", "galician": "gl", "welsh": "cy", "turkish": "tr", "azerbaijani": "az",
	"kazakh": "kk", "arabic": "ar", "hebrew": "he", "persian": "fa", "urdu": "ur", "hindi": "hi",
	"bengali": "bn", "gujarati": "gu", "punjabi": "pa", "marathi": "mr", "kannada": "kn", "malayalam": "ml",
	"tamil": "ta", "telugu": "te", "chinese": "zh", "japanese": "ja", "korean": "ko", "vietnamese": "vi",
	"thai": "th", "indonesian": "id", "malay": "ms", "tagalog": "tl", "afrikaans": "af", "swahili": "sw",
}

// readCloser pairs a reader with the cleanup that goes with it.
type readCloser struct {
	io.Reader
	close func() error
}

func (r *readCloser) Close() error {
	if r.close == nil {
		return nil
	}
	c := r.close
	r.close = nil
	return c()
}
