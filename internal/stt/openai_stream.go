package stt

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"sync"

	"vito/internal/audio"
	"vito/internal/config"
)

// openaiStream is the "session" for an endpoint that cannot stream: it keeps
// the PCM as it comes in and sends the whole recording, as one WAV, when the
// dictation ends. There are no partials — the live transcript stays empty and
// the text arrives after the stop, however long the endpoint takes over it.
// Nothing is dropped on the way in, as the socket streams may do: the buffer
// here is the whole point, and a dictation is small (16 kHz mono s16le is under
// 2 MB a minute).
type openaiStream struct {
	client *openaiClient
	log    *slog.Logger

	mu   sync.Mutex
	pcm  []byte
	lang string
}

func newOpenAIStream(cfg config.STT, keyterms []string, log *slog.Logger) *openaiStream {
	return &openaiStream{client: newOpenAIClient(cfg, keyterms), log: log}
}

func (s *openaiStream) Send(pcm []byte) {
	s.mu.Lock()
	s.pcm = append(s.pcm, pcm...)
	s.mu.Unlock()
}

func (s *openaiStream) Finish(ctx context.Context) (string, error) {
	s.mu.Lock()
	pcm := s.pcm
	s.pcm = nil
	s.mu.Unlock()
	if len(pcm) == 0 {
		return "", nil
	}
	wav := audio.WAVFromPCM(pcm)
	open := func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(wav)), nil }
	seconds := float64(len(pcm)) / float64(audio.CaptureSampleRate*audio.CaptureChannels*audio.BytesPerSample)
	out, err := s.client.transcribe(ctx, open, "dictation.wav", seconds)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.lang = out.Language
	s.mu.Unlock()
	return out.Text, nil
}

func (s *openaiStream) Language() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lang
}

func (s *openaiStream) Abort() {
	s.mu.Lock()
	s.pcm = nil
	s.mu.Unlock()
}
