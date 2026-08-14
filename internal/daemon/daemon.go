// Package daemon owns the dictation state machine:
//
//	idle → recording → processing → idle
//
// Start opens the mic, spool and STT socket in parallel; Stop finishes the
// stream (or falls back to async transcription of the spool), optionally runs
// the Haiku cleanup pass, and injects the result; Cancel discards everything.
package daemon

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"vito/assets"
	"vito/internal/apierr"
	"vito/internal/audio"
	"vito/internal/cleanup"
	"vito/internal/config"
	"vito/internal/dictionary"
	"vito/internal/history"
	"vito/internal/inject"
	"vito/internal/media"
	"vito/internal/notify"
	"vito/internal/stt"
)

type State string

const (
	StateIdle       State = "idle"
	StateRecording  State = "recording"
	StateProcessing State = "processing"
)

type Timings struct {
	Recording time.Duration `json:"recording_ms"` // start-speaking → stop-speaking
	SttFinal  time.Duration `json:"stt_final_ms"` // stop → transcript
	Cleanup   time.Duration `json:"cleanup_ms"`
	Injected  time.Duration `json:"injected_ms"` // stop → pasted
}

type Status struct {
	State       State   `json:"state"`
	LastError   string  `json:"last_error,omitempty"`
	LastPreview string  `json:"last_preview,omitempty"`
	LastTimings Timings `json:"last_timings"`
	// LastRecordingID is the history id of the most recent dictation whose audio
	// was kept (history.store_audio), so the UI can offer it as a download.
	LastRecordingID string `json:"last_recording_id,omitempty"`
	// Credit lists providers (display names) currently known to be out of credit,
	// each cleared the next time that provider is used successfully.
	Credit []string `json:"credit,omitempty"`
	// Command is the armed one-off spoken command, or "" when none is pending.
	Command string `json:"command,omitempty"`
}

// Event is broadcast to the web UI over WebSocket via the OnEvent callback.
type Event struct {
	Type    string `json:"type"` // state | partial | final | level | error
	State   State  `json:"state,omitempty"`
	Text    string `json:"text,omitempty"`
	Raw     string `json:"raw,omitempty"`
	Cleaned string `json:"cleaned,omitempty"`
	Level   int    `json:"level,omitempty"` // peak 0..100
	Clip    bool   `json:"clip,omitempty"`
	Timings any    `json:"timings,omitempty"`
	Error   string `json:"error,omitempty"`
	// Command is the armed one-off spoken command ("vertaal naar Duits"), or ""
	// when it's cleared. Drives the command-mode indicator in the UI.
	Command string `json:"command,omitempty"`
	// CommandReceived marks a command whose input is already in (a clipboard
	// command), so the indicator skips the "speak your text" waiting state.
	CommandReceived bool `json:"command_received,omitempty"`
	// RecordingID names the kept audio of this dictation, when there is any.
	RecordingID string `json:"recording_id,omitempty"`
	// Playback carries the play head of a recording being played back.
	Playback *audio.PlaybackState `json:"playback,omitempty"`
	// Upload reports progress while an uploaded audio file is transcribed.
	Upload *UploadStatus `json:"upload,omitempty"`
	// CleanupFailed marks a final where the AI cleanup pass was attempted but
	// errored/timed out, so the raw transcript was injected instead. The web UI
	// surfaces this as a toast.
	CleanupFailed bool `json:"cleanup_failed,omitempty"`
	// Credit names the provider whose depleted balance caused a failure, when
	// that is the reason (rather than a transient error). Set on an "error"
	// event; CleanupCredit is its equivalent on a "final" where only the cleanup
	// pass was skipped for lack of credit.
	Credit        string `json:"credit,omitempty"`
	CleanupCredit string `json:"cleanup_credit,omitempty"`
}

type session struct {
	cfg       config.Config // snapshot for the whole session
	spool     *audio.Spool
	capture   *audio.Capture
	stream    stt.Streamer
	chunks    chan []byte
	consumers sync.WaitGroup
	started   time.Time
	media     *media.Session // ducked/paused media to restore when recording ends
	// lastActivity is the UnixMilli of the last transcribed speech; the silence
	// watchdog uses it to auto-cancel a session that runs on without any speech.
	lastActivity atomic.Int64
	// spoke is set once the session has produced any real transcript, so auto-stop
	// only arms after speech has actually begun (before that a silent session is a
	// silence-cancel, not an auto-stop).
	spoke atomic.Bool
}

type Daemon struct {
	log      *slog.Logger
	audioCtx *audio.Context
	hist     *history.Store

	// OnEvent, when set, receives live events for the web UI. Must not block.
	OnEvent func(Event)

	listenerMu sync.Mutex
	listeners  []func(Event) // additional event sinks (e.g. the tray)

	privacyMu     sync.Mutex
	privacyOn     bool
	privacyExpiry time.Time // zero = no expiry ("until off")

	mu         sync.Mutex
	cfg        *config.Config
	state      State
	sess       *session
	status     Status
	micTesting bool

	mediaMu  sync.Mutex
	curMedia *media.Session // active media suppression, tracked for shutdown restore

	player      *audio.Player // plays kept recordings back on the configured device
	playMu      sync.Mutex
	playTicking bool

	creditMu  sync.Mutex
	creditOut map[string]bool // provider display name → known out of credit

	cmdMu      sync.Mutex
	pendingCmd string // one-off spoken command ("Vito, …") armed for the next dictation
}

// sttProviderName is the display name of the configured speech-recognition
// provider, used to name it in the "out of credit" UI.
func sttProviderName(cfg config.STT) string {
	if cfg.Provider == "soniox" {
		return "Soniox"
	}
	return "AssemblyAI"
}

// flagCredit marks a provider as out of credit; clearCredit clears it (on the
// next successful use). Both report whether the state actually changed, so the
// caller can broadcast only real transitions.
func (d *Daemon) flagCredit(provider string) bool {
	d.creditMu.Lock()
	defer d.creditMu.Unlock()
	if d.creditOut == nil {
		d.creditOut = map[string]bool{}
	}
	if d.creditOut[provider] {
		return false
	}
	d.creditOut[provider] = true
	return true
}

func (d *Daemon) clearCredit(provider string) bool {
	d.creditMu.Lock()
	defer d.creditMu.Unlock()
	if !d.creditOut[provider] {
		return false
	}
	delete(d.creditOut, provider)
	return true
}

// CreditOut returns the providers currently known to be out of credit, sorted.
func (d *Daemon) CreditOut() []string {
	d.creditMu.Lock()
	defer d.creditMu.Unlock()
	out := make([]string, 0, len(d.creditOut))
	for p := range d.creditOut {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// markCredit sets or clears a provider's out-of-credit flag and, on a real
// change, broadcasts a "credit" event so the web UI's card appears or clears
// without waiting for the next poll.
func (d *Daemon) markCredit(provider string, out bool) {
	changed := false
	if out {
		changed = d.flagCredit(provider)
	} else {
		changed = d.clearCredit(provider)
	}
	if changed {
		d.emit(Event{Type: "credit"})
	}
}

// trackMedia records the active media suppression so Shutdown can restore it
// even after the session state has moved on.
func (d *Daemon) trackMedia(m *media.Session) {
	d.mediaMu.Lock()
	d.curMedia = m
	d.mediaMu.Unlock()
}

// restoreMedia undoes the tracked media suppression (duck/pause) and clears it.
// Safe to call repeatedly and when nothing is active (Restore is nil-safe).
func (d *Daemon) restoreMedia() {
	d.mediaMu.Lock()
	m := d.curMedia
	d.curMedia = nil
	d.mediaMu.Unlock()
	m.Restore()
}

// Shutdown restores any ducked/paused media and cancels an in-flight recording,
// so the daemon never exits leaving system audio suppressed. Call before exit.
func (d *Daemon) Shutdown() {
	_ = d.Cancel()   // restores + tears down when recording
	d.restoreMedia() // catch the brief stop→finish window, or any leftover
}

// AddEventListener registers an extra sink for live events, alongside OnEvent.
// Listeners must not block. Used by the system tray to reflect daemon state.
func (d *Daemon) AddEventListener(fn func(Event)) {
	d.listenerMu.Lock()
	d.listeners = append(d.listeners, fn)
	d.listenerMu.Unlock()
}

// SetPrivacy turns privacy mode on or off. When on, dictations are not written
// to history. A positive dur auto-expires it; dur <= 0 means "until off".
func (d *Daemon) SetPrivacy(on bool, dur time.Duration) {
	d.privacyMu.Lock()
	d.privacyOn = on
	if on && dur > 0 {
		d.privacyExpiry = time.Now().Add(dur)
	} else {
		d.privacyExpiry = time.Time{}
	}
	d.privacyMu.Unlock()
	d.log.Info("privacy mode", "on", on, "expiry", dur)
	d.emit(Event{Type: "privacy"})
}

// PrivacyStatus reports whether privacy mode is active and until when (zero time
// = no expiry). Expired timers are cleared here.
func (d *Daemon) PrivacyStatus() (on bool, until time.Time) {
	d.privacyMu.Lock()
	defer d.privacyMu.Unlock()
	if d.privacyOn && !d.privacyExpiry.IsZero() && time.Now().After(d.privacyExpiry) {
		d.privacyOn = false
		d.privacyExpiry = time.Time{}
	}
	return d.privacyOn, d.privacyExpiry
}

func (d *Daemon) privacyActive() bool { on, _ := d.PrivacyStatus(); return on }

func New(cfg *config.Config, log *slog.Logger, audioCtx *audio.Context, hist *history.Store) *Daemon {
	d := &Daemon{cfg: cfg, log: log, audioCtx: audioCtx, hist: hist, state: StateIdle}
	d.player = audio.NewPlayer(func(string) { d.stopPlayback() })
	go d.retentionLoop()
	return d
}

// retentionLoop enforces the age-based history auto-delete: once at startup, then
// hourly. Config changes trigger their own immediate prune via UpdateConfig.
func (d *Daemon) retentionLoop() {
	d.pruneHistory()
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for range t.C {
		d.pruneHistory()
	}
}

// pruneHistory deletes non-favorite entries older than the configured retention
// window and removes their kept recordings. A no-op when retention is off.
func (d *Daemon) pruneHistory() {
	if d.hist == nil {
		return
	}
	d.mu.Lock()
	days := d.cfg.History.RetentionDays
	d.mu.Unlock()
	ids, err := d.hist.PruneOlderThan(days)
	if err != nil {
		d.log.Warn("history retention prune failed", "err", err)
		return
	}
	for _, id := range ids {
		audio.RemoveRecording(id)
	}
	if len(ids) > 0 {
		d.log.Info("auto-deleted old history entries", "count", len(ids), "older_than_days", days)
	}
}

// ---- playing back a kept recording ----------------------------------------
//
// The daemon plays, not the browser: it is the only side that knows which
// output device the user configured. Position updates ride the existing event
// stream, so the UI's progress bar follows without polling.

// PlayRecording starts (or resumes, or pauses) a kept recording. Toggling the
// one that is already loaded pauses and resumes it; any other id replaces it.
func (d *Daemon) PlayRecording(id string) error {
	if st := d.player.State(); st.ID == id {
		if st.Playing {
			d.player.Pause()
		} else {
			d.player.Resume()
		}
		d.emitPlayback()
		return nil
	}
	path, ok := audio.RecordingPath(id)
	if !ok {
		return fmt.Errorf("recording not found")
	}
	if err := d.player.Play(d.audioCtx, d.Config().Audio.OutputDevice, id, path); err != nil {
		return err
	}
	d.startPlaybackTicker()
	d.emitPlayback()
	return nil
}

func (d *Daemon) SeekPlayback(sec float64) {
	d.player.Seek(sec)
	d.emitPlayback()
}

// StopPlayback ends playback from the outside (the UI, or a starting dictation:
// the microphone should never record what we are playing back).
func (d *Daemon) StopPlayback() { d.stopPlayback() }

func (d *Daemon) stopPlayback() {
	if d.player.State().ID == "" {
		return
	}
	d.player.Stop()
	d.emitPlayback()
}

func (d *Daemon) PlaybackState() audio.PlaybackState { return d.player.State() }

func (d *Daemon) emitPlayback() {
	st := d.player.State()
	d.emit(Event{Type: "playback", Playback: &st})
}

// startPlaybackTicker keeps one goroutine alive for as long as something is
// loaded, pushing the play head to the UI a few times a second.
func (d *Daemon) startPlaybackTicker() {
	d.playMu.Lock()
	defer d.playMu.Unlock()
	if d.playTicking {
		return
	}
	d.playTicking = true
	go func() {
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		for range t.C {
			st := d.player.State()
			if st.ID == "" {
				d.playMu.Lock()
				d.playTicking = false
				d.playMu.Unlock()
				return
			}
			if st.Playing {
				d.emit(Event{Type: "playback", Playback: &st})
			}
		}
	}()
}

// note shows a routine status notification, honoring ui.notifications: it
// appears only at "all" (or the unset default) and is transient — shown briefly
// but not kept in the notification server's history.
func (d *Daemon) note(summary, body string) {
	switch d.Config().UI.Notifications {
	case "", "all":
		notify.Send(summary, body)
	}
}

// noteErr shows an error notification (kept in history) unless notifications
// are switched fully off.
func (d *Daemon) noteErr(summary, body string) {
	if d.Config().UI.Notifications != "off" {
		notify.SendSticky(summary, body)
	}
}

// Config returns a copy of the current configuration.
func (d *Daemon) Config() config.Config {
	d.mu.Lock()
	defer d.mu.Unlock()
	return *d.cfg
}

// UpdateConfig applies a new configuration; the next dictation uses it.
// Server port/token changes still require a restart. A "config" event is
// emitted so live consumers (the tray) can refresh their state.
func (d *Daemon) UpdateConfig(cfg *config.Config) {
	d.mu.Lock()
	d.cfg = cfg
	d.mu.Unlock()
	if d.hist != nil {
		d.hist.SetRetentionDays(cfg.History.RetentionDays)
		go d.pruneHistory() // apply a shortened retention window right away
	}
	d.log.Info("configuration updated")
	d.emit(Event{Type: "config"})
}

// SetConfig validates, persists and applies cfg. Used by the tray for quick
// setting changes; the web UI goes through the server's PUT /api/config.
func (d *Daemon) SetConfig(cfg config.Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	d.UpdateConfig(&cfg)
	return nil
}

func (d *Daemon) Status() Status {
	d.mu.Lock()
	defer d.mu.Unlock()
	st := d.status
	st.State = d.state
	return st
}

func (d *Daemon) emit(e Event) {
	if d.OnEvent != nil {
		d.OnEvent(e)
	}
	d.listenerMu.Lock()
	listeners := d.listeners
	d.listenerMu.Unlock()
	for _, fn := range listeners {
		fn(e)
	}
}

func (d *Daemon) setState(s State) {
	d.mu.Lock()
	d.state = s
	d.mu.Unlock()
	d.emit(Event{Type: "state", State: s})
}

// Toggle starts or stops a dictation depending on current state.
func (d *Daemon) Toggle() (State, error) { return d.toggle() }

func (d *Daemon) toggle() (State, error) {
	d.mu.Lock()
	state := d.state
	d.mu.Unlock()
	switch state {
	case StateIdle:
		return StateRecording, d.start()
	case StateRecording:
		return StateProcessing, d.Stop()
	default:
		return state, fmt.Errorf("busy processing previous dictation")
	}
}

// RequestPip asks any connected web UI to toggle the always-on-top live
// transcript window (Document Picture-in-Picture). Used by the tray.
func (d *Daemon) RequestPip() { d.emit(Event{Type: "pip"}) }

func (d *Daemon) Start() error { return d.start() }

func (d *Daemon) start() error {
	// A fresh dictation that isn't carrying an armed command clears the leftover
	// command indicator (kept visible through the command's own dictation above).
	d.cmdMu.Lock()
	pending := d.pendingCmd
	d.cmdMu.Unlock()
	if pending == "" {
		d.clearCommandDisplay()
	}
	d.mu.Lock()
	if d.state != StateIdle {
		d.mu.Unlock()
		return fmt.Errorf("cannot start while %s", d.state)
	}
	if d.micTesting {
		d.mu.Unlock()
		return fmt.Errorf("microfoontest bezig")
	}
	d.state = StateRecording
	d.status.LastError = ""
	cfg := *d.cfg // session snapshot: settings changes apply per-dictation
	d.mu.Unlock()
	d.emit(Event{Type: "state", State: StateRecording})
	d.stopPlayback() // never let the microphone record a recording we're playing

	spool, err := audio.NewSpool()
	if err != nil {
		d.setIdleWithError(fmt.Errorf("create spool: %w", err))
		return err
	}

	s := &session{
		cfg:     cfg,
		spool:   spool,
		started: time.Now(),
		chunks:  make(chan []byte, 256),
	}

	// Duck (or pause) any playing music/video for the duration of the
	// recording. This runs in the background (see media.Start) so it adds no
	// start latency; Restore happens the moment recording stops (finish/Cancel).
	s.media = media.Start(cfg.Audio.MediaAction, d.log)
	d.trackMedia(s.media) // so shutdown can restore it regardless of session state

	var keyterms []string
	if cfg.STT.KeytermsEnabled {
		keyterms = dictionary.Keyterms(cfg.Dictionary)
	}
	// Prewarm: the STT socket dials while the first chunks are buffered.
	s.lastActivity.Store(time.Now().UnixMilli())
	var lastPartial string // reset the silence timer only on genuinely new text
	s.stream = stt.NewStream(cfg.STT, keyterms, d.log, func(partial string) {
		if p := strings.TrimSpace(partial); p != "" && p != lastPartial {
			lastPartial = p
			s.lastActivity.Store(time.Now().UnixMilli())
			s.spoke.Store(true)
		}
		d.emit(Event{Type: "partial", Text: partial})
	})

	s.consumers.Add(1)
	go func() {
		defer s.consumers.Done()
		lastLevel := time.Time{}
		var meter meterScale // scaled to the last seconds of speech, per session
		for chunk := range s.chunks {
			if err := s.spool.Write(chunk); err != nil {
				d.log.Error("spool write failed", "err", err)
			}
			s.stream.Send(chunk)
			if now := time.Now(); now.Sub(lastLevel) >= 100*time.Millisecond {
				lastLevel = now
				r, clip := peakRatio(chunk)
				d.emit(Event{Type: "level", Level: meter.level(r, now), Clip: clip})
			}
		}
	}()

	capture, err := audio.StartCapture(d.audioCtx, cfg.Audio.InputDevice, cfg.Audio.InputGain, func(chunk []byte) {
		select {
		case s.chunks <- chunk:
		default: // consumer stalled; drop rather than block the audio thread
		}
	})
	if err != nil {
		s.stream.Abort()
		close(s.chunks)
		s.consumers.Wait()
		_ = spool.Close()
		spool.Remove()
		d.restoreMedia() // recording never started; undo the media action
		d.setIdleWithError(fmt.Errorf("open microphone: %w", err))
		d.noteErr("vito: microfoon-fout", err.Error())
		return err
	}
	s.capture = capture

	d.mu.Lock()
	d.sess = s
	d.mu.Unlock()

	if to := time.Duration(cfg.Audio.SilenceTimeoutSec) * time.Second; to > 0 {
		go d.watchSilence(s, to)
	}
	if cfg.Audio.AutoStop {
		pause := time.Duration(cfg.Audio.AutoStopSilenceMS) * time.Millisecond
		if pause <= 0 {
			pause = 1200 * time.Millisecond
		}
		go d.watchAutoStop(s, pause)
	}
	d.playSound(cfg, assets.SoundStart)
	d.log.Info("recording started", "spool", spool.Path(), "cleanup", d.cleanupEffective(s))
	return nil
}

// Stop ends capture and finishes the pipeline in the background so the
// hotkey CLI returns immediately.
func (d *Daemon) Stop() error {
	d.mu.Lock()
	if d.state != StateRecording || d.sess == nil {
		d.mu.Unlock()
		return fmt.Errorf("not recording")
	}
	s := d.sess
	d.sess = nil
	d.state = StateProcessing
	d.mu.Unlock()
	d.emit(Event{Type: "state", State: StateProcessing})
	// Play the "done" chime the instant recording stops (on the stop hotkey), not
	// after the STT/cleanup pipeline finishes — that's the snappier feedback.
	d.playSound(s.cfg, assets.SoundDone)

	go d.finish(s)
	return nil
}

func (d *Daemon) Cancel() error {
	d.mu.Lock()
	if d.sess == nil {
		d.mu.Unlock()
		return fmt.Errorf("not recording")
	}
	s := d.sess
	d.sess = nil
	d.state = StateIdle
	d.mu.Unlock()

	d.discard(s)
	d.playSound(s.cfg, assets.SoundCancel)
	d.note("vito: geannuleerd", "")
	d.log.Info("recording cancelled")
	return nil
}

// discard tears down a recording session and removes its spool, without
// injecting anything. Shared by Cancel and the silence watchdog.
func (d *Daemon) discard(s *session) {
	if s.capture != nil {
		s.capture.Stop()
	}
	d.restoreMedia()
	close(s.chunks)
	s.consumers.Wait()
	s.stream.Abort()
	_ = s.spool.Close()
	s.spool.Remove()
	d.emit(Event{Type: "state", State: StateIdle})
}

// watchSilence auto-cancels s if no speech is transcribed for `timeout`, so a
// dictation started by accident can't keep recording unnoticed. It exits as
// soon as the session is no longer the active recording (user stopped it).
func (d *Daemon) watchSilence(s *session, timeout time.Duration) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for range tick.C {
		d.mu.Lock()
		active := d.sess == s && d.state == StateRecording
		d.mu.Unlock()
		if !active {
			return // stopped or cancelled elsewhere
		}
		if time.Since(time.UnixMilli(s.lastActivity.Load())) >= timeout {
			d.cancelSilent(s, timeout)
			return
		}
	}
}

// cancelSilent discards s if it is still the active recording. A no-op if the
// user already stopped or cancelled it in the meantime.
func (d *Daemon) cancelSilent(s *session, timeout time.Duration) {
	d.mu.Lock()
	if d.sess != s || d.state != StateRecording {
		d.mu.Unlock()
		return
	}
	d.sess = nil
	d.state = StateIdle
	d.mu.Unlock()

	d.discard(s)
	d.playSound(s.cfg, assets.SoundCancel)
	d.note("vito: automatisch gestopt", fmt.Sprintf("geen spraak in %s", timeout))
	d.log.Info("recording auto-cancelled (silence)", "timeout", timeout)
}

// watchAutoStop finalizes s once speech has started and then paused for `pause`,
// so a tap-to-start dictation ends itself without a second keypress. It reuses
// the same lastActivity signal as the silence watchdog — provider-agnostic, no
// STT-protocol coupling — and defers to it before any speech is heard (a session
// that never hears anything is a silence-cancel, not an auto-stop). Exits as soon
// as s is no longer the active recording.
func (d *Daemon) watchAutoStop(s *session, pause time.Duration) {
	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()
	for range tick.C {
		d.mu.Lock()
		active := d.sess == s && d.state == StateRecording
		d.mu.Unlock()
		if !active {
			return // stopped or cancelled elsewhere
		}
		// Stay dormant until speech has actually begun: a session that never hears
		// anything is a silence-cancel, not an auto-stop.
		if s.spoke.Load() && time.Since(time.UnixMilli(s.lastActivity.Load())) >= pause {
			d.autoStopSession(s)
			return
		}
	}
}

// autoStopSession ends s the way the stop hotkey would — finalize the transcript
// and inject — when auto-stop detects you've stopped speaking. A no-op if s is no
// longer the active recording (the user stopped or cancelled it first).
func (d *Daemon) autoStopSession(s *session) {
	d.mu.Lock()
	if d.sess != s || d.state != StateRecording {
		d.mu.Unlock()
		return
	}
	d.sess = nil
	d.state = StateProcessing
	d.mu.Unlock()
	d.emit(Event{Type: "state", State: StateProcessing})
	d.playSound(s.cfg, assets.SoundDone)
	d.log.Info("recording auto-stopped (end of speech)")
	go d.finish(s)
}

// InjectText re-injects arbitrary text (used by the history "re-inject"
// button in the web UI).
func (d *Daemon) InjectText(text string) error {
	cfg := d.Config()
	_, err := inject.Inject(cfg.Injection, text)
	return err
}

// TestMic records `seconds` of microphone audio (with the configured device and
// gain) and plays it straight back, so the user can judge recording quality and
// volume. It emits "mic_test" events (recording → playing → done/error) for the
// web UI. Runs to completion; callers should invoke it in a goroutine.
func (d *Daemon) TestMic(seconds int) error {
	d.mu.Lock()
	if d.state != StateIdle || d.micTesting {
		d.mu.Unlock()
		return fmt.Errorf("bezig")
	}
	d.micTesting = true
	cfg := *d.cfg
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		d.micTesting = false
		d.mu.Unlock()
	}()

	d.emit(Event{Type: "mic_test", Text: "recording"})
	var mu sync.Mutex
	var pcm []byte
	var lastLevel time.Time
	capture, err := audio.StartCapture(d.audioCtx, cfg.Audio.InputDevice, cfg.Audio.InputGain, func(chunk []byte) {
		mu.Lock()
		pcm = append(pcm, chunk...)
		mu.Unlock()
		// The test meter stays on the absolute scale: you're setting the input
		// gain here, so it has to show the real level, not a normalised one.
		if now := time.Now(); now.Sub(lastLevel) >= 80*time.Millisecond {
			lastLevel = now
			r, clip := peakRatio(chunk)
			d.emit(Event{Type: "level", Level: absLevel(r), Clip: clip})
		}
	})
	if err != nil {
		d.emit(Event{Type: "mic_test", Text: "error", Error: err.Error()})
		return err
	}
	time.Sleep(time.Duration(seconds) * time.Second)
	capture.Stop()

	mu.Lock()
	data := pcm
	mu.Unlock()
	d.emit(Event{Type: "mic_test", Text: "playing"})
	if err := audio.PlayWAV(d.audioCtx, cfg.Audio.OutputDevice, audio.WAVFromPCM(data), 1.0); err != nil {
		d.emit(Event{Type: "mic_test", Text: "error", Error: err.Error()})
		return err
	}
	d.emit(Event{Type: "mic_test", Text: "done"})
	return nil
}

// InputLevel returns the OS microphone level (0..1) of the configured input
// device; SetInputLevel sets it. These control the real hardware/endpoint level,
// unlike audio.input_gain which is a software multiplier applied after capture.
func (d *Daemon) InputLevel() (float64, error) { return audio.InputLevel(d.Config().Audio.InputDevice) }
func (d *Daemon) SetInputLevel(l float64) error {
	return audio.SetInputLevel(d.Config().Audio.InputDevice, l)
}

func (d *Daemon) cleanupEffective(s *session) bool {
	return s.cfg.Cleanup.Enabled && cleanup.Configured(s.cfg.Cleanup)
}

// setPendingCmd / takePendingCmd hold the one-off spoken command ("Vito, vertaal
// naar Duits") between the command utterance and the dictation it applies to.
func (d *Daemon) setStatusCommand(c string) { d.mu.Lock(); d.status.Command = c; d.mu.Unlock() }
func (d *Daemon) setPendingCmd(c string) {
	d.cmdMu.Lock()
	d.pendingCmd = c
	d.cmdMu.Unlock()
	d.setStatusCommand(c)
	d.emit(Event{Type: "command", Command: c})
}
func (d *Daemon) takePendingCmd() string {
	d.cmdMu.Lock()
	c := d.pendingCmd
	d.pendingCmd = ""
	d.cmdMu.Unlock()
	return c
}

// clearCommandDisplay hides the command indicator. Kept separate from consuming
// the command so the banner lingers through the command's own dictation (and its
// result) and only clears when the next fresh dictation starts.
func (d *Daemon) clearCommandDisplay() {
	d.mu.Lock()
	had := d.status.Command != ""
	d.status.Command = ""
	d.mu.Unlock()
	if had {
		d.emit(Event{Type: "command", Command: ""})
	}
}

// parseCommand recognises a short spoken command that starts with the wake word,
// e.g. "Vito, vertaal naar Duits", and returns the instruction after it. Kept
// strict — wake-word-first and short — so ordinary dictation is never mistaken
// for a command. The wake word is matched loosely since the recogniser may hear
// "Vido"/"Fito".
func parseCommand(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	low := strings.ToLower(s)
	for _, w := range []string{"vito", "vido", "fito", "veto"} {
		if !strings.HasPrefix(low, w) || len(s) <= len(w) {
			continue
		}
		if !strings.ContainsRune(" ,:.!-\t", rune(s[len(w)])) { // a boundary, not "vitowski"
			continue
		}
		instr := strings.TrimSpace(strings.TrimLeft(s[len(w):], " ,:.!-\t"))
		if instr == "" || len(strings.Fields(instr)) > 15 { // longer than a short command = real text
			return "", false
		}
		return instr, true
	}
	return "", false
}

// clipboardInput reports whether a command works on the clipboard ("Vito, vat de
// tekst op het klembord samen") and, if so, returns its current contents.
func clipboardInput(instr string) (string, bool) {
	l := strings.ToLower(instr)
	if !strings.Contains(l, "klembord") && !strings.Contains(l, "clipboard") {
		return "", false
	}
	clip, _ := inject.ReadClipboard()
	return strings.TrimSpace(clip), true
}

func (d *Daemon) finish(s *session) {
	cfg := s.cfg
	stopAt := time.Now()
	duration := s.spool.Duration()

	s.capture.Stop()
	d.restoreMedia() // recording is over — bring the music back right away
	close(s.chunks)
	s.consumers.Wait()
	if err := s.spool.Close(); err != nil {
		d.log.Error("close spool", "err", err)
	}

	var keyterms []string
	if cfg.STT.KeytermsEnabled {
		keyterms = dictionary.Keyterms(cfg.Dictionary)
	}

	streamCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	text, err := s.stream.Finish(streamCtx)
	cancel()
	source := "stream"
	if err != nil || strings.TrimSpace(text) == "" {
		if err != nil {
			// A depleted balance would fail the async path the same way, so skip it
			// and report the billing problem straight away.
			if _, isCredit := apierr.CreditProvider(err); isCredit {
				d.finishWithError(s, err, duration)
				return
			}
			d.log.Warn("stream failed, falling back to async transcription", "err", err)
			d.note("vito: verwerken…", "streaming viel weg, spool wordt verwerkt")
			asyncCtx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			text, err = stt.NewAsyncClient(cfg.STT, keyterms).TranscribeFile(asyncCtx, s.spool.Path())
			cancel()
			source = "async"
		}
		if err != nil {
			d.finishWithError(s, fmt.Errorf("transcription failed (%s): %w", source, err), duration)
			return
		}
	}
	// Speech recognition succeeded: if this provider was flagged out of credit, it
	// clearly isn't anymore.
	d.markCredit(sttProviderName(cfg.STT), false)
	sttDone := time.Now()

	// Prefer the language the provider actually recognised (Soniox reports it per
	// token) so the history flag matches what was spoken, not just the configured
	// language; fall back to the configured one (e.g. AssemblyAI, async).
	entryLang := cfg.STT.Language
	if source == "stream" {
		if dl := s.stream.Language(); dl != "" {
			entryLang = dl
		}
	}

	raw := strings.TrimSpace(text)
	if raw == "" {
		d.takePendingCmd() // an empty follow-up drops any command that was armed
		d.setIdle(func(st *Status) { st.LastPreview = "" })
		d.emit(Event{Type: "state", State: StateIdle})
		s.spool.Remove()
		d.playSound(cfg, assets.SoundCancel)
		d.note("vito: niets gehoord", "")
		d.log.Info("empty transcript", "duration", duration, "source", source)
		return
	}

	// Deterministic corrections run always, so raw mode benefits too.
	raw = dictionary.Apply(raw, cfg.Dictionary.Corrections)

	// A spoken command ("Vito, vertaal naar Duits") arms the next dictation and is
	// not itself pasted; the following dictation carries it into the cleanup pass.
	var instruction string
	clipboardOut := false // a clipboard command returns its result to the clipboard, not the focused field
	if instr, ok := parseCommand(raw); ok {
		if !d.cleanupEffective(s) { // the cleanup pass is what runs the command, so it must be on
			d.playSound(cfg, assets.SoundCancel)
			d.note("vito: opdracht genegeerd", "AI-opschoning staat uit — zet die aan voor spraakopdrachten")
			d.setIdle(func(st *Status) { st.LastPreview = "" })
			d.emit(Event{Type: "state", State: StateIdle})
			s.spool.Remove()
			d.log.Info("command ignored, cleanup disabled", "instruction", instr)
			return
		}
		if clip, isClip := clipboardInput(instr); isClip {
			// The command works on the clipboard ("vat de tekst op het klembord
			// samen"): run it on that text right away — no follow-up dictation.
			if clip == "" {
				d.playSound(cfg, assets.SoundCancel)
				d.note("vito: klembord leeg", "geen tekst op het klembord voor deze opdracht")
				d.setIdle(func(st *Status) { st.LastPreview = "" })
				d.emit(Event{Type: "state", State: StateIdle})
				s.spool.Remove()
				return
			}
			d.playSound(cfg, assets.SoundCommand)
			d.note("vito: opdracht op klembord — "+instr, "")
			d.setStatusCommand(instr)
			d.emit(Event{Type: "command", Command: instr, CommandReceived: true}) // input is the clipboard → no "speak your text" state
			raw = clip
			instruction = instr
			clipboardOut = true
			// fall through to the cleanup + inject path below
		} else {
			d.setPendingCmd(instr)
			d.playSound(cfg, assets.SoundCommand)
			d.note("vito: opdracht — "+instr, "")
			d.setIdle(func(st *Status) { st.LastPreview = "" })
			d.emit(Event{Type: "state", State: StateIdle})
			s.spool.Remove()
			d.log.Info("command armed", "instruction", instr)
			// Re-open the mic right away so you can dictate the target text without
			// pressing the hotkey again — one continuous flow.
			go func() {
				time.Sleep(350 * time.Millisecond) // let the ack sound play and the session tear down
				if err := d.start(); err != nil {
					d.log.Warn("auto-restart after command failed", "err", err)
				}
			}()
			return
		}
	} else {
		instruction = d.takePendingCmd()
	}

	cleaned := ""
	cleanupUsed := false
	cleanupFailed := false
	cleanupCredit := ""
	var cleanUsage cleanup.Usage
	// A Vito Assist command may run on its own (heavier) model; a plain dictation
	// always uses the cleanup model. The master on/off switch stays cfg.Cleanup —
	// gated by cleanupEffective below — so Assist still requires AI cleanup to be on.
	cleanCfg := cfg.Cleanup
	if instruction != "" && !cfg.Assist.UsesCleanupModel() {
		cleanCfg = cfg.Assist.Cleanup
		cleanCfg.Enabled = true
		if cleanCfg.TimeoutMS <= 0 {
			cleanCfg.TimeoutMS = cfg.Cleanup.TimeoutMS
		}
	}
	// Cleanup must be on. A pending command then forces a pass even below the
	// min-words threshold (that's how the command is applied).
	if d.cleanupEffective(s) && (instruction != "" || wordCount(raw) >= cfg.Cleanup.MinWords) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cleanCfg.TimeoutMS)*time.Millisecond)
		out, usage, err := cleanup.NewCleaner(cleanCfg).Clean(ctx, raw, cfg.STT.Language, cfg.Dictionary.Corrections, instruction)
		cancel()
		cleanUsage = usage // tokens are billed even if we then reject the output
		if err != nil {
			// Never block on cleanup: inject the raw transcript instead.
			d.log.Warn("cleanup failed, injecting raw transcript", "err", err)
			cleanupFailed = true
			if p, isCredit := apierr.CreditProvider(err); isCredit {
				cleanupCredit = p
				d.markCredit(p, true)
			}
		} else {
			cleaned = out
			cleanupUsed = true
			d.markCredit(cleanup.ProviderName(cleanCfg), false) // a successful pass clears any prior flag
		}
	}
	cleanupDone := time.Now()

	final := raw
	if cleanupUsed {
		final = cleaned
	}

	// A clipboard command is clipboard-in, clipboard-out: put the result back on
	// the clipboard for you to paste yourself, rather than pasting into whatever
	// happens to be focused (which the restore step would then overwrite with the
	// original input).
	injCfg := cfg.Injection
	if clipboardOut {
		injCfg = config.Injection{Mode: "clipboard_only"}
	}
	mode, err := inject.Inject(injCfg, final)
	if err != nil {
		// The text exists; never lose it. Fall back to clipboard only.
		if _, cbErr := inject.Inject(config.Injection{Mode: "clipboard_only"}, final); cbErr == nil {
			mode = inject.ModeClipboardOnly
			d.noteErr("vito: in klembord (injectie faalde)", err.Error())
		} else {
			d.finishWithError(s, fmt.Errorf("inject: %w", err), duration)
			return
		}
	}
	injected := time.Now()

	timings := Timings{
		Recording: duration,
		SttFinal:  sttDone.Sub(stopAt),
		Cleanup:   cleanupDone.Sub(sttDone),
		Injected:  injected.Sub(stopAt),
	}
	// A kept recording is named after the history entry it belongs to, so the id
	// is minted here rather than by the store.
	entryID := history.NewID()
	recordingID := ""
	if cfg.History.Enabled && cfg.History.StoreAudio && d.hist != nil && !d.privacyActive() {
		// Favorites' recordings are kept regardless of the last-N cap.
		keep, _ := d.hist.FavoriteIDs()
		if _, err := audio.SaveRecording(s.spool.Path(), entryID, keep); err != nil {
			d.log.Warn("keeping the recording failed", "err", err)
		} else {
			recordingID = entryID
		}
	}
	d.setIdle(func(st *Status) {
		st.LastPreview = preview(final)
		st.LastTimings = timings
		st.LastRecordingID = recordingID
	})
	if recordingID == "" && !cfg.Audio.KeepSpool {
		s.spool.Remove()
	}

	if cfg.History.Enabled && d.hist != nil {
		entry := history.Entry{
			ID:               entryID,
			Timestamp:        s.started,
			DurationMS:       duration.Milliseconds(),
			Language:         entryLang,
			Source:           source,
			Raw:              raw,
			Cleaned:          cleaned,
			CleanupUsed:      cleanupUsed,
			Command:          instruction != "",
			CommandText:      instruction,
			ClipboardCommand: clipboardOut,
			SttMS:            timings.SttFinal.Milliseconds(),
			CleanupMS:        timings.Cleanup.Milliseconds(),
			InjectedMS:       timings.Injected.Milliseconds(),
		}
		// A Vito Assist command runs through the same cleaner, but its tokens are
		// question-answering / transformation, not tidy-up — bill them to the
		// command bucket so the cost breakdown keeps the two lines apart.
		if instruction != "" {
			entry.CommandInTokens, entry.CommandOutTokens = cleanUsage.InputTokens, cleanUsage.OutputTokens
		} else {
			entry.CleanupInTokens, entry.CleanupOutTokens = cleanUsage.InputTokens, cleanUsage.OutputTokens
		}
		// Privacy mode keeps the usage statistics but never writes the transcript
		// text to disk; otherwise store the full entry.
		var err error
		if d.privacyActive() {
			err = d.hist.AppendStatsOnly(entry)
		} else {
			err = d.hist.Append(entry)
		}
		if err != nil {
			d.log.Warn("history append failed", "err", err)
		}
	}

	d.emit(Event{Type: "final", Raw: raw, Cleaned: cleaned, Timings: timings, RecordingID: recordingID, CleanupFailed: cleanupFailed, CleanupCredit: cleanupCredit})
	d.emit(Event{Type: "state", State: StateIdle})
	// The "done" chime already played when recording stopped (see Stop). It
	// promised a finished dictation, so when the cleanup then failed and the raw
	// transcript went in instead, say so out loud — otherwise the only clue is a
	// toast in a window you are probably not looking at, and unpolished text
	// reads as Vito having done its job badly rather than not at all.
	if cleanupFailed {
		d.playSound(cfg, assets.SoundWarn)
	}
	switch {
	case cleanupFailed && mode == inject.ModeClipboardOnly:
		d.noteErr("vito: opschoning mislukt", "ruwe tekst in klembord — "+preview(final))
	case cleanupFailed:
		d.noteErr("vito: opschoning mislukt", "ruwe tekst geplakt — "+preview(final))
	case mode == inject.ModeClipboardOnly:
		d.note("vito: in klembord", preview(final))
	default:
		d.note("vito: klaar", preview(final))
	}
	d.log.Info("dictation done",
		"duration", duration.Round(10*time.Millisecond),
		"source", source,
		"cleanup", cleanupUsed,
		"stt_final", timings.SttFinal.Round(time.Millisecond),
		"cleanup_ms", timings.Cleanup.Round(time.Millisecond),
		"injected", timings.Injected.Round(time.Millisecond),
		"chars", len(final))
}

func (d *Daemon) finishWithError(s *session, err error, duration time.Duration) {
	d.setIdleWithError(err)
	provider, isCredit := apierr.CreditProvider(err)
	if isCredit {
		d.markCredit(provider, true)
	}
	// keep the spool: the audio is still recoverable by hand
	d.emit(Event{Type: "error", Error: err.Error(), Credit: provider})
	d.emit(Event{Type: "state", State: StateIdle})
	d.playSound(s.cfg, assets.SoundCancel)
	if isCredit {
		d.noteErr("vito: tegoed op", provider+" — audio bewaard: "+s.spool.Path())
	} else {
		d.noteErr("vito: fout", err.Error()+" — audio bewaard: "+s.spool.Path())
	}
	d.log.Error("dictation failed", "err", err, "duration", duration, "spool", s.spool.Path())
}

func (d *Daemon) setIdle(update func(*Status)) {
	d.mu.Lock()
	d.state = StateIdle
	if update != nil {
		update(&d.status)
	}
	d.mu.Unlock()
}

func (d *Daemon) setIdleWithError(err error) {
	d.setIdle(func(st *Status) { st.LastError = err.Error() })
}

func (d *Daemon) playSound(cfg config.Config, wav []byte) {
	if !cfg.Audio.SoundsEnabled {
		return
	}
	go func() {
		if err := audio.PlayWAV(d.audioCtx, cfg.Audio.OutputDevice, wav, cfg.Audio.SoundsVolume); err != nil {
			d.log.Debug("sound playback failed", "err", err)
		}
	}()
}

// PlaySound plays one named feedback sound (start|done|cancel) on the output
// device — used by the settings preview buttons. A volume < 0 falls back to the
// configured feedback volume; otherwise the given volume is used so previews
// reflect the (possibly unsaved) slider. Ignores the sounds-enabled toggle.
func (d *Daemon) PlaySound(name string, volume float64) error {
	var wav []byte
	switch name {
	case "start":
		wav = assets.SoundStart
	case "done":
		wav = assets.SoundDone
	case "cancel":
		wav = assets.SoundCancel
	case "achievement":
		wav = assets.SoundAchievement
	case "command":
		wav = assets.SoundCommand
	case "warn":
		wav = assets.SoundWarn
	default:
		return fmt.Errorf("unknown sound %q", name)
	}
	cfg := d.Config()
	if volume < 0 {
		volume = cfg.Audio.SoundsVolume
	}
	return audio.PlayWAV(d.audioCtx, cfg.Audio.OutputDevice, wav, volume)
}

// peakRatio returns the chunk's peak as a 0..1 fraction of full scale plus a
// clip indicator. Clipping is judged on the raw sample, so "red" keeps meaning
// actual clipping no matter how the value is scaled for display.
func peakRatio(pcm []byte) (float64, bool) {
	var peak int32
	for i := 0; i+1 < len(pcm); i += 2 {
		v := int32(int16(binary.LittleEndian.Uint16(pcm[i:])))
		if v < 0 {
			v = -v
		}
		if v > peak {
			peak = v
		}
	}
	return float64(peak) / 32767, peak >= 32700
}

const (
	meterGate   = 0.012                  // ambient hiss: the bar rests at 0 below this
	meterRefMin = 0.05                   // ≈ -26 dBFS: quietest peak we'll stretch to full
	meterWindow = 2 * time.Second        // sliding window whose loudest peak is "100%"
	meterFall   = 400 * time.Millisecond // how fast the scale eases back down
)

type meterSample struct {
	at   time.Time
	peak float64
}

// meterScale maps peaks onto the 0..100 bar relative to the loudest peak in a
// sliding window of the last meterWindow, rather than against full scale.
// Speech sits far below full scale and how far depends on the mic, the gain and
// how loudly you happen to talk, so a fixed scale either hugs the bottom or
// pegs the top. Normalising to the window means the loudest moment of the last
// two seconds always reads 100 and everything else is a true dB reading beneath
// it, so the bar uses its whole range on any setup and re-scales as you get
// louder or quieter.
//
// The reference never goes below meterRefMin, so room tone in a silent room
// can't be stretched into a full deflection.
type meterScale struct {
	win  []meterSample
	ref  float64 // applied reference: rises instantly, eases down
	last time.Time
}

func (m *meterScale) level(r float64, now time.Time) int {
	m.win = append(m.win, meterSample{at: now, peak: r})
	cut := now.Add(-meterWindow)
	drop := 0
	for drop < len(m.win) && m.win[drop].at.Before(cut) {
		drop++
	}
	m.win = m.win[drop:]

	target := meterRefMin
	for _, s := range m.win {
		if s.peak > target {
			target = s.peak
		}
	}
	// Rise with the audio, but glide back down: when the loudest sample drops
	// out of the window the scale would otherwise snap and the bar would visibly
	// jump without the speech having changed.
	switch {
	case m.last.IsZero() || target >= m.ref:
		m.ref = target
	default:
		if dt := now.Sub(m.last).Seconds(); dt > 0 {
			m.ref = target + (m.ref-target)*math.Exp(-dt/meterFall.Seconds())
		}
	}
	m.last = now

	if r <= meterGate {
		return 0
	}
	floorDB, refDB := 20*math.Log10(meterGate), 20*math.Log10(m.ref)
	if refDB <= floorDB {
		return 100
	}
	disp := int((20*math.Log10(r)-floorDB)/(refDB-floorDB)*100 + 0.5)
	if disp > 100 {
		disp = 100
	}
	if disp < 0 {
		disp = 0
	}
	return disp
}

// absLevel maps a peak onto 0..100 on a fixed decibel scale running from the
// noise gate up to full scale. The microphone test uses this instead of
// meterScale: there the whole point is to read the true input level while you
// set the hardware gain, and normalising would hide exactly what you're
// adjusting.
func absLevel(r float64) int {
	if r <= meterGate {
		return 0
	}
	floorDB := 20 * math.Log10(meterGate)
	disp := int((20*math.Log10(r)-floorDB)/-floorDB*100 + 0.5)
	if disp > 100 {
		disp = 100
	}
	return disp
}

func wordCount(s string) int { return len(strings.Fields(s)) }

func preview(text string) string {
	const max = 80
	text = strings.ReplaceAll(text, "\n", " ")
	if len(text) > max {
		return text[:max] + "…"
	}
	return text
}
