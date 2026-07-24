package stt

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"vito/internal/apierr"
	"vito/internal/config"
)

// Transcribing a file the user supplied, rather than something Vito recorded
// itself. Both providers do this over their asynchronous REST APIs: upload the
// file, start a job, poll until it is done. The formats they accept (mp3, m4a,
// wav, ogg, flac, video containers) are decoded on their side, so Vito never
// has to know how to decode anything.

// UploadPhase says which half of the job is running, so the UI can show a
// determinate bar for the upload and a spinner for the transcription.
type UploadPhase string

const (
	PhaseUpload     UploadPhase = "upload"
	PhaseTranscribe UploadPhase = "transcribe"
)

// UploadProgress is reported as the job advances. Frac is 0..1 while uploading
// and -1 while transcribing, where no provider tells us how far along it is.
type UploadProgress struct {
	Phase UploadPhase
	Frac  float64
}

// UploadOutcome is what a provider gives back about a transcribed file. The
// duration comes from the provider rather than from the browser: a media
// element refuses to read metadata while its tab is in the background, and this
// number decides both the statistics and what the job cost.
type UploadOutcome struct {
	Text       string
	Language   string
	DurationMS int64
}

// TranscribeUpload transcribes an audio file with whichever provider is
// configured, and reports the language it detected when the provider says so.
func TranscribeUpload(ctx context.Context, cfg config.STT, keyterms []string, path string, onProgress func(UploadProgress)) (UploadOutcome, error) {
	if onProgress == nil {
		onProgress = func(UploadProgress) {}
	}
	if cfg.Provider == "soniox" {
		return newSonioxFileClient(cfg, keyterms).transcribe(ctx, path, onProgress)
	}
	return NewAsyncClient(cfg, keyterms).transcribeWithProgress(ctx, path, onProgress)
}

// UploadRateUSD is the price per hour of audio for file transcription, which is
// not the streaming price: providers charge less for pre-recorded work. Used
// for the estimate shown before a file is sent and for the cost card after.
func UploadRateUSD(provider string) float64 {
	if provider == "soniox" {
		return 0.10 // stt-async-v5
	}
	return 0.15 // AssemblyAI pre-recorded, default model
}

// progressReader reports how much of a body has been handed to the transport,
// throttled so a large file doesn't flood the event stream.
type progressReader struct {
	r      io.Reader
	n      int64
	total  int64
	report func(float64)
	last   time.Time
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.n += int64(n)
	if p.total > 0 && (time.Since(p.last) > 150*time.Millisecond || err == io.EOF) {
		p.last = time.Now()
		frac := float64(p.n) / float64(p.total)
		if frac > 1 {
			frac = 1
		}
		p.report(frac)
	}
	return n, err
}

func openWithProgress(path string, onProgress func(UploadProgress)) (io.Reader, func() error, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, 0, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, 0, err
	}
	pr := &progressReader{r: f, total: st.Size(), report: func(frac float64) {
		onProgress(UploadProgress{Phase: PhaseUpload, Frac: frac})
	}}
	return pr, f.Close, st.Size(), nil
}

// ---- AssemblyAI ------------------------------------------------------------

func (c *AsyncClient) transcribeWithProgress(ctx context.Context, path string, onProgress func(UploadProgress)) (UploadOutcome, error) {
	body, closeFile, _, err := openWithProgress(path, onProgress)
	if err != nil {
		return UploadOutcome{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase(c.cfg)+"/v2/upload", body)
	if err != nil {
		closeFile()
		return UploadOutcome{}, err
	}
	req.Header.Set("Authorization", c.cfg.APIKey)
	req.Header.Set("Content-Type", "application/octet-stream")
	var up struct {
		UploadURL string `json:"upload_url"`
	}
	err = c.do(req, &up)
	closeFile()
	if err != nil {
		return UploadOutcome{}, fmt.Errorf("upload: %w", err)
	}

	onProgress(UploadProgress{Phase: PhaseTranscribe, Frac: -1})
	// Deliberately no speech_model: the ids in the settings name streaming
	// models, which the pre-recorded endpoint rejects. Its own default applies.
	id, err := c.createTranscript(ctx, up.UploadURL)
	if err != nil {
		return UploadOutcome{}, err
	}
	return c.pollUpload(ctx, id)
}

func (c *AsyncClient) pollUpload(ctx context.Context, id string) (UploadOutcome, error) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return UploadOutcome{}, ctx.Err()
		case <-ticker.C:
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase(c.cfg)+"/v2/transcript/"+id, nil)
		if err != nil {
			return UploadOutcome{}, err
		}
		req.Header.Set("Authorization", c.cfg.APIKey)
		var out struct {
			Status        string  `json:"status"`
			Text          string  `json:"text"`
			LanguageCode  string  `json:"language_code"`
			AudioDuration float64 `json:"audio_duration"` // seconds
			Error         string  `json:"error"`
		}
		if err := c.do(req, &out); err != nil {
			return UploadOutcome{}, fmt.Errorf("poll transcript: %w", err)
		}
		switch out.Status {
		case "completed":
			return UploadOutcome{Text: out.Text, Language: out.LanguageCode, DurationMS: int64(out.AudioDuration * 1000)}, nil
		case "error":
			return UploadOutcome{}, fmt.Errorf("transcription failed: %s", out.Error)
		}
	}
}

// ---- Soniox ----------------------------------------------------------------

// SonioxAsyncModel is the pre-recorded counterpart of the realtime model.
const SonioxAsyncModel = "stt-async-v5"

const sonioxAPI = "https://api.soniox.com"

type sonioxFileClient struct {
	cfg      config.STT
	keyterms []string
	http     *http.Client
}

func newSonioxFileClient(cfg config.STT, keyterms []string) *sonioxFileClient {
	// No client-level timeout: an hour of audio takes minutes, and the caller's
	// context is what should decide when to give up.
	return &sonioxFileClient{cfg: cfg, keyterms: keyterms, http: &http.Client{}}
}

func (c *sonioxFileClient) transcribe(ctx context.Context, path string, onProgress func(UploadProgress)) (UploadOutcome, error) {
	fileID, err := c.upload(ctx, path, onProgress)
	if err != nil {
		return UploadOutcome{}, fmt.Errorf("upload: %w", err)
	}
	// Whatever happens next, don't leave the audio sitting on their servers.
	defer c.delete(ctx, "/v1/files/"+fileID)

	onProgress(UploadProgress{Phase: PhaseTranscribe, Frac: -1})
	jobID, err := c.createJob(ctx, fileID)
	if err != nil {
		return UploadOutcome{}, err
	}
	defer c.delete(ctx, "/v1/transcriptions/"+jobID)

	durationMS, err := c.waitFor(ctx, jobID)
	if err != nil {
		return UploadOutcome{}, err
	}
	out, err := c.transcript(ctx, jobID)
	out.DurationMS = durationMS
	return out, err
}

func (c *sonioxFileClient) upload(ctx context.Context, path string, onProgress func(UploadProgress)) (string, error) {
	src, closeFile, size, err := openWithProgress(path, onProgress)
	if err != nil {
		return "", err
	}
	defer closeFile()

	// Streamed through a pipe rather than buffered: an hour of audio is far too
	// much to hold in memory just to wrap it in a multipart envelope.
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		part, err := mw.CreateFormFile("file", filepath.Base(path))
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
	_ = size

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sonioxAPI+"/v1/files", pr)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	var out struct {
		ID string `json:"id"`
	}
	if err := c.do(req, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

func (c *sonioxFileClient) createJob(ctx context.Context, fileID string) (string, error) {
	body := map[string]any{
		"model":                          SonioxAsyncModel,
		"file_id":                        fileID,
		"enable_language_identification": true,
	}
	if c.cfg.Language != "auto" && c.cfg.Language != "" {
		body["language_hints"] = []string{c.cfg.Language}
	}
	if len(c.keyterms) > 0 {
		body["context"] = map[string]any{"terms": c.keyterms}
	}
	data, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sonioxAPI+"/v1/transcriptions", strings.NewReader(string(data)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	var out struct {
		ID string `json:"id"`
	}
	if err := c.do(req, &out); err != nil {
		return "", fmt.Errorf("create transcription: %w", err)
	}
	return out.ID, nil
}

// waitFor blocks until the job is done and reports the audio length Soniox
// measured, in milliseconds.
func (c *sonioxFileClient) waitFor(ctx context.Context, jobID string) (int64, error) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-ticker.C:
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, sonioxAPI+"/v1/transcriptions/"+jobID, nil)
		if err != nil {
			return 0, err
		}
		var out struct {
			Status          string `json:"status"`
			AudioDurationMS int64  `json:"audio_duration_ms"`
			ErrorMessage    string `json:"error_message"`
		}
		if err := c.do(req, &out); err != nil {
			return 0, fmt.Errorf("poll transcription: %w", err)
		}
		switch out.Status {
		case "completed":
			return out.AudioDurationMS, nil
		case "error":
			return 0, fmt.Errorf("transcription failed: %s", out.ErrorMessage)
		}
	}
}

func (c *sonioxFileClient) transcript(ctx context.Context, jobID string) (UploadOutcome, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sonioxAPI+"/v1/transcriptions/"+jobID+"/transcript", nil)
	if err != nil {
		return UploadOutcome{}, err
	}
	var out struct {
		Text   string        `json:"text"`
		Tokens []sonioxToken `json:"tokens"`
	}
	if err := c.do(req, &out); err != nil {
		return UploadOutcome{}, fmt.Errorf("fetch transcript: %w", err)
	}
	text := out.Text
	langs := map[string]int{}
	if text == "" {
		var b strings.Builder
		for _, tk := range out.Tokens {
			b.WriteString(tk.Text) // tokens carry their own spacing
		}
		text = b.String()
	}
	for _, tk := range out.Tokens {
		if tk.Language != "" && strings.TrimSpace(tk.Text) != "" {
			langs[tk.Language]++
		}
	}
	best, bestN := "", 0
	for l, n := range langs {
		if n > bestN {
			best, bestN = l, n
		}
	}
	return UploadOutcome{Text: strings.TrimSpace(text), Language: best}, nil
}

func (c *sonioxFileClient) delete(ctx context.Context, path string) {
	// Best effort, and never held up by a cancelled parent context.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, sonioxAPI+path, nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.SonioxAPIKey)
	resp, err := c.http.Do(req)
	if err == nil {
		resp.Body.Close()
	}
}

func (c *sonioxFileClient) do(req *http.Request, out any) error {
	req.Header.Set("Authorization", "Bearer "+c.cfg.SonioxAPIKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		if e := apierr.FromHTTP("Soniox", resp.StatusCode, string(body)); e != nil {
			return e
		}
		return fmt.Errorf("%s: HTTP %d: %s", req.URL.Path, resp.StatusCode, truncate(string(body), 200))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}
