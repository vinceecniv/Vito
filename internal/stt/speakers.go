package stt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"vito/internal/apierr"
	"vito/internal/config"
)

// Speaker recognition (diarization) for long recordings: the whole audio is
// transcribed once more, at Soniox or AssemblyAI, with each stretch of speech
// labelled by who said it. Only those two can; a local engine or a Whisper
// endpoint cannot.

// Turn is one speaker's uninterrupted stretch of speech. Speakers are
// numbered "1", "2", … in the order they first speak.
type Turn struct {
	Speaker string  `json:"speaker"`
	Start   float64 `json:"start"` // seconds
	End     float64 `json:"end"`
	Text    string  `json:"text"`
	// Words: when each word starts, where the service says (Soniox's
	// tokens, AssemblyAI's words). Empty for turns saved before.
	Words []WordTime `json:"words,omitempty"`
}

// WordTime is when a word of a turn starts: Off is where it begins in the
// turn's Text, counted in runes.
type WordTime struct {
	Off int     `json:"o"`
	At  float64 `json:"t"`
}

// shiftWords moves word positions by d runes and drops those that fall
// before the text.
func shiftWords(ws []WordTime, d int) []WordTime {
	out := ws[:0:0]
	for _, w := range ws {
		if w.Off+d >= 0 {
			out = append(out, WordTime{Off: w.Off + d, At: w.At})
		}
	}
	return out
}

// SpeakerProvider is the service that would recognise speakers with this
// configuration: the speech service in use when it can, else whichever of
// the two has a key; "" when neither does.
func SpeakerProvider(cfg config.STT) string {
	switch {
	case cfg.Provider == "soniox" && cfg.SonioxAPIKey != "":
		return "soniox"
	case cfg.Provider == "assemblyai" && cfg.APIKey != "":
		return "assemblyai"
	case cfg.SonioxAPIKey != "":
		return "soniox"
	case cfg.APIKey != "":
		return "assemblyai"
	}
	return ""
}

// SpeakerRateUSD is what speaker recognition costs per hour of audio.
func SpeakerRateUSD(provider string) float64 {
	if provider == "soniox" {
		return 0.10
	}
	return 0.17 // AssemblyAI pre-recorded plus speaker labels
}

// TranscribeSpeakers sends the file at path to provider with speaker
// recognition and returns the turns.
func TranscribeSpeakers(ctx context.Context, cfg config.STT, provider, path string) ([]Turn, error) {
	var (
		turns []Turn
		err   error
	)
	switch provider {
	case "soniox":
		turns, err = newSonioxFileClient(cfg).speakers(ctx, path)
	case "assemblyai":
		turns, err = NewAsyncClient(cfg).speakers(ctx, path)
	default:
		return nil, fmt.Errorf("speaker recognition needs a Soniox or AssemblyAI key")
	}
	if err != nil {
		return nil, err
	}
	return renumber(turns), nil
}

// renumber names the speakers 1, 2, … in the order they first speak, and
// joins a speaker's turns that follow each other.
func renumber(in []Turn) []Turn {
	ids := map[string]string{}
	var out []Turn
	for _, t := range in {
		lead := utf8.RuneCountInString(t.Text) - utf8.RuneCountInString(strings.TrimLeftFunc(t.Text, unicode.IsSpace))
		t.Text = strings.TrimSpace(t.Text)
		t.Words = shiftWords(t.Words, -lead)
		if t.Text == "" {
			continue
		}
		id, ok := ids[t.Speaker]
		if !ok {
			id = strconv.Itoa(len(ids) + 1)
			ids[t.Speaker] = id
		}
		t.Speaker = id
		if n := len(out); n > 0 && out[n-1].Speaker == id {
			out[n-1].Words = append(out[n-1].Words, shiftWords(t.Words, utf8.RuneCountInString(out[n-1].Text)+1)...)
			out[n-1].Text += " " + t.Text
			out[n-1].End = t.End
			continue
		}
		out = append(out, t)
	}
	return out
}

// ---- Soniox ----

type sonioxTimedToken struct {
	Text    string `json:"text"`
	StartMS int64  `json:"start_ms"`
	EndMS   int64  `json:"end_ms"`
	Speaker string `json:"speaker"`
}

func (c *sonioxFileClient) speakers(ctx context.Context, path string) ([]Turn, error) {
	fileID, err := c.upload(ctx, path, func(UploadProgress) {})
	if err != nil {
		return nil, fmt.Errorf("upload: %w", err)
	}
	defer c.delete(ctx, "/v1/files/"+fileID)
	body := map[string]any{
		"model":                          SonioxAsyncModel,
		"file_id":                        fileID,
		"enable_language_identification": true,
		"enable_speaker_diarization":     true,
	}
	if c.cfg.Language != "auto" && c.cfg.Language != "" {
		body["language_hints"] = []string{c.cfg.Language}
	}
	data, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sonioxAPI+"/v1/transcriptions", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	var job struct {
		ID string `json:"id"`
	}
	if err := c.do(req, &job); err != nil {
		return nil, fmt.Errorf("create transcription: %w", err)
	}
	defer c.delete(ctx, "/v1/transcriptions/"+job.ID)
	if _, err := c.waitFor(ctx, job.ID); err != nil {
		return nil, err
	}
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, sonioxAPI+"/v1/transcriptions/"+job.ID+"/transcript", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.SonioxAPIKey)
	var out struct {
		Tokens []sonioxTimedToken `json:"tokens"`
	}
	if err := getJSON(c.http, req, "Soniox", &out); err != nil {
		return nil, fmt.Errorf("fetch transcript: %w", err)
	}
	return sonioxTurns(out.Tokens), nil
}

// sonioxTurns groups tokens (which carry their own spacing) by speaker.
func sonioxTurns(tokens []sonioxTimedToken) []Turn {
	var turns []Turn
	var b strings.Builder
	runes := 0 // in b
	cur := Turn{Speaker: "\x00"}
	flush := func() {
		if cur.Speaker != "\x00" {
			cur.Text = b.String()
			turns = append(turns, cur)
		}
		b.Reset()
		runes = 0
	}
	for _, tk := range tokens {
		if strings.HasPrefix(tk.Text, "<") && strings.HasSuffix(tk.Text, ">") {
			continue // control tokens such as <end>
		}
		sp := tk.Speaker
		if sp == "" {
			sp = cur.Speaker // a token without a label stays with the speaker
		}
		if sp != cur.Speaker {
			flush()
			cur = Turn{Speaker: sp, Start: float64(tk.StartMS) / 1000}
		}
		// A token that starts a word starts with a space (or the turn);
		// the others are the rest of a word.
		body := strings.TrimLeftFunc(tk.Text, unicode.IsSpace)
		if body != "" && (runes == 0 || body != tk.Text) {
			cur.Words = append(cur.Words, WordTime{Off: runes + utf8.RuneCountInString(tk.Text) - utf8.RuneCountInString(body), At: float64(tk.StartMS) / 1000})
		}
		b.WriteString(tk.Text)
		runes += utf8.RuneCountInString(tk.Text)
		cur.End = float64(tk.EndMS) / 1000
	}
	flush()
	return turns
}

// ---- AssemblyAI ----

func (c *AsyncClient) speakers(ctx context.Context, path string) ([]Turn, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase(c.cfg)+"/v2/upload", f)
	if err != nil {
		f.Close()
		return nil, err
	}
	req.Header.Set("Authorization", c.cfg.APIKey)
	req.Header.Set("Content-Type", "application/octet-stream")
	up := &http.Client{} // an hour of audio uploads longer than the client's 60 s
	var u struct {
		UploadURL string `json:"upload_url"`
	}
	err = getJSON(up, req, "AssemblyAI", &u)
	f.Close()
	if err != nil {
		return nil, fmt.Errorf("upload: %w", err)
	}
	body := map[string]any{"audio_url": u.UploadURL, "speaker_labels": true}
	if c.cfg.Language == "auto" || c.cfg.Language == "" {
		body["language_detection"] = true
	} else {
		body["language_code"] = c.cfg.Language
	}
	data, _ := json.Marshal(body)
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, apiBase(c.cfg)+"/v2/transcript", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", c.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	var job struct {
		ID string `json:"id"`
	}
	if err := getJSON(c.http, req, "AssemblyAI", &job); err != nil {
		return nil, fmt.Errorf("create transcript: %w", err)
	}
	defer func() { // don't leave the transcript on their servers
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if r, err := http.NewRequestWithContext(dctx, http.MethodDelete, apiBase(c.cfg)+"/v2/transcript/"+job.ID, nil); err == nil {
			r.Header.Set("Authorization", c.cfg.APIKey)
			if resp, err := c.http.Do(r); err == nil {
				resp.Body.Close()
			}
		}
	}()
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-tick.C:
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase(c.cfg)+"/v2/transcript/"+job.ID, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", c.cfg.APIKey)
		var out struct {
			Status     string `json:"status"`
			Error      string `json:"error"`
			Utterances []struct {
				Speaker string `json:"speaker"`
				Start   int64  `json:"start"`
				End     int64  `json:"end"`
				Text    string `json:"text"`
				Words   []struct {
					Text  string `json:"text"`
					Start int64  `json:"start"`
				} `json:"words"`
			} `json:"utterances"`
		}
		if err := getJSON(c.http, req, "AssemblyAI", &out); err != nil {
			return nil, fmt.Errorf("poll transcript: %w", err)
		}
		switch out.Status {
		case "completed":
			turns := make([]Turn, 0, len(out.Utterances))
			for _, u := range out.Utterances {
				t := Turn{Speaker: u.Speaker, Start: float64(u.Start) / 1000, End: float64(u.End) / 1000, Text: u.Text}
				// The words make up the text in order: find each after the last.
				pos := 0 // bytes into u.Text
				for _, w := range u.Words {
					i := strings.Index(u.Text[pos:], w.Text)
					if w.Text == "" || i < 0 {
						continue
					}
					t.Words = append(t.Words, WordTime{Off: utf8.RuneCountInString(u.Text[:pos+i]), At: float64(w.Start) / 1000})
					pos += i + len(w.Text)
				}
				turns = append(turns, t)
			}
			return turns, nil
		case "error":
			if e := apierr.FromMessage("AssemblyAI", out.Error); e != nil {
				return nil, e
			}
			return nil, fmt.Errorf("transcription failed: %s", out.Error)
		}
	}
}

// getJSON runs req and decodes a large JSON answer (a transcript with every
// word of hours of audio).
func getJSON(hc *http.Client, req *http.Request, service string, out any) error {
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		if e := apierr.FromHTTP(service, resp.StatusCode, string(body)); e != nil {
			return e
		}
		return fmt.Errorf("%s: HTTP %d: %s", req.URL.Path, resp.StatusCode, truncate(string(body), 200))
	}
	return json.Unmarshal(body, out)
}
