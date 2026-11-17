package stt

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"time"

	"vito/internal/audio"
	"vito/internal/config"
	"vito/internal/whistle"
)

// An endpoint on this computer (Vito local's Parakeet, a whisper-server of
// one's own) transcribes while you speak, as Whistle does: the recording is
// cut at pauses into pieces of at least localSegMin, each finished piece is
// sent as it ends, and only the last one is left once you stop — about a
// second on a plain CPU, however long the dictation. The open piece is sent
// again now and then for the live text. A remote endpoint is left alone: it
// bills per request (Groq at least 10 s each), and one request is cheaper.
const (
	localSegMin = 6 * time.Second
	// A longer pause (0.7 s, 1 s) cut less often but no better, and left
	// a longer last piece: 2–3 s after the stop instead of 0.3 (measured).
	localPause     = 450 * time.Millisecond
	localLookEvery = 1500 * time.Millisecond
)

// localSegmented reports whether cfg is an endpoint on this computer that
// gets the recording piece by piece: not when the daemon asked for it whole.
func localSegmented(cfg config.STT) bool {
	if cfg.Provider != "openai" || cfg.Whole {
		return false
	}
	u, err := url.Parse(strings.TrimSpace(cfg.OpenAIBaseURL))
	if err != nil {
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func newLocalStream(cfg config.STT, log *slog.Logger, onPartial func(string)) *whistleStream {
	c := newOpenAIClient(cfg)
	run := func(ctx context.Context, pcm []byte) (whistle.Result, error) {
		wav := audio.WAVFromPCM(pcm)
		open := func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(wav)), nil }
		seconds := float64(len(pcm)) / float64(audio.CaptureSampleRate*audio.CaptureChannels*audio.BytesPerSample)
		out, err := c.transcribe(ctx, open, "dictation.wav", seconds)
		return whistle.Result{Text: strings.TrimSpace(out.Text), Language: out.Language}, err
	}
	s := &whistleStream{run: run, whole: run, log: log, onPartial: onPartial,
		seg: whistle.NewSegmenterFor(localSegMin, localPause), lookEvery: localLookEvery}
	s.start()
	return s
}
