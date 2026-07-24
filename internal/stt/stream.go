package stt

import (
	"context"
	"log/slog"

	"vito/internal/config"
)

// Streamer is one live transcription session. The daemon feeds it PCM while
// recording, then either asks for the transcript or throws the session away.
//
// Implementations must be safe to call from several goroutines and must never
// block the audio path: a failed session drops its audio, because the spool
// file on disk is the real safety net.
type Streamer interface {
	// Send buffers a PCM chunk. Never blocks.
	Send(pcm []byte)
	// Finish flushes what is left, ends the session and returns the transcript.
	Finish(ctx context.Context) (string, error)
	// Language returns the recognised language (ISO code) if the provider
	// reported one during the session, else "" (fall back to the configured one).
	Language() string
	// Abort tears the session down without waiting for a result.
	Abort()
}

// NewStream opens a session with the configured provider. Anything unknown
// falls back to AssemblyAI, which is the setting every existing install has.
func NewStream(cfg config.STT, keyterms []string, log *slog.Logger, onPartial func(string)) Streamer {
	switch cfg.Provider {
	case "soniox":
		return newSonioxStream(cfg, keyterms, log, onPartial)
	default:
		return newAssemblyAIStream(cfg, keyterms, log, onPartial)
	}
}
