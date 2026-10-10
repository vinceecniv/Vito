package daemon

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"vito/internal/wakeword"
)

// The wake word: while Vito is idle and the setting is on, a listener keeps
// the microphone open and starts a dictation when it hears the phrase. It
// steps aside for everything else that uses the microphone — a dictation,
// the microphone test, and (unless the user says otherwise) a call in
// another program — and comes back once that is over.

// wakeSilenceSec cancels a wake-started dictation in which nothing is said:
// with no key pressed, a false wake must end on its own, and soon.
const wakeSilenceSec = 8

type wakeState struct {
	mu       sync.Mutex
	l        *wakeword.Listener
	key      string // what the running listener was started with
	status   string // "", "listening", "paused_call", "error"
	err      string
	meterTil atomic.Int64 // unix ms until which scores are sent to the UI
	pollOnce sync.Once
}

// WakeStatus is the wake word's state for the settings page.
type WakeStatus struct {
	Status  string   `json:"status"` // "" (off), listening, paused, paused_call, error
	Error   string   `json:"error,omitempty"`
	Phrases []string `json:"phrases"`
	// Thresholds maps each phrase to its threshold at the middle sensitivity,
	// for the settings page's meter.
	Thresholds map[string]float32 `json:"thresholds"`
}

func (d *Daemon) wakeInit() {
	d.wake.l = wakeword.NewListener(d.log, d.audioCtx)
	d.wake.l.OnWake = d.onWake
	d.wake.l.OnScore = func(s float32) {
		if time.Now().UnixMilli() < d.wake.meterTil.Load() {
			d.emit(Event{Type: "wake_score", Level: int(s * 100)})
		}
	}
}

// WakeMeter asks for live wake word scores for the next 30 seconds; the
// settings page renews it while its meter is on screen.
func (d *Daemon) WakeMeter() { d.wake.meterTil.Store(time.Now().Add(30 * time.Second).UnixMilli()) }

// Wake reports the wake word's state.
func (d *Daemon) Wake() WakeStatus {
	d.wake.mu.Lock()
	defer d.wake.mu.Unlock()
	ph := wakeword.Phrases()
	if ph == nil {
		ph = []string{}
	}
	th := map[string]float32{}
	for _, p := range ph {
		th[p] = wakeword.Threshold(p, 0.5)
	}
	return WakeStatus{Status: d.wake.status, Error: d.wake.err, Phrases: ph, Thresholds: th}
}

// SyncWake starts or stops the listener to match the settings and what Vito
// is doing. Cheap when nothing changes; called on every state change.
func (d *Daemon) SyncWake() {
	d.wake.pollOnce.Do(func() { go d.wakePoll() })
	d.mu.Lock()
	cfg := *d.cfg
	idle := d.state == StateIdle && !d.micTesting
	d.mu.Unlock()
	ww := cfg.WakeWord
	phrase := ww.Phrase
	if phrase == "" {
		phrase = "hey_vito"
	}

	d.wake.mu.Lock()
	defer d.wake.mu.Unlock()
	status, errText := "", ""
	switch {
	case !ww.Enabled:
	case !idle:
		status = "paused"
	case !ww.KeepListeningInCalls && wakeword.MicInUseByOthers():
		status = "paused_call"
	default:
		status = "listening"
	}
	if status != "listening" {
		d.wake.l.Stop()
		d.wake.key = ""
	} else {
		key := fmt.Sprintf("%s|%s|%g|%g", phrase, cfg.Audio.InputDevice, cfg.Audio.InputGain, ww.Sensitivity)
		if d.wake.l.Running() == "" || d.wake.key != key {
			if err := d.wake.l.Start(cfg.Audio.InputDevice, cfg.Audio.InputGain, phrase, wakeword.Threshold(phrase, ww.Sensitivity)); err != nil {
				status, errText = "error", err.Error()
				if errText != d.wake.err { // the poll retries; say it once
					d.log.Error("wake word", "err", err)
				}
				d.wake.key = ""
			} else {
				d.wake.key = key
			}
		}
	}
	if status != d.wake.status || errText != d.wake.err {
		d.wake.status, d.wake.err = status, errText
		go d.emit(Event{Type: "wake", Text: status, Error: errText})
	}
}

// wakePoll notices what no event announces: a call starting or ending in
// another program, or an idle state reached without a state event.
func (d *Daemon) wakePoll() {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for range t.C {
		d.mu.Lock()
		on := d.cfg.WakeWord.Enabled
		d.mu.Unlock()
		d.wake.mu.Lock()
		running := d.wake.status != ""
		d.wake.mu.Unlock()
		if on || running {
			d.SyncWake()
		}
	}
}

func (d *Daemon) onWake(score float32) {
	d.mu.Lock()
	idle := d.state == StateIdle && !d.micTesting
	d.mu.Unlock()
	if !idle {
		return
	}
	d.emit(Event{Type: "wake", Text: "heard", Level: int(score * 100)})
	if err := d.startWith(true); err != nil {
		d.log.Warn("wake word: could not start dictation", "err", err)
	}
}
