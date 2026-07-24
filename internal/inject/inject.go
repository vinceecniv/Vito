// Package inject delivers the final text at the cursor.
package inject

import (
	"fmt"

	"vito/internal/config"
)

type Mode string

const (
	ModePaste         Mode = "paste"
	ModeType          Mode = "type"
	ModeClipboardOnly Mode = "clipboard_only"
)

// SelectMode maps the configured injection mode, falling back to the
// always-safe clipboard_only for anything unrecognised.
func SelectMode(cfg config.Injection) Mode {
	switch Mode(cfg.Mode) {
	case ModePaste, ModeType, ModeClipboardOnly:
		return Mode(cfg.Mode)
	default:
		return ModeClipboardOnly
	}
}

// Inject puts text at the cursor per the configured mode. It returns the mode
// actually used so callers can word the notification.
func Inject(cfg config.Injection, text string) (Mode, error) {
	if text == "" {
		return "", fmt.Errorf("nothing to inject")
	}
	// A trailing space keeps back-to-back dictations separated. Added only to the
	// delivered text, not to what's stored/previewed.
	if cfg.AppendSpace {
		text += " "
	}
	mode := SelectMode(cfg)
	if err := injectPlatform(cfg, mode, text); err != nil {
		return mode, err
	}
	// Optionally submit the text with a trailing Enter (handy for chat/REPL
	// inputs like a Claude Code session). Best-effort: the text is already
	// delivered, so a failed Enter must not trigger a re-inject. Skipped for
	// clipboard-only, where nothing is typed into a field.
	if cfg.AppendEnter && mode != ModeClipboardOnly {
		_ = pressEnter()
	}
	return mode, nil
}
