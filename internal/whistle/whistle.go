// Package whistle runs Cactus Compute's Whistle, a 16.9 MB speech model that
// transcribes up to 30 seconds of 16 kHz mono audio per pass, on the CPU, in
// English, German, French, Spanish, Italian, Dutch and Polish.
//
// For now it drives the engine's own command-line binary (needle), one process
// per clip: that costs about 0.1 s per call and keeps the engine — a C++ library
// built against libc++ — out of Vito's own link. The Engine type is the seam: a
// linked engine can replace the process without the callers noticing.
package whistle

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// SampleRate is what the model takes: 16 kHz, mono, 16-bit.
const SampleRate = 16000

// MaxSeconds is the longest clip the model accepts in one pass.
const MaxSeconds = 30

// Languages the model knows; anything else is left for it to detect.
var Languages = map[string]bool{"en": true, "de": true, "fr": true, "es": true, "it": true, "nl": true, "pl": true}

// Engine transcribes clips with a needle binary and the whistle.cact model.
type Engine struct {
	Bin   string // needle(.exe)
	Model string // whistle.cact
}

// Result is one clip's transcript and the language the model used.
type Result struct {
	Text     string `json:"text"`
	Language string `json:"language"`
}

// Transcribe runs one clip of 16 kHz mono s16le PCM, at most MaxSeconds long.
// lang is a code from Languages or "" to detect; keywords are words and phrases
// to favour (Vito's keyterms).
func (e Engine) Transcribe(ctx context.Context, pcm []byte, lang string, keywords []string) (Result, error) {
	if len(pcm) < 2 {
		return Result{}, nil
	}
	if len(pcm) > MaxSeconds*SampleRate*2 {
		return Result{}, fmt.Errorf("whistle: clip longer than %d s", MaxSeconds)
	}
	dir, err := os.MkdirTemp("", "vito-whistle-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(dir)
	wav := filepath.Join(dir, "clip.wav")
	if err := os.WriteFile(wav, WAV(pcm), 0o600); err != nil {
		return Result{}, err
	}
	args := []string{"--model", e.Model, "--audio", wav}
	if Languages[lang] {
		args = append(args, "--audio-language", lang)
	}
	if len(keywords) > 0 {
		kw := filepath.Join(dir, "keywords.txt")
		if err := os.WriteFile(kw, []byte(strings.Join(keywords, "\n")), 0o600); err != nil {
			return Result{}, err
		}
		args = append(args, "--audio-keywords", kw)
	}
	cmd := exec.CommandContext(ctx, e.Bin, args...)
	hideWindow(cmd)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return Result{}, errors.New("whistle: " + msg)
	}
	var r Result
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &r); err != nil {
		return Result{}, fmt.Errorf("whistle: unexpected output: %.200s", out.String())
	}
	r.Text = strings.TrimSpace(r.Text)
	return r, nil
}

// WAV wraps 16 kHz mono s16le PCM in a RIFF header.
func WAV(pcm []byte) []byte {
	var b bytes.Buffer
	w := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	b.WriteString("RIFF")
	w(uint32(36 + len(pcm)))
	b.WriteString("WAVEfmt ")
	w(uint32(16))
	w(uint16(1))
	w(uint16(1))
	w(uint32(SampleRate))
	w(uint32(SampleRate * 2))
	w(uint16(2))
	w(uint16(16))
	b.WriteString("data")
	w(uint32(len(pcm)))
	b.Write(pcm)
	return b.Bytes()
}
