// Package config loads, validates and saves the single JSON settings file.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

type Server struct {
	Port  int    `json:"port"`
	Token string `json:"token"`
}

type Audio struct {
	InputDevice   string  `json:"input_device"`  // device name; empty = system default
	OutputDevice  string  `json:"output_device"` // device name; empty = system default
	InputGain     float64 `json:"input_gain"`
	SoundsEnabled bool    `json:"sounds_enabled"`
	SoundsVolume  float64 `json:"sounds_volume"` // feedback-sound volume 0..2 (1.0 = system level; >1 amplifies)
	MediaAction   string  `json:"media_action"`  // what to do with playing media while dictating: duck | pause | off
	KeepSpool     bool    `json:"keep_spool"`    // keep spool WAVs after success (debugging)
	// SilenceTimeoutSec auto-cancels a recording after this many seconds without
	// any transcribed speech, so an accidentally-triggered session can't run
	// unnoticed. 0 disables it.
	SilenceTimeoutSec int `json:"silence_timeout_sec"`
	// AutoStop finalizes and injects a dictation on its own once you stop speaking
	// for AutoStopSilenceMS, so a tap-to-start dictation needs no second keypress.
	// Off by default. A held push-to-talk key still stops on release as usual; if
	// you pause past the threshold while holding, this ends it too.
	AutoStop bool `json:"auto_stop"`
	// AutoStopSilenceMS is the pause length that counts as "done" for AutoStop.
	// 0 falls back to the default.
	AutoStopSilenceMS int `json:"auto_stop_silence_ms"`
}

type STT struct {
	Provider string `json:"provider"` // assemblyai | soniox
	APIKey   string `json:"api_key"`  // AssemblyAI
	// SonioxAPIKey is kept separate so switching provider doesn't throw the
	// other key away — you can keep both configured and flip between them.
	SonioxAPIKey    string `json:"soniox_api_key"`
	Language        string `json:"language"` // ISO code or "auto"
	Model           string `json:"model"`    // AssemblyAI speech_model; empty = server default
	KeytermsEnabled bool   `json:"keyterms_enabled"`
	EUEndpoint      bool   `json:"eu_endpoint"`
}

type Cleanup struct {
	Enabled  bool   `json:"enabled"`
	Provider string `json:"provider"` // anthropic | openai (OpenAI-compatible endpoint)
	APIKey   string `json:"api_key"`  // Anthropic
	Model    string `json:"model"`    // Anthropic model
	// OpenAI-compatible provider (provider="openai"): any endpoint that speaks the
	// OpenAI chat-completions API — Groq (a free tier that doesn't train on your data), OpenAI
	// itself, or a local model (Ollama, LM Studio, …), which keeps cleanup fully
	// on-device so the text never leaves the machine. Kept separate from the
	// Anthropic fields so switching provider throws neither key away.
	OpenAIBaseURL string `json:"openai_base_url"` // e.g. https://api.groq.com/openai/v1
	OpenAIKey     string `json:"openai_key"`
	OpenAIModel   string `json:"openai_model"`
	// OpenAIPreset is a UI-only hint (groq | openai | local) so the settings page
	// can remember "local / custom endpoint" — which isn't derivable from the base
	// URL alone. The backend ignores it and only looks at the fields above.
	OpenAIPreset string `json:"openai_preset,omitempty"`
	TimeoutMS    int    `json:"timeout_ms"`
	MinWords     int    `json:"min_words"`
}

// Assist configures the model behind Vito Assist voice commands. By default a
// command runs through the very same model as AI cleanup; turning that off lets
// you point Assist at a heavier model (Q&A and transformation are more demanding
// than tidy-up), configured with the same fields as the cleanup section.
type Assist struct {
	// UseCleanupModel, when true (the default — nil counts as true), routes Assist
	// commands through cfg.Cleanup. When false, the Cleanup config below is used.
	UseCleanupModel *bool `json:"use_cleanup_model,omitempty"`
	// Cleanup is Assist's own model config, used only when UseCleanupModel is off.
	// Reusing the type keeps the settings section identical to AI cleanup.
	Cleanup Cleanup `json:"cleanup"`
}

// UsesCleanupModel reports whether Assist should borrow the AI-cleanup model.
func (a Assist) UsesCleanupModel() bool { return a.UseCleanupModel == nil || *a.UseCleanupModel }

type Correction struct {
	Wrong string `json:"wrong"`
	Right string `json:"right"`
}

type Dictionary struct {
	Keyterms    []string     `json:"keyterms"`
	Corrections []Correction `json:"corrections"`
}

type Injection struct {
	Mode string `json:"mode"` // paste | type | clipboard_only
	// Backend picks how the keystrokes are delivered on Linux:
	// auto (default) | portal | ydotool. "portal" uses the XDG RemoteDesktop
	// portal — the sandboxed, Wayland-native route that needs no ydotoold and no
	// udev rule, and the only one that works inside a Flatpak. "ydotool" is the
	// original uinput path, kept for X11 and compositors without the portal.
	// "auto" prefers the portal when it is usable and falls back to ydotool.
	// Ignored on Windows, which has exactly one way to do this.
	Backend          string `json:"backend,omitempty"`
	RestoreClipboard bool   `json:"restore_clipboard"`
	PasteDelayMS     int    `json:"paste_delay_ms"`   // delay between copy and paste keystroke
	RestoreDelayMS   int    `json:"restore_delay_ms"` // delay before restoring previous clipboard
	AppendEnter      bool   `json:"append_enter"`     // press Enter after the text (submits chat/REPL inputs)
	AppendSpace      bool   `json:"append_space"`     // append a trailing space, so back-to-back dictations stay separated
}

type History struct {
	Enabled    bool `json:"enabled"`
	MaxEntries int  `json:"max_entries"`
	StoreAudio bool `json:"store_audio"`
	// RetentionDays auto-deletes entries older than this many days (0 = keep
	// forever). Starred favorites are never removed by it.
	RetentionDays int `json:"retention_days"`
}

type Tray struct {
	Enabled bool `json:"enabled"` // show a system-tray icon (needs a tray host)
}

type UI struct {
	Theme string `json:"theme"` // system | light | dark (follow OS by default)
	Lang  string `json:"lang"`  // "" = auto (OS language, fallback English) | en | nl
	// Desktop notifications: all (every status) | errors (only failures) | off.
	// "" is treated as "all". Routine notifications are transient (not kept in
	// the notification server's history); errors are kept.
	Notifications string `json:"notifications"`
	// WelcomeDone records that the onboarding welcome card was dismissed. It lives
	// here, in Vito's own config, rather than in the browser's localStorage on
	// purpose: an uninstall+reinstall that deletes all settings must bring the
	// welcome card back, and localStorage (keyed by the unchanged 127.0.0.1 origin)
	// would otherwise silently remember "seen" across a clean reinstall.
	WelcomeDone bool `json:"welcome_done"`
}

// Update controls the version check. It is the one thing Vito asks the outside
// world about on its own, so it can be switched off — the check is a single
// unauthenticated request to GitHub's releases API and sends nothing but the
// running version in the user agent.
type Update struct {
	// Check enables the daily version check. Nil-safe: zero value means "not set
	// yet", which Load turns into true.
	Check *bool `json:"check,omitempty"`
}

// CheckEnabled reports whether the version check should run.
func (u Update) CheckEnabled() bool { return u.Check == nil || *u.Check }

// Backup controls the automatic rolling local backups. A full backup can always
// be exported by hand from the settings page; this is the safety net that keeps
// a few recent copies on disk without being asked.
type Backup struct {
	// Auto enables the weekly rolling backup. Nil-safe: zero value means "not set
	// yet", which Load turns into true.
	Auto *bool `json:"auto,omitempty"`
}

// AutoEnabled reports whether automatic backups should run.
func (b Backup) AutoEnabled() bool { return b.Auto == nil || *b.Auto }

type Stats struct {
	// TypingSpeed picks the words-per-minute baseline for the "time saved"
	// estimate: slow | average | fast. Empty means average.
	TypingSpeed string `json:"typing_speed"`
}

// Costs holds the provider prices used to estimate spend. Rates are in USD (the
// providers publish in USD); the UI converts to Currency for display.
type Costs struct {
	Currency             string  `json:"currency"` // usd | eur (display)
	SttPerHourUSD        float64 `json:"stt_per_hour_usd"`
	CleanupInPerMTokUSD  float64 `json:"cleanup_in_per_mtok_usd"`
	CleanupOutPerMTokUSD float64 `json:"cleanup_out_per_mtok_usd"`
	// Assist token rates, used to price Vito Assist commands when Assist runs on
	// its own model (a heavier model can cost more). Authoritative in that case —
	// the settings page fills them from the chosen model, so 0 means genuinely
	// free (Groq's tier / a local model), not "unset". When Assist borrows the
	// cleanup model, the cleanup rates above are used instead.
	AssistInPerMTokUSD  float64 `json:"assist_in_per_mtok_usd,omitempty"`
	AssistOutPerMTokUSD float64 `json:"assist_out_per_mtok_usd,omitempty"`
}

type Config struct {
	Server              Server     `json:"server"`
	Audio               Audio      `json:"audio"`
	STT                 STT        `json:"stt"`
	Cleanup             Cleanup    `json:"cleanup"`
	Assist              Assist     `json:"assist"`
	Dictionary          Dictionary `json:"dictionary"`
	Injection           Injection  `json:"injection"`
	HotkeyWindows       string     `json:"hotkey_windows"`        // start/stop dictation
	HotkeyCancelWindows string     `json:"hotkey_cancel_windows"` // cancel recording (empty = none)
	// PushToTalk: holding the start/stop hotkey records only while it's held and
	// stops on release; a quick tap still toggles as before. Nil-safe — an unset
	// value counts as enabled.
	PushToTalk *bool   `json:"push_to_talk,omitempty"`
	History    History `json:"history"`
	Tray       Tray    `json:"tray"`
	UI         UI      `json:"ui"`
	Stats      Stats   `json:"stats"`
	Costs      Costs   `json:"costs"`
	Update     Update  `json:"update"`
	Backup     Backup  `json:"backup"`
	// Demo fills the UI with fabricated English sample data — statistics, costs,
	// history, dictionary and a replayed live transcript — for screenshots and
	// demos. Deliberately file-only (no setting in the UI) so it can't be turned
	// on by accident. Your real history and dictionary are left untouched: they
	// are hidden while it's on, not replaced.
	Demo bool `json:"demo"`
}

// PushToTalkEnabled reports whether the hold-to-talk behaviour is on.
func (c Config) PushToTalkEnabled() bool { return c.PushToTalk == nil || *c.PushToTalk }

// TypingWPM maps the configured typing-speed choice to a words-per-minute value
// used to estimate saved typing time.
func (c Config) TypingWPM() float64 {
	switch c.Stats.TypingSpeed {
	case "slow":
		return 25
	case "fast":
		return 65
	default: // average / empty
		return 40
	}
}

func Default() *Config {
	return &Config{
		Server: Server{Port: 4573},
		Audio:  Audio{InputGain: 1.0, SoundsEnabled: true, SoundsVolume: 1.0, MediaAction: "duck", SilenceTimeoutSec: 15, AutoStopSilenceMS: 1200},
		STT: STT{
			// Default to Soniox: its single realtime model (stt-rt-v5) covers all
			// 60 languages and is the first choice in the UI's model list — the
			// sensible starting point on a fresh install, before any keys exist.
			Provider:   "soniox",
			Language:   "nl",
			Model:      "stt-rt-v5",
			EUEndpoint: true, // only affects AssemblyAI; harmless for Soniox
		},
		Cleanup: Cleanup{
			// A fresh install defaults to Groq (an OpenAI-compatible endpoint): its
			// free tier — which states it doesn't train on API data — lets a new
			// user start cleanup for free, alongside AssemblyAI's free STT credit.
			// The Anthropic model stays filled in case they switch providers.
			Provider:      "openai",
			OpenAIBaseURL: "https://api.groq.com/openai/v1",
			OpenAIModel:   "llama-3.3-70b-versatile",
			OpenAIPreset:  "groq",
			Model:         "claude-haiku-4-5",
			// Generous on purpose: hitting the timeout means you pay for the
			// tokens and still get the raw transcript, so waiting a moment
			// longer beats giving up on a call that was nearly done.
			TimeoutMS: 5000,
			MinWords:  4,
		},
		Injection: Injection{
			Mode:             "paste",
			RestoreClipboard: true,
			PasteDelayMS:     100,
			RestoreDelayMS:   300,
		},
		HotkeyWindows: "ctrl+alt+space",
		History:       History{Enabled: true, MaxEntries: 500},
		Tray:          Tray{Enabled: true},
		UI:            UI{Theme: "system", Notifications: "all"},
		Stats:         Stats{TypingSpeed: "average"},
		Costs:         Costs{Currency: "eur", SttPerHourUSD: 0.15, CleanupInPerMTokUSD: 1.0, CleanupOutPerMTokUSD: 5.0},
	}
}

// Path returns the config file location, honouring the VITO_CONFIG override.
func Path() (string, error) {
	if p := os.Getenv("VITO_CONFIG"); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "vito", "config.json"), nil
}

// Exists reports whether the config file is already present, i.e. this is not
// the first run. Check it before Load(), which creates the file.
func Exists() bool {
	p, err := Path()
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}

// Load reads the config file, creating it with defaults (and a fresh auth
// token) on first run.
func Load() (*Config, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		cfg := Default()
		// Fresh install: show sample data so the UI has something to explain
		// itself with, until a real speech-recognition key arrives. Set here and
		// not in Default(), because Default() is also the base for parsing an
		// existing file — an upgrade whose config predates this field must not
		// suddenly hide the user's own data behind a demo.
		cfg.Demo = true
		if cfg.Server.Token, err = newToken(); err != nil {
			return nil, err
		}
		if err := cfg.Save(); err != nil {
			return nil, err
		}
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	cfg := Default()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", p, err)
	}
	if cfg.Server.Token == "" {
		if cfg.Server.Token, err = newToken(); err != nil {
			return nil, err
		}
		if err := cfg.Save(); err != nil {
			return nil, err
		}
	}
	// The cleanup timeout used to default to 2 s, which cut off calls that were
	// nearly finished — you pay for the tokens and still get the raw transcript.
	// It has never been settable in the UI, so an exact 2000 is that old default
	// rather than a deliberate choice, and is raised once to the new one.
	if cfg.Cleanup.TimeoutMS == 2000 {
		cfg.Cleanup.TimeoutMS = Default().Cleanup.TimeoutMS
		if err := cfg.Save(); err != nil {
			return nil, err
		}
	}
	// An empty speech model used to mean "let AssemblyAI decide from the language
	// code". That was never free choice: those sessions are billed as Realtime
	// Universal-3.5 Pro. Naming it makes the price visible and the setting
	// honest, without changing which model actually runs. This predates Soniox
	// (whose configs always carry stt-rt-v5), so the concrete AssemblyAI model is
	// the right fill-in — not the current default provider's model.
	if cfg.STT.Model == "" {
		cfg.STT.Model = "universal-3-5-pro"
		if err := cfg.Save(); err != nil {
			return nil, err
		}
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	return cfg, nil
}

func (c *Config) Save() error {
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	mode := os.FileMode(0o600) // holds API keys
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), mode); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(tmp, mode)
	}
	return os.Rename(tmp, p)
}

// isLangCode reports whether s is a bare two-letter lowercase language code.
func isLangCode(s string) bool {
	if len(s) != 2 {
		return false
	}
	for _, r := range s {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return true
}

func (c *Config) Validate() error {
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("server.port %d out of range", c.Server.Port)
	}
	if c.Audio.InputGain < 0.1 || c.Audio.InputGain > 10 {
		return fmt.Errorf("audio.input_gain %v out of range [0.1, 10]", c.Audio.InputGain)
	}
	if c.Audio.SoundsVolume < 0 || c.Audio.SoundsVolume > 2 {
		return fmt.Errorf("audio.sounds_volume %v out of range [0, 2]", c.Audio.SoundsVolume)
	}
	switch c.Injection.Mode {
	case "paste", "type", "clipboard_only":
	default:
		return fmt.Errorf("injection.mode %q invalid (paste|type|clipboard_only)", c.Injection.Mode)
	}
	switch c.Audio.MediaAction {
	case "", "duck", "pause", "off":
	default:
		return fmt.Errorf("audio.media_action %q invalid (duck|pause|off)", c.Audio.MediaAction)
	}
	if c.Audio.SilenceTimeoutSec < 0 || c.Audio.SilenceTimeoutSec > 600 {
		return fmt.Errorf("audio.silence_timeout_sec %d out of range [0, 600]", c.Audio.SilenceTimeoutSec)
	}
	if c.Audio.AutoStopSilenceMS != 0 && (c.Audio.AutoStopSilenceMS < 300 || c.Audio.AutoStopSilenceMS > 5000) {
		return fmt.Errorf("audio.auto_stop_silence_ms %d out of range [300, 5000]", c.Audio.AutoStopSilenceMS)
	}
	if c.History.RetentionDays < 0 || c.History.RetentionDays > 3650 {
		return fmt.Errorf("history.retention_days %d out of range [0, 3650]", c.History.RetentionDays)
	}
	switch c.UI.Theme {
	case "", "system", "light", "dark":
	default:
		return fmt.Errorf("ui.theme %q invalid (system|light|dark)", c.UI.Theme)
	}
	// "" means auto (follow the OS); otherwise a UI language code (nl, en or any
	// of the translated languages). Keep it permissive: a bare 2-letter code.
	if c.UI.Lang != "" && !isLangCode(c.UI.Lang) {
		return fmt.Errorf("ui.lang %q invalid (empty for auto, or a language code)", c.UI.Lang)
	}
	switch c.UI.Notifications {
	case "", "all", "errors", "off":
	default:
		return fmt.Errorf("ui.notifications %q invalid (all|errors|off)", c.UI.Notifications)
	}
	if c.STT.Language == "" {
		return errors.New("stt.language must be set (ISO code or \"auto\")")
	}
	switch c.Stats.TypingSpeed {
	case "", "slow", "average", "fast":
	default:
		return fmt.Errorf("stats.typing_speed %q invalid (slow|average|fast)", c.Stats.TypingSpeed)
	}
	switch c.Costs.Currency {
	case "", "usd", "eur":
	default:
		return fmt.Errorf("costs.currency %q invalid (usd|eur)", c.Costs.Currency)
	}
	return nil
}

func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
