//go:build darwin

// Package hotkey registers global hotkeys where the OS allows it.
//
// On macOS this is a CoreGraphics event tap rather than Carbon's
// RegisterEventHotKey, for the same reason Windows uses a low-level hook
// instead of RegisterHotKey: the tap sees presses *and* releases, which is what
// "push-to-talk" (hold to record, release to stop) needs, and it can swallow
// the key so the combination never reaches the focused app. A quick tap still
// toggles; only a deliberate hold records for as long as the key is down.
//
// The tap needs the Accessibility right — the same one paste already needs.
// Without it CGEventTapCreate simply returns nothing, which is reported as
// ErrCode "denied" so the settings page can explain what to grant.
package hotkey

/*
#cgo LDFLAGS: -framework CoreFoundation -framework CoreGraphics
#include "hotkey_darwin.h"
*/
import "C"

import (
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"time"

	"vito/internal/daemon"
)

// CGEventFlags bits for the modifiers Vito can bind. The raw flags also carry
// bits Vito does not care about (non-coalesced, keypad, fn), so a comparison
// always masks down to these four first.
const (
	modShift   = 0x00020000
	modControl = 0x00040000
	modOption  = 0x00080000
	modCommand = 0x00100000

	modMask = modShift | modControl | modOption | modCommand

	// holdThreshold separates a tap (toggle) from a hold (push-to-talk).
	holdThreshold = 350 * time.Millisecond

	// tapRetryInterval is how often a refused event tap is tried again. A
	// refused CGEventTapCreate returns immediately, so this is cheap enough to
	// leave running for the life of the process.
	tapRetryInterval = 2 * time.Second
)

// BindInfo reports one hotkey's configured combination and whether it is
// currently registered (errCode: "" | "invalid" | "denied").
type BindInfo struct {
	Spec       string
	Registered bool
	ErrCode    string
}

type hkEvent int

const (
	evToggleDown hkEvent = iota
	evToggleUp
	evCancel
)

// Manager owns the global hotkeys (start/stop and cancel) via a single event
// tap, and a worker that turns tap events into daemon actions.
type Manager struct {
	d   *daemon.Daemon
	log *slog.Logger

	mu         sync.Mutex
	wantToggle string
	wantCancel string
	stToggle   BindInfo
	stCancel   BindInfo

	// denied records that macOS refused the event tap, so Status can explain
	// why a perfectly valid combination is not actually listening.
	denied bool

	// Parsed specs, read by the tap callback on every key event.
	tglOK           bool
	tglMods, tglKey uint64
	cxlOK           bool
	cxlMods, cxlKey uint64

	// toggleHeld is touched only by the tap callback (one thread), so it needs
	// no lock: whether the toggle key is currently held down through our tap.
	toggleHeld bool

	events chan hkEvent

	stop     chan struct{}
	stopOnce sync.Once
}

// active is the Manager the package-level tap callback talks to. There is only
// ever one Manager; it is set before the tap is created and never changes.
var active *Manager

// New creates a Manager; call Start to install the tap.
func New(d *daemon.Daemon, log *slog.Logger) *Manager {
	return &Manager{d: d, log: log, stop: make(chan struct{})}
}

// Start installs the event tap on a dedicated OS thread and runs the worker.
// Never fatal: without the Accessibility right Vito still works from the tray
// and the web UI, it just has no global hotkey.
func (m *Manager) Start(toggle, cancel string) {
	m.mu.Lock()
	m.wantToggle, m.wantCancel = toggle, cancel
	m.events = make(chan hkEvent, 32)
	m.mu.Unlock()
	m.reparse()

	go m.worker()

	go func() {
		// The tap and the run loop it is added to must share one OS thread.
		runtime.LockOSThread()
		active = m
		if !m.awaitTap() {
			return
		}
		m.setDenied(false)
		m.log.Info("keyboard event tap installed (push-to-talk capable)")
		C.vitoTapRun() // parks this thread until Stop
	}()
}

// awaitTap creates the event tap, waiting for the Accessibility right if macOS
// refuses it, and reports whether it succeeded.
//
// It retries rather than giving up because of when the permission is granted:
// the user reads the warning, opens System Settings and ticks the box — with
// Vito already running. Checking only once meant that box did nothing until the
// app was restarted, which is a poor answer to "I just granted it".
//
// Returns false only when the Manager is stopped while waiting.
func (m *Manager) awaitTap() bool {
	if bool(C.vitoTapCreate()) {
		return true
	}
	m.setDenied(true)
	m.log.Warn("no Accessibility permission yet; the global hotkey starts working as soon as it is " +
		"granted (System Settings → Privacy & Security → Accessibility)")

	for {
		select {
		case <-m.stop:
			return false
		case <-time.After(tapRetryInterval):
			if bool(C.vitoTapCreate()) {
				m.log.Info("Accessibility permission granted")
				return true
			}
		}
	}
}

func (m *Manager) setDenied(denied bool) {
	m.mu.Lock()
	m.denied = denied
	m.mu.Unlock()
	m.reparse()
}

// Stop tears the tap down, and releases the goroutine still waiting for the
// Accessibility right if it never arrived. Only needed on shutdown.
func (m *Manager) Stop() {
	m.stopOnce.Do(func() { close(m.stop) })
	C.vitoTapStop()
}

// Rebind swaps in new combinations at runtime. The tap is generic and reads the
// parsed specs live, so this only has to re-parse them.
func (m *Manager) Rebind(toggle, cancel string) {
	m.mu.Lock()
	m.wantToggle, m.wantCancel = toggle, cancel
	m.mu.Unlock()
	m.reparse()
}

// Status reports both hotkeys' state; supported is always true on macOS.
func (m *Manager) Status() (toggle, cancel BindInfo, supported bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stToggle, m.stCancel, true
}

// reparse turns the configured specs into modifiers + a key code the tap can
// match, and records each one's status for the UI.
func (m *Manager) reparse() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tglOK, m.tglMods, m.tglKey, m.stToggle = parseInto(m.wantToggle, m.denied, m.log, "toggle")
	m.cxlOK, m.cxlMods, m.cxlKey, m.stCancel = parseInto(m.wantCancel, m.denied, m.log, "cancel")
}

func parseInto(spec string, denied bool, log *slog.Logger, which string) (ok bool, mods, key uint64, info BindInfo) {
	spec = strings.TrimSpace(strings.ToLower(spec))
	if spec == "" {
		return false, 0, 0, BindInfo{}
	}
	mods, key, err := parseSpec(spec)
	if err != nil {
		log.Warn("invalid hotkey", "which", which, "spec", spec, "err", err)
		return false, 0, 0, BindInfo{Spec: spec, ErrCode: "invalid"}
	}
	if denied {
		// The combination is fine; macOS just isn't letting Vito listen for it.
		return true, mods, key, BindInfo{Spec: spec, ErrCode: "denied"}
	}
	return true, mods, key, BindInfo{Spec: spec, Registered: true}
}

// vitoHotkeyEvent is the event-tap callback, called from the run-loop thread.
// It returns 1 to swallow the key.
//
//export vitoHotkeyEvent
func vitoHotkeyEvent(code C.longlong, flags C.ulonglong, down C.int, repeat C.int) C.int {
	m := active
	if m == nil {
		return 0
	}
	if m.handleKey(uint64(code), uint64(flags), down != 0, repeat != 0) {
		return 1
	}
	return 0
}

// handleKey decides whether a key event is one of our combinations, emits the
// matching event, and reports whether to swallow the key.
func (m *Manager) handleKey(key, flags uint64, down, repeat bool) bool {
	mods := flags & modMask

	m.mu.Lock()
	tglOK, tglKey, tglMods := m.tglOK, m.tglKey, m.tglMods
	cxlOK, cxlKey, cxlMods := m.cxlOK, m.cxlKey, m.cxlMods
	denied := m.denied
	m.mu.Unlock()

	if denied {
		return false
	}

	if cxlOK && key == cxlKey && down && mods == cxlMods {
		m.send(evCancel)
		return true
	}
	if tglOK && key == tglKey {
		if down {
			if m.toggleHeld || repeat {
				return m.toggleHeld // key-repeat while held: swallow, but do nothing
			}
			if mods == tglMods {
				m.toggleHeld = true
				m.send(evToggleDown)
				return true
			}
			return false // the key pressed without our modifiers is a normal key
		}
		if m.toggleHeld {
			m.toggleHeld = false
			m.send(evToggleUp)
			return true
		}
	}
	return false
}

func (m *Manager) send(ev hkEvent) {
	select {
	case m.events <- ev:
	default:
		m.log.Warn("hotkey event dropped (worker busy)")
	}
}

// worker turns tap events into daemon actions. It is the only place the daemon
// is called, off the tap thread, so a slow Start/Stop can't stall the tap — and
// a stalled tap is what macOS disables for taking too long.
//
// Tap vs hold: a press starts recording (when idle) and arms a hold; the release
// stops it only if the key was held past the threshold. A quick tap therefore
// leaves recording running, and the next tap stops it — the familiar toggle.
func (m *Manager) worker() {
	var armed bool
	var downAt time.Time
	for ev := range m.events {
		ptt := m.d.Config().PushToTalkEnabled()
		switch ev {
		case evToggleDown:
			if !ptt { // hold-to-talk off: plain toggle on every press
				if _, err := m.d.Toggle(); err != nil {
					m.log.Warn("hotkey toggle rejected", "err", err)
				}
				continue
			}
			downAt = time.Now()
			if m.d.Status().State == daemon.StateRecording {
				// A press while already recording is "tap again to stop".
				if err := m.d.Stop(); err != nil {
					m.log.Debug("hotkey stop rejected", "err", err)
				}
				armed = false
			} else if err := m.d.Start(); err != nil {
				m.log.Debug("hotkey start rejected", "err", err)
				armed = false
			} else {
				armed = true
			}
		case evToggleUp:
			if ptt && armed && time.Since(downAt) >= holdThreshold {
				if err := m.d.Stop(); err != nil {
					m.log.Debug("hotkey release stop rejected", "err", err)
				}
			}
			armed = false
		case evCancel:
			if err := m.d.Cancel(); err != nil {
				m.log.Debug("hotkey cancel", "err", err)
			}
			armed = false
		}
	}
}

// macKeys maps Vito's hotkey vocabulary to macOS virtual key codes. The letter
// and digit codes are positional (they follow the physical ANSI layout), which
// is why they look unordered.
var macKeys = map[string]uint64{
	"a": 0x00, "s": 0x01, "d": 0x02, "f": 0x03, "h": 0x04, "g": 0x05,
	"z": 0x06, "x": 0x07, "c": 0x08, "v": 0x09, "b": 0x0B, "q": 0x0C,
	"w": 0x0D, "e": 0x0E, "r": 0x0F, "y": 0x10, "t": 0x11, "o": 0x1F,
	"u": 0x20, "i": 0x22, "p": 0x23, "l": 0x25, "j": 0x26, "k": 0x28,
	"n": 0x2D, "m": 0x2E,

	"1": 0x12, "2": 0x13, "3": 0x14, "4": 0x15, "5": 0x17,
	"6": 0x16, "7": 0x1A, "8": 0x1C, "9": 0x19, "0": 0x1D,

	"space": 0x31, "return": 0x24, "enter": 0x24, "tab": 0x30,
	"esc": 0x35, "escape": 0x35, "backspace": 0x33,
}

// macFKeys are the function keys, which are not contiguous either.
var macFKeys = map[int]uint64{
	1: 0x7A, 2: 0x78, 3: 0x63, 4: 0x76, 5: 0x60, 6: 0x61, 7: 0x62,
	8: 0x64, 9: 0x65, 10: 0x6D, 11: 0x67, 12: 0x6F, 13: 0x69, 14: 0x6B,
	15: 0x71, 16: 0x6A, 17: 0x40, 18: 0x4F, 19: 0x50, 20: 0x5A,
}

// parseSpec turns "ctrl+alt+space" into modifier flags + a macOS key code.
// The vocabulary is shared with Windows, so "win"/"super"/"meta" all mean the
// Command key here, and "alt" means Option — that is the key in that position.
func parseSpec(spec string) (mods, key uint64, err error) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(spec)), "+")
	if len(parts) == 0 {
		return 0, 0, fmt.Errorf("empty hotkey")
	}
	name := strings.TrimSpace(parts[len(parts)-1])
	for _, p := range parts[:len(parts)-1] {
		switch strings.TrimSpace(p) {
		case "ctrl", "control":
			mods |= modControl
		case "alt", "opt", "option":
			mods |= modOption
		case "shift":
			mods |= modShift
		case "win", "super", "meta", "cmd", "command":
			mods |= modCommand
		default:
			return 0, 0, fmt.Errorf("unknown modifier %q", p)
		}
	}
	switch code, ok := macKeys[name]; {
	case ok:
		key = code
	case len(name) >= 2 && name[0] == 'f':
		var n int
		if _, err := fmt.Sscanf(name, "f%d", &n); err != nil {
			return 0, 0, fmt.Errorf("unknown key %q", name)
		}
		fcode, ok := macFKeys[n]
		if !ok {
			return 0, 0, fmt.Errorf("unknown key %q", name)
		}
		key = fcode
	default:
		return 0, 0, fmt.Errorf("unknown key %q", name)
	}
	if mods == 0 {
		return 0, 0, fmt.Errorf("hotkey needs at least one modifier")
	}
	return mods, key, nil
}
