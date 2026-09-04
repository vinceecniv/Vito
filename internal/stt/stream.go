package stt

import (
	"context"
	"log/slog"
	"time"

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
	case "openai":
		return newOpenAIStream(cfg, keyterms, log)
	default:
		return newAssemblyAIStream(cfg, keyterms, log, onPartial)
	}
}

// HasPartials reports whether the provider's session produces text while the
// recording is still running. The daemon reads speech activity off the partials
// — for its silence watchdog and auto-stop — and needs another signal when
// there are none.
func HasPartials(cfg config.STT) bool {
	return cfg.Provider != "openai"
}

// FinishTimeout is how long the daemon waits for Finish. A socket session has
// already heard everything and only needs to close, so a few seconds is plenty
// and anything longer just delays the fallback. An endpoint that gets the
// recording whole at the end has all of its work still ahead of it — a local
// model on a plain CPU included — so it gets the same window the file path has.
func FinishTimeout(cfg config.STT) time.Duration {
	if !HasPartials(cfg) {
		return 120 * time.Second
	}
	return 8 * time.Second
}

// Fallback is the file-based client that transcribes the spool when the live
// session failed — the provider's own, so a dropped Soniox stream is retried
// at Soniox rather than at AssemblyAI with a key that may not exist. Nil when
// there is nothing to retry with: an endpoint session already *was* one
// request with the whole file, and sending it again would only fail the same way.
func Fallback(cfg config.STT, keyterms []string) Transcriber {
	switch cfg.Provider {
	case "openai":
		return nil
	case "soniox":
		return newSonioxFileClient(cfg, keyterms)
	default:
		return NewAsyncClient(cfg, keyterms)
	}
}
