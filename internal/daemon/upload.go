package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"vito/internal/apierr"
	"vito/internal/cleanup"
	"vito/internal/config"
	"vito/internal/dictionary"
	"vito/internal/history"
	"vito/internal/inject"
	"vito/internal/stt"
)

// Transcribing an audio file the user hands to Vito, rather than something it
// recorded itself.
//
// This deliberately does not go through the dictation state machine. Dictation
// exists to put text where your cursor is; an upload is started from the web UI,
// where the cursor is in the browser — injecting there would type the transcript
// into the page. So the result goes to the transcript panel, the clipboard and
// the history, and you decide where it lands.

// uploadCleanupMaxWords caps which transcripts get the AI cleanup pass. It is
// tuned for dictations of a few sentences; running it over an hour-long
// recording would be a second, much larger bill that the estimate shown before
// the upload never mentioned.
const uploadCleanupMaxWords = 1200

// UploadStatus is broadcast while a file is being transcribed.
type UploadStatus struct {
	Phase  string  `json:"phase"`          // upload | transcribe | done | error
	Frac   float64 `json:"frac"`           // 0..1 while uploading, -1 when unknown
	Name   string  `json:"name,omitempty"` // the file being worked on
	Text   string  `json:"text,omitempty"`
	Error  string  `json:"error,omitempty"`
	Credit string  `json:"credit,omitempty"` // provider out of credit, if that's why it failed
}

// UploadResult is what the HTTP caller gets back once the file is done.
type UploadResult struct {
	ID          string `json:"id"`
	Raw         string `json:"raw"`
	Cleaned     string `json:"cleaned,omitempty"`
	CleanupUsed bool   `json:"cleanup_used"`
	Language    string `json:"language,omitempty"`
	Words       int    `json:"words"`
	DurationMS  int64  `json:"duration_ms"`
}

// TranscribeUpload transcribes path with the configured provider. durationMS is
// what the browser measured for the file; it is only used for the statistics and
// the cost, so a zero is survivable.
func (d *Daemon) TranscribeUpload(ctx context.Context, path, name string, durationMS int64) (UploadResult, error) {
	cfg := d.Config()
	if err := uploadReady(cfg.STT); err != nil {
		d.emitUpload(UploadStatus{Phase: "error", Name: name, Error: err.Error()})
		return UploadResult{}, err
	}
	sttCfg, err := d.resolveSTT(cfg.STT)
	if err != nil {
		d.emitUpload(UploadStatus{Phase: "error", Name: name, Error: err.Error()})
		return UploadResult{}, err
	}
	// The managed engine decodes nothing but WAV, and Vito has no decoder of
	// its own to hand it anything else. Said up front, before the upload.
	if cfg.STT.Provider == "local" && !strings.EqualFold(filepath.Ext(name), ".wav") {
		err := fmt.Errorf("de lokale spraakherkenning accepteert alleen WAV-bestanden")
		d.emitUpload(UploadStatus{Phase: "error", Name: name, Error: err.Error()})
		return UploadResult{}, err
	}

	var keyterms []string
	if cfg.STT.KeytermsEnabled {
		keyterms = dictionary.Keyterms(cfg.Dictionary)
	}

	started := time.Now()
	d.emitUpload(UploadStatus{Phase: "upload", Frac: 0, Name: name})
	out, err := stt.TranscribeUpload(ctx, sttCfg, keyterms, path, func(p stt.UploadProgress) {
		d.emitUpload(UploadStatus{Phase: string(p.Phase), Frac: p.Frac, Name: name})
	})
	if err != nil {
		provider, isCredit := apierr.CreditProvider(err)
		if isCredit {
			d.markCredit(provider, true)
		}
		d.emitUpload(UploadStatus{Phase: "error", Name: name, Error: err.Error(), Credit: provider})
		return UploadResult{}, err
	}
	d.markCredit(sttProviderName(sttCfg), false) // succeeded → not out of credit
	sttMS := time.Since(started).Milliseconds()
	lang := out.Language
	// The provider measured the audio itself; the browser's figure is only a
	// fallback, since a background tab won't read media metadata.
	if out.DurationMS > 0 {
		durationMS = out.DurationMS
	}

	raw := dictionary.Apply(strings.TrimSpace(out.Text), cfg.Dictionary.Corrections)
	if raw == "" {
		err := fmt.Errorf("geen spraak herkend in dit bestand")
		d.emitUpload(UploadStatus{Phase: "error", Name: name, Error: err.Error()})
		return UploadResult{}, err
	}
	if lang == "" {
		lang = cfg.STT.Language
	}

	cleaned, cleanupUsed := "", false
	cleanupErr := ""
	var usage cleanup.Usage
	if cfg.Cleanup.Enabled && cleanup.Configured(cfg.Cleanup) && wordCount(raw) <= uploadCleanupMaxWords {
		// A longer window than a dictation gets: there is no cursor waiting on it.
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		out, u, err := cleanup.NewCleaner(cfg.Cleanup).Clean(cctx, raw, cfg.STT.Language, cfg.Dictionary.Corrections, "")
		cancel()
		usage = u
		if err != nil {
			d.log.Warn("cleanup of uploaded file failed, keeping the raw transcript", "err", err)
			cleanupErr = err.Error()
			if p, isCredit := apierr.CreditProvider(err); isCredit {
				d.markCredit(p, true)
			}
		} else {
			cleaned, cleanupUsed = out, true
			d.markCredit(cleanup.ProviderName(cfg.Cleanup), false)
		}
	}

	final := raw
	if cleanupUsed {
		final = cleaned
	}
	// Clipboard only — see the note at the top of this file.
	if _, err := inject.Inject(config.Injection{Mode: "clipboard_only"}, final); err != nil {
		d.log.Warn("could not put the transcript on the clipboard", "err", err)
	}

	entry := history.Entry{
		ID:               history.NewID(),
		Timestamp:        time.Now(),
		DurationMS:       durationMS,
		Language:         lang,
		Source:           history.SourceUpload,
		Raw:              raw,
		Cleaned:          cleaned,
		CleanupUsed:      cleanupUsed,
		CleanupError:     cleanupErr,
		SttMS:            sttMS,
		CleanupInTokens:  usage.InputTokens,
		CleanupOutTokens: usage.OutputTokens,
	}
	if cfg.History.Enabled && d.hist != nil && !d.privacyActive() {
		if err := d.hist.AppendUpload(entry); err != nil {
			d.log.Warn("history append failed", "err", err)
		}
	}

	d.emitUpload(UploadStatus{Phase: "done", Frac: 1, Name: name, Text: final})
	d.note("vito: bestand getranscribeerd", preview(final))
	d.log.Info("file transcribed",
		"name", name, "provider", cfg.STT.Provider, "audio_ms", durationMS,
		"took", time.Since(started).Round(time.Second), "chars", len(final), "cleanup", cleanupUsed)

	return UploadResult{
		ID: entry.ID, Raw: raw, Cleaned: cleaned, CleanupUsed: cleanupUsed,
		Language: lang, Words: len(strings.Fields(final)), DurationMS: durationMS,
	}, nil
}

// uploadReady reports whether the configured provider can be used at all.
func uploadReady(cfg config.STT) error {
	switch cfg.Provider {
	case "soniox":
		if strings.TrimSpace(cfg.SonioxAPIKey) == "" {
			return fmt.Errorf("geen Soniox API-key ingesteld")
		}
		return nil
	case "openai":
		if !stt.OpenAIConfigured(cfg) {
			return fmt.Errorf("geen spraak-endpoint ingesteld")
		}
		return nil
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return fmt.Errorf("geen AssemblyAI API-key ingesteld")
	}
	return nil
}

func (d *Daemon) emitUpload(st UploadStatus) { d.emit(Event{Type: "upload", Upload: &st}) }
