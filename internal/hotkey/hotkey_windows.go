//go:build windows

// Package hotkey registers global hotkeys where the OS allows it.
//
// On Windows this is a low-level keyboard hook (WH_KEYBOARD_LL) rather than
// RegisterHotKey, because RegisterHotKey only reports a press — never a release.
// The hook sees both, which is what "push-to-talk" (hold to record, release to
// stop) needs. A quick tap still toggles; only a deliberate hold records for as
// long as the key is down.
package hotkey

import (
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"vito/internal/daemon"
)

var (
	user32                = syscall.NewLazyDLL("user32.dll")
	procGetMessageW       = user32.NewProc("GetMessageW")
	procSetWindowsHookExW = user32.NewProc("SetWindowsHookExW")
	procCallNextHookEx    = user32.NewProc("CallNextHookEx")
	procGetAsyncKeyState  = user32.NewProc("GetAsyncKeyState")
)

const (
	modAlt     = 0x0001
	modControl = 0x0002
	modShift   = 0x0004
	modWin     = 0x0008

	whKeyboardLL = 13
	hcAction     = 0
	wmKeyDown    = 0x0100
	wmKeyUp      = 0x0101
	wmSysKeyDown = 0x0104
	wmSysKeyUp   = 0x0105

	vkShift   = 0x10
	vkControl = 0x11
	vkMenu    = 0x12 // Alt
	vkLWin    = 0x5B
	vkRWin    = 0x5C

	// holdThreshold separates a tap (toggle) from a hold (push-to-talk).
	holdThreshold = 350 * time.Millisecond
)

type kbdllHookStruct struct {
	VkCode      uint32
	ScanCode    uint32
	Flags       uint32
	Time        uint32
	DwExtraInfo uintptr
}

type winMsg struct {
	HWND    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

// BindInfo reports one hotkey's configured combination and whether it is
// currently registered (errCode: "" | "invalid").
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

// Manager owns the global hotkeys (start/stop and cancel) via a single
// keyboard hook, and a worker that turns hook events into daemon actions.
type Manager struct {
	d   *daemon.Daemon
	log *slog.Logger

	mu         sync.Mutex
	wantToggle string
	wantCancel string
	stToggle   BindInfo
	stCancel   BindInfo

	// Parsed specs, read by the hook callback on every key event.
	tglOK          bool
	tglMods, tglVK uint32
	cxlOK          bool
	cxlMods, cxlVK uint32

	// toggleHeld is touched only by the hook callback (one thread), so it needs
	// no lock: whether the toggle key is currently held down through our hook.
	toggleHeld bool

	events chan hkEvent
}

// active is the Manager the package-level hook callback talks to. There is only
// ever one Manager; it is set before the hook is installed and never changes.
var active *Manager
var hookCallback = syscall.NewCallback(hookProc)

// New creates a Manager; call Start to install the hook.
func New(d *daemon.Daemon, log *slog.Logger) *Manager { return &Manager{d: d, log: log} }

// Start installs the keyboard hook on a dedicated OS thread and runs the worker.
// Never fatal.
func (m *Manager) Start(toggle, cancel string) {
	m.mu.Lock()
	m.wantToggle, m.wantCancel = toggle, cancel
	m.events = make(chan hkEvent, 32)
	m.mu.Unlock()
	m.reparse()

	go m.worker()

	go func() {
		// SetWindowsHookEx and its message pump must share one OS thread.
		runtime.LockOSThread()
		active = m
		h, _, err := procSetWindowsHookExW.Call(uintptr(whKeyboardLL), hookCallback, 0, 0)
		if h == 0 {
			m.log.Error("installing the keyboard hook failed; hotkeys are unavailable", "err", err)
			return
		}
		m.log.Info("keyboard hook installed (push-to-talk capable)")
		// A blocking GetMessage keeps the thread pumping so the low-level hook
		// callback fires; no messages are posted, so this simply parks the thread.
		var msg winMsg
		for {
			r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
			if int32(r) <= 0 { // WM_QUIT or error
				return
			}
		}
	}()
}

// Rebind swaps in new combinations at runtime. The hook is generic and reads the
// parsed specs live, so this only has to re-parse them.
func (m *Manager) Rebind(toggle, cancel string) {
	m.mu.Lock()
	m.wantToggle, m.wantCancel = toggle, cancel
	m.mu.Unlock()
	m.reparse()
}

// Status reports both hotkeys' state; supported is always true on Windows.
func (m *Manager) Status() (toggle, cancel BindInfo, supported bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stToggle, m.stCancel, true
}

// reparse turns the configured specs into modifiers + a virtual-key code the
// hook can match, and records each one's status for the UI.
func (m *Manager) reparse() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tglOK, m.tglMods, m.tglVK, m.stToggle = parseInto(m.wantToggle, m.log, "toggle")
	m.cxlOK, m.cxlMods, m.cxlVK, m.stCancel = parseInto(m.wantCancel, m.log, "cancel")
}

func parseInto(spec string, log *slog.Logger, which string) (ok bool, mods, vk uint32, info BindInfo) {
	spec = strings.TrimSpace(strings.ToLower(spec))
	if spec == "" {
		return false, 0, 0, BindInfo{}
	}
	mods, vk, err := parseSpec(spec)
	if err != nil {
		log.Warn("invalid hotkey", "which", which, "spec", spec, "err", err)
		return false, 0, 0, BindInfo{Spec: spec, ErrCode: "invalid"}
	}
	return true, mods, vk, BindInfo{Spec: spec, Registered: true}
}

// hookProc is the WH_KEYBOARD_LL callback. It must be fast: it only classifies
// the key and hands real work to the worker via a channel.
func hookProc(nCode uintptr, wParam uintptr, lParam uintptr) uintptr {
	if int32(nCode) == hcAction {
		if m := active; m != nil {
			ks := (*kbdllHookStruct)(unsafe.Pointer(lParam))
			switch wParam {
			case wmKeyDown, wmSysKeyDown:
				if m.handleKey(ks.VkCode, true, false) {
					return 1 // swallow: don't let the key reach the focused app
				}
			case wmKeyUp, wmSysKeyUp:
				if m.handleKey(ks.VkCode, false, true) {
					return 1
				}
			}
		}
	}
	ret, _, _ := procCallNextHookEx.Call(0, nCode, wParam, lParam)
	return ret
}

// handleKey decides whether a key event is one of our combinations, emits the
// matching event, and reports whether to swallow the key.
func (m *Manager) handleKey(vk uint32, down, up bool) bool {
	m.mu.Lock()
	tglOK, tglVK, tglMods := m.tglOK, m.tglVK, m.tglMods
	cxlOK, cxlVK, cxlMods := m.cxlOK, m.cxlVK, m.cxlMods
	m.mu.Unlock()

	if cxlOK && vk == cxlVK && down && modsMatch(cxlMods) {
		m.send(evCancel)
		return true
	}
	if tglOK && vk == tglVK {
		if down {
			if m.toggleHeld {
				return true // OS key-repeat while held: swallow, but do nothing
			}
			if modsMatch(tglMods) {
				m.toggleHeld = true
				m.send(evToggleDown)
				return true
			}
			return false // the key pressed without our modifiers is a normal key
		}
		if up && m.toggleHeld {
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

// worker turns hook events into daemon actions. It is the only place the daemon
// is called, off the hook thread, so a slow Start/Stop can't stall the hook.
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

// modsMatch reports whether exactly the required modifiers are down right now —
// no fewer (so a bare key still types) and no more (so Ctrl+Space doesn't fire
// on Ctrl+Shift+Space), mirroring RegisterHotKey's exact-match behaviour.
func modsMatch(mods uint32) bool {
	if (mods&modControl != 0) != keyDown(vkControl) {
		return false
	}
	if (mods&modAlt != 0) != keyDown(vkMenu) {
		return false
	}
	if (mods&modShift != 0) != keyDown(vkShift) {
		return false
	}
	if (mods&modWin != 0) != (keyDown(vkLWin) || keyDown(vkRWin)) {
		return false
	}
	return true
}

func keyDown(vk uint32) bool {
	r, _, _ := procGetAsyncKeyState.Call(uintptr(vk))
	return r&0x8000 != 0
}

// parseSpec turns "ctrl+alt+space" into modifiers + a virtual key code.
func parseSpec(spec string) (mods uint32, vk uint32, err error) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(spec)), "+")
	if len(parts) == 0 {
		return 0, 0, fmt.Errorf("empty hotkey")
	}
	key := parts[len(parts)-1]
	for _, p := range parts[:len(parts)-1] {
		switch strings.TrimSpace(p) {
		case "ctrl", "control":
			mods |= modControl
		case "alt":
			mods |= modAlt
		case "shift":
			mods |= modShift
		case "win", "super", "meta":
			mods |= modWin
		default:
			return 0, 0, fmt.Errorf("unknown modifier %q", p)
		}
	}
	switch {
	case key == "space":
		vk = 0x20
	case key == "enter", key == "return":
		vk = 0x0D
	case key == "tab":
		vk = 0x09
	case key == "esc", key == "escape":
		vk = 0x1B
	case key == "backspace":
		vk = 0x08
	case len(key) == 1 && key[0] >= 'a' && key[0] <= 'z':
		vk = uint32(key[0] - 'a' + 'A')
	case len(key) == 1 && key[0] >= '0' && key[0] <= '9':
		vk = uint32(key[0])
	case len(key) >= 2 && key[0] == 'f':
		var n int
		if _, err := fmt.Sscanf(key, "f%d", &n); err != nil || n < 1 || n > 24 {
			return 0, 0, fmt.Errorf("unknown key %q", key)
		}
		vk = uint32(0x70 + n - 1) // VK_F1..
	default:
		return 0, 0, fmt.Errorf("unknown key %q", key)
	}
	if mods == 0 {
		return 0, 0, fmt.Errorf("hotkey needs at least one modifier")
	}
	return mods, vk, nil
}

// Configure is unsupported on Windows: the hotkey is Vito's own setting there,
// edited in Vito's settings page rather than by the desktop.
func (m *Manager) Configure() error { return fmt.Errorf("not supported on Windows") }

// CanConfigure is false on Windows: the hotkey is Vito's own setting, edited in
// Vito's settings page rather than by the desktop.
func (m *Manager) CanConfigure() bool { return false }
