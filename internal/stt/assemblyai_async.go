package stt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"vito/internal/apierr"
	"vito/internal/config"
)

// AsyncClient transcribes a spool WAV via the v2 REST API. Slower than
// streaming, but it always works as long as the file exists — this is the
// "never lose a dictation" path.
type AsyncClient struct {
	cfg      config.STT
	keyterms []string
	http     *http.Client
}

func NewAsyncClient(cfg config.STT, keyterms []string) *AsyncClient {
	return &AsyncClient{cfg: cfg, keyterms: keyterms, http: &http.Client{Timeout: 60 * time.Second}}
}

func (c *AsyncClient) TranscribeFile(ctx context.Context, path string) (string, error) {
	audioURL, err := c.upload(ctx, path)
	if err != nil {
		return "", err
	}
	id, err := c.createTranscript(ctx, audioURL)
	if err != nil {
		return "", err
	}
	return c.poll(ctx, id)
}

func (c *AsyncClient) upload(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase(c.cfg)+"/v2/upload", f)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", c.cfg.APIKey)
	req.Header.Set("Content-Type", "application/octet-stream")

	var out struct {
		UploadURL string `json:"upload_url"`
	}
	if err := c.do(req, &out); err != nil {
		return "", fmt.Errorf("upload spool: %w", err)
	}
	return out.UploadURL, nil
}

func (c *AsyncClient) createTranscript(ctx context.Context, audioURL string) (string, error) {
	body := map[string]any{"audio_url": audioURL}
	if c.cfg.Language == "auto" {
		body["language_detection"] = true
	} else {
		body["language_code"] = c.cfg.Language
	}
	if len(c.keyterms) > 0 {
		body["word_boost"] = c.keyterms
	}
	data, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase(c.cfg)+"/v2/transcript", bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", c.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")

	var out struct {
		ID string `json:"id"`
	}
	if err := c.do(req, &out); err != nil {
		return "", fmt.Errorf("create transcript: %w", err)
	}
	return out.ID, nil
}

func (c *AsyncClient) poll(ctx context.Context, id string) (string, error) {
	ticker := time.NewTicker(800 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase(c.cfg)+"/v2/transcript/"+id, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", c.cfg.APIKey)

		var out struct {
			Status string `json:"status"`
			Text   string `json:"text"`
			Error  string `json:"error"`
		}
		if err := c.do(req, &out); err != nil {
			return "", fmt.Errorf("poll transcript: %w", err)
		}
		switch out.Status {
		case "completed":
			return out.Text, nil
		case "error":
			return "", fmt.Errorf("async transcription failed: %s", out.Error)
		}
	}
}

func (c *AsyncClient) do(req *http.Request, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		if e := apierr.FromHTTP("AssemblyAI", resp.StatusCode, string(body)); e != nil {
			return e
		}
		return fmt.Errorf("%s: HTTP %d: %s", req.URL.Path, resp.StatusCode, truncate(string(body), 200))
	}
	return json.Unmarshal(body, out)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
