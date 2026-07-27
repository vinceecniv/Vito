//go:build linux

// Package hotkey registers global hotkeys where the OS allows it.
package hotkey

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"

	"vito/internal/daemon"
	"vito/internal/xdgportal"
)

// On Wayland an application cannot grab a key for itself — and for years that
// meant Vito had to be driven from outside, by binding `pkill -USR2` in the
// compositor. The GlobalShortcuts portal changes that: the app asks for a
// shortcut, the desktop asks the user, and the binding is theirs to keep.
//
// Two things make this worth having beyond convenience. It works from inside a
// Flatpak, and — unlike a signal, which has no notion of release — it reports
// key *up* as well as down, so push-to-talk finally works on Linux.
//
// The SIGUSR1/SIGUSR2 signals stay wired up regardless: they cost nothing, they
// remain the documented route for scripts, and they are the fallback wherever
// this portal is missing.

const (
	ifaceGS = "org.freedesktop.portal.GlobalShortcuts"

	shortcutToggle = "toggle"
	shortcutCancel = "cancel"

	// holdThreshold separates a tap (toggle) from a hold (push-to-talk). Same
	// value as the Windows hook, so the two platforms feel identical.
	holdThreshold = 350 * time.Millisecond

	// configureMinVersion is the GlobalShortcuts version that introduced
	// ConfigureShortcuts.
	configureMinVersion = 2

	// repeatGap tells a key-repeat Activated from a genuinely new press. Repeats
	// arrive ~30ms apart; this only has to catch the case where a Deactivated
	// never arrives, so it can afford to be generous.
	repeatGap = time.Second
)

// BindInfo mirrors the Windows type so callers are platform-agnostic.
type BindInfo struct {
	Spec       string
	Registered bool
	ErrCode    string
}

type event int

const (
	evToggleDown event = iota
	evToggleUp
	evCancel
)

// Manager holds a GlobalShortcuts session for the life of the daemon: the
// bindings are only live while the session is.
type Manager struct {
	d   *daemon.Daemon
	log *slog.Logger

	events chan event

	mu        sync.Mutex
	conn      *dbus.Conn
	session   dbus.ObjectPath
	supported bool
	// gsVersion is the GlobalShortcuts portal version. ConfigureShortcuts only
	// exists from version 2 — and the frontend lists the method regardless of
	// what the backend implements, so the version is the only honest signal.
	gsVersion uint32
	toggle    BindInfo
	cancel    BindInfo
	started   bool
}

func New(d *daemon.Daemon, log *slog.Logger) *Manager {
	return &Manager{d: d, log: log, events: make(chan event, 8)}
}

// Start asks the desktop to bind the two shortcuts. It never blocks the caller:
// the portal may show a dialog, and Vito must come up regardless of whether the
// user answers it.
func (m *Manager) Start(toggle, cancel string) {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return
	}
	m.started = true
	m.mu.Unlock()

	go m.worker()
	go func() {
		if err := m.bind(toggle, cancel); err != nil {
			// Not an error the user needs to act on: the signal route still
			// works, and plenty of desktops don't implement this portal.
			hint := ""
			if strings.Contains(err.Error(), "app id") {
				// The portal identifies host apps by their systemd scope, so a
				// binary started from a shell has no identity to bind against.
				// Launchers that follow the XDG convention (app-<id>-<rnd>.scope)
				// give it one; inside a Flatpak it is automatic.
				hint = "start Vito from its .desktop entry, or inside a systemd app scope, to give it an app id"
			}
			m.log.Info("global shortcuts portal unavailable, using signals", "err", err, "hint", hint)
			return
		}
	}()
}

// Rebind is a no-op: with the portal, the binding belongs to the desktop, not
// to Vito. The user changes it in their own shortcut settings — see Configure,
// which is how Vito offers that without pretending to own the key.
func (m *Manager) Rebind(toggle, cancel string) {}

// CanConfigure reports whether this desktop can open a shortcut editor for us.
// GNOME's portal is still at version 1 and cannot, so the button is hidden there
// rather than offered and then failing.
func (m *Manager) CanConfigure() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.supported && m.gsVersion >= configureMinVersion
}

// Configure asks the desktop to open its own shortcut editor for Vito.
//
// An application cannot simply take a key on Wayland, and shouldn't: the user
// decides, and they need to see conflicts with everything else they have bound.
// ConfigureShortcuts is the portal's answer — Vito's settings page gets a button,
// and the desktop shows its familiar dialog rather than Vito inventing its own
// key-capture UI.
func (m *Manager) Configure() error {
	m.mu.Lock()
	conn, session, ver := m.conn, m.session, m.gsVersion
	m.mu.Unlock()
	if conn == nil || session == "" {
		return fmt.Errorf("no global shortcuts session")
	}
	if ver < configureMinVersion {
		return fmt.Errorf("this desktop's shortcuts portal has no editor to open")
	}
	obj := conn.Object(xdgportal.Dest, dbus.ObjectPath(xdgportal.Path))
	// Fire and forget: the dialog is the desktop's, and it may sit open for as
	// long as the user likes.
	if call := obj.Call(ifaceGS+".ConfigureShortcuts", 0, session, "", map[string]dbus.Variant{}); call.Err != nil {
		return call.Err
	}
	return nil
}

func (m *Manager) Status() (toggle, cancel BindInfo, supported bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.toggle, m.cancel, m.supported
}

// shortcut is one entry of the portal's a(sa{sv}) shortcuts argument.
type shortcut struct {
	ID    string
	Props map[string]dbus.Variant
}

func (m *Manager) bind(toggleSpec, cancelSpec string) error {
	conn, err := xdgportal.Bus()
	if err != nil {
		return err
	}
	ver, ok := xdgportal.Version(conn, ifaceGS)
	if !ok {
		return fmt.Errorf("no GlobalShortcuts portal")
	}
	m.mu.Lock()
	m.gsVersion = ver
	m.mu.Unlock()
	obj := conn.Object(xdgportal.Dest, dbus.ObjectPath(xdgportal.Path))

	sessionToken := xdgportal.NewToken()
	res, err := xdgportal.Request(conn, 30*time.Second, func(opts map[string]dbus.Variant) *dbus.Call {
		opts["session_handle_token"] = dbus.MakeVariant(sessionToken)
		return obj.Call(ifaceGS+".CreateSession", 0, opts)
	})
	if err != nil {
		return fmt.Errorf("CreateSession: %w", err)
	}
	session, ok := xdgportal.SessionHandle(res)
	if !ok {
		return fmt.Errorf("CreateSession returned no session handle")
	}

	// Listen before binding: the portal can report the binding immediately.
	if err := m.listen(conn, session); err != nil {
		return err
	}
	m.mu.Lock()
	m.conn, m.session = conn, session
	m.mu.Unlock()

	shortcuts := []shortcut{
		{ID: shortcutToggle, Props: props("Start / stop dictation", toggleSpec)},
		{ID: shortcutCancel, Props: props("Cancel the recording", cancelSpec)},
	}
	// The user may be shown a dialog to confirm or change the keys.
	res, err = xdgportal.Request(conn, 3*time.Minute, func(opts map[string]dbus.Variant) *dbus.Call {
		return obj.Call(ifaceGS+".BindShortcuts", 0, session, shortcuts, "", opts)
	})
	if err != nil {
		return fmt.Errorf("BindShortcuts: %w", err)
	}
	m.recordBindings(res)
	return nil
}

func props(description, preferred string) map[string]dbus.Variant {
	p := map[string]dbus.Variant{"description": dbus.MakeVariant(description)}
	if trigger := portalTrigger(preferred); trigger != "" {
		p["preferred_trigger"] = dbus.MakeVariant(trigger)
	}
	return p
}

// recordBindings stores what the desktop actually bound, which may differ from
// what Vito asked for — the user has the final say.
func (m *Manager) recordBindings(res map[string]dbus.Variant) {
	v, ok := res["shortcuts"]
	if !ok {
		return
	}
	list, ok := v.Value().([][]interface{})
	if !ok {
		// godbus decodes a(sa{sv}) into []struct-shaped slices; be forgiving
		// about the exact shape and simply mark support without the trigger text.
		m.mu.Lock()
		m.supported = true
		m.mu.Unlock()
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.supported = true
	for _, entry := range list {
		if len(entry) < 2 {
			continue
		}
		id, _ := entry[0].(string)
		trigger := ""
		if p, ok := entry[1].(map[string]dbus.Variant); ok {
			if t, ok := p["trigger_description"]; ok {
				trigger, _ = t.Value().(string)
			}
		}
		info := BindInfo{Spec: trigger, Registered: true}
		switch id {
		case shortcutToggle:
			m.toggle = info
		case shortcutCancel:
			m.cancel = info
		}
	}
	m.log.Info("global shortcuts bound", "toggle", m.toggle.Spec, "cancel", m.cancel.Spec)
}

// listen subscribes to Activated/Deactivated and turns them into events. Unlike
// a signal-based hotkey, Deactivated tells us the key was released — which is
// what makes push-to-talk possible.
func (m *Manager) listen(conn *dbus.Conn, session dbus.ObjectPath) error {
	match := []dbus.MatchOption{dbus.WithMatchInterface(ifaceGS)}
	if err := conn.AddMatchSignal(match...); err != nil {
		return err
	}
	ch := make(chan *dbus.Signal, 16)
	conn.Signal(ch)

	go func() {
		for sig := range ch {
			if len(sig.Body) < 2 {
				continue
			}
			if path, ok := sig.Body[0].(dbus.ObjectPath); !ok || path != session {
				continue
			}
			id, _ := sig.Body[1].(string)
			m.log.Debug("global shortcut signal", "signal", sig.Name, "id", id)
			switch {
			case strings.HasSuffix(sig.Name, ".Activated") && id == shortcutToggle:
				m.events <- evToggleDown
			case strings.HasSuffix(sig.Name, ".Deactivated") && id == shortcutToggle:
				m.events <- evToggleUp
			case strings.HasSuffix(sig.Name, ".Activated") && id == shortcutCancel:
				m.events <- evCancel
			}
		}
	}()
	return nil
}

// worker turns shortcut events into daemon actions, off the D-Bus signal
// goroutine so a slow Start/Stop can't stall signal delivery.
//
// Tap vs hold, identical to the Windows hook: a press starts recording and arms
// a hold; the release stops it only if the key was held past the threshold. A
// quick tap therefore leaves recording running, and the next tap stops it.
func (m *Manager) worker() {
	var armed, held bool
	var downAt, lastDown time.Time
	for ev := range m.events {
		ptt := m.d.Config().PushToTalkEnabled()
		switch ev {
		case evToggleDown:
			// The portal repeats Activated for as long as the key is held —
			// GNOME sends one roughly every 30ms — and only reports Deactivated
			// on release. Without this guard a hold reads as a stream of taps,
			// which starts and stops the recording over and over.
			now := time.Now()
			if held && now.Sub(lastDown) < repeatGap {
				lastDown = now
				continue
			}
			held, lastDown = true, now
			if !ptt {
				if _, err := m.d.Toggle(); err != nil {
					m.log.Warn("hotkey toggle rejected", "err", err)
				}
				continue
			}
			downAt = now
			if m.d.Status().State == daemon.StateRecording {
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
			held = false
			if ptt && armed && time.Since(downAt) >= holdThreshold {
				if err := m.d.Stop(); err != nil {
					m.log.Debug("hotkey release stop rejected", "err", err)
				}
			}
			armed = false
		case evCancel:
			if err := m.d.Cancel(); err != nil {
				m.log.Debug("hotkey cancel rejected", "err", err)
			}
		}
	}
}

// portalTrigger converts Vito's hotkey spec ("ctrl+alt+space") into the portal's
// shortcut syntax ("CTRL+ALT+space"). It is only a *preferred* trigger — the
// desktop decides — so an unmappable spec is better dropped than forced.
func portalTrigger(spec string) string {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(spec)), "+")
	var out []string
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if i == len(parts)-1 { // the key itself
			out = append(out, keyName(p))
			continue
		}
		switch p {
		case "ctrl", "control":
			out = append(out, "CTRL")
		case "alt":
			out = append(out, "ALT")
		case "shift":
			out = append(out, "SHIFT")
		case "win", "super", "logo", "meta", "cmd":
			out = append(out, "LOGO")
		default:
			return "" // unknown modifier: let the desktop choose entirely
		}
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, "+")
}

// keyName maps a key to its XKB keysym name. Single characters and the common
// named keys pass through; F-keys need their capital.
func keyName(k string) string {
	if len(k) == 2 && k[0] == 'f' && k[1] >= '1' && k[1] <= '9' {
		return strings.ToUpper(k)
	}
	if len(k) == 3 && k[0] == 'f' && k[1] == '1' && k[2] >= '0' && k[2] <= '2' {
		return strings.ToUpper(k)
	}
	return k
}
