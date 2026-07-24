// Package stt talks to AssemblyAI: the v3 streaming WebSocket for the live
// path and the v2 async REST API as spool-file fallback.
package stt

import (
	"context"

	"vito/internal/config"
)

// Transcriber is the async (file-based) transcription interface; keeping it
// small makes a future local-Whisper backend a drop-in.
type Transcriber interface {
	TranscribeFile(ctx context.Context, path string) (string, error)
}

func streamURL(cfg config.STT) string {
	if cfg.EUEndpoint {
		return "wss://streaming.eu.assemblyai.com/v3/ws"
	}
	return "wss://streaming.assemblyai.com/v3/ws"
}

func apiBase(cfg config.STT) string {
	if cfg.EUEndpoint {
		return "https://api.eu.assemblyai.com"
	}
	return "https://api.assemblyai.com"
}
