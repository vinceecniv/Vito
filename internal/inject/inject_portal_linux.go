//go:build linux

package inject

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/godbus/dbus/v5"

	"vito/internal/config"
	"vito/internal/xdgportal"
)

// The XDG RemoteDesktop portal backend. See docs/linux-portals.md for why this
// exists: it is the Wayland-native way to press a key in another application —
// no ydotoold, no /dev/uinput rule — and it works inside a Flatpak.
//
// Injection goes over plain D-Bus (NotifyKeyboardKeycode / NotifyKeyboardKeysym)
// rather than libei, so this stays pure Go with no cgo.

const (
	ifaceRD       = "org.freedesktop.portal.RemoteDesktop"
	deviceTypeKbd = uint32(1) // RemoteDesktop device bitmask: 1 = keyboard

	// persist mode 2 = "until explicitly revoked": the whole point, so the user
	// grants remote control once instead of once per dictation.
	persistPersistent = uint32(2)

	// evdev keycodes — the same numbers the ydotool backend uses.
	keyLeftCtrl = int32(29)
	keyV        = int32(47)
	keyEnter    = int32(28)

	keyReleased = uint32(0)
	keyPressed  = uint32(1)
)

// errPortalUnavailable means the session could not be established (no portal,
// user denied, timed out). It is only ever returned *before* any key is sent,
// so the caller may safely fall back to another backend.
var errPortalUnavailable = errors.New("remote desktop portal unavailable")

var portalProbe struct {
	sync.Once
	present bool
	version uint32
}

// portalPresent reports whether the RemoteDesktop portal is on the session bus.
func portalPresent() bool {
	portalProbe.Do(func() {
		conn, err := xdgportal.Bus()
		if err != nil {
			return
		}
		if ver, ok := xdgportal.Version(conn, ifaceRD); ok {
			portalProbe.present, portalProbe.version = true, ver
		}
	})
	return portalProbe.present
}

// PortalVersion reports the RemoteDesktop portal version (0 when absent), for
// the settings page's Linux diagnostics.
func PortalVersion() uint32 {
	portalPresent()
	return portalProbe.version
}

// portalFailed latches once a session attempt has failed, so "auto" stops
// choosing the portal for the rest of the run.
//
// This matters more than it looks. xdg-desktop-portal *always* advertises
// RemoteDesktop — the version property says 2 even when no backend implements
// org.freedesktop.impl.portal.RemoteDesktop behind it (niri here: the GNOME
// backend delegates to Mutter, which isn't running). The interface being present
// is therefore not proof that it works; only a session attempt is. And if the
// user simply refuses the permission, re-asking on every dictation would be
// worse than falling back quietly.
var portalFailed atomic.Bool

// PortalWorking reports whether the portal looks usable in this run.
func PortalWorking() bool { return portalUsable() }

func portalUsable() bool { return portalPresent() && !portalFailed.Load() }

// --- the session ---------------------------------------------------------
//
// Both the portal session and the permission live longer than one dictation:
// the session is created once and held for the life of the daemon, and the
// restore token persists the grant across restarts.

var portal struct {
	mu      sync.Mutex
	conn    *dbus.Conn
	session dbus.ObjectPath
}

func ensureSession() (*dbus.Conn, dbus.ObjectPath, error) {
	portal.mu.Lock()
	defer portal.mu.Unlock()
	if portal.session != "" {
		return portal.conn, portal.session, nil
	}
	if !portalPresent() {
		return nil, "", errPortalUnavailable
	}
	conn, err := xdgportal.Bus()
	if err != nil {
		portalFailed.Store(true)
		return nil, "", fmt.Errorf("%w: session bus: %v", errPortalUnavailable, err)
	}
	session, err := startSession(conn)
	if err != nil {
		// Don't try again this run: either nothing implements the portal, or the
		// user said no. Both are answers, not transient errors.
		portalFailed.Store(true)
		return nil, "", err
	}
	portal.conn, portal.session = conn, session
	return conn, session, nil
}

// dropSession forgets the session so the next injection builds a fresh one —
// used when the compositor has closed it under us.
func dropSession() {
	portal.mu.Lock()
	portal.session = ""
	portal.mu.Unlock()
}

// startSession runs the three-step portal handshake: CreateSession, then
// SelectDevices (asking for a keyboard and for the grant to be remembered),
// then Start — which is where the user sees the permission dialog.
func startSession(conn *dbus.Conn) (dbus.ObjectPath, error) {
	obj := conn.Object(xdgportal.Dest, dbus.ObjectPath(xdgportal.Path))

	sessionToken := xdgportal.NewToken()
	res, err := xdgportal.Request(conn, 30*time.Second, func(opts map[string]dbus.Variant) *dbus.Call {
		opts["session_handle_token"] = dbus.MakeVariant(sessionToken)
		return obj.Call(ifaceRD+".CreateSession", 0, opts)
	})
	if err != nil {
		return "", fmt.Errorf("%w: CreateSession: %v", errPortalUnavailable, err)
	}
	session, ok := xdgportal.SessionHandle(res)
	if !ok {
		return "", fmt.Errorf("%w: CreateSession returned no session handle", errPortalUnavailable)
	}

	// Ask for a keyboard, and for the permission to be remembered. A saved
	// restore token turns the next Start into a no-prompt grant.
	_, err = xdgportal.Request(conn, 30*time.Second, func(opts map[string]dbus.Variant) *dbus.Call {
		opts["types"] = dbus.MakeVariant(deviceTypeKbd)
		opts["persist_mode"] = dbus.MakeVariant(persistPersistent)
		if tok := loadRestoreToken(); tok != "" {
			opts["restore_token"] = dbus.MakeVariant(tok)
		}
		return obj.Call(ifaceRD+".SelectDevices", 0, session, opts)
	})
	if err != nil {
		return "", fmt.Errorf("%w: SelectDevices: %v", errPortalUnavailable, err)
	}

	// Start shows the dialog on first run. Give the user time to answer it.
	res, err = xdgportal.Request(conn, 3*time.Minute, func(opts map[string]dbus.Variant) *dbus.Call {
		return obj.Call(ifaceRD+".Start", 0, session, "", opts)
	})
	if err != nil {
		return "", fmt.Errorf("%w: Start: %v", errPortalUnavailable, err)
	}
	// Keep the token the portal hands back, so the grant survives a restart.
	if v, ok := res["restore_token"]; ok {
		if tok, ok := v.Value().(string); ok && tok != "" {
			saveRestoreToken(tok)
		}
	}
	return session, nil
}

// --- the remembered grant ------------------------------------------------

func restoreTokenPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "vito", "portal-restore-token")
}

func loadRestoreToken() string {
	p := restoreTokenPath()
	if p == "" {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func saveRestoreToken(tok string) {
	p := restoreTokenPath()
	if p == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	_ = os.WriteFile(p, []byte(tok), 0o600) // best effort: worst case is one more prompt
}

// --- injection -----------------------------------------------------------

func portalInject(cfg config.Injection, mode Mode, text string) error {
	conn, session, err := ensureSession()
	if err != nil {
		return err // errPortalUnavailable — nothing was sent, safe to fall back
	}

	switch mode {
	case ModeType:
		return typeKeysyms(conn, session, text)

	case ModePaste:
		prev, prevOK := readClipboardText()
		if err := copyText(text); err != nil {
			return err
		}
		time.Sleep(time.Duration(cfg.PasteDelayMS) * time.Millisecond)
		if err := pressCtrlV(conn, session); err != nil {
			return err
		}
		if cfg.RestoreClipboard && prevOK {
			time.Sleep(time.Duration(cfg.RestoreDelayMS) * time.Millisecond)
			_ = copyText(prev) // best effort
		}
		return nil
	}
	return fmt.Errorf("unknown injection mode %q", mode)
}

func portalPressEnter() error {
	conn, session, err := ensureSession()
	if err != nil {
		return err
	}
	time.Sleep(40 * time.Millisecond) // let the paste settle before submitting
	return tapKeycode(conn, session, keyEnter)
}

func pressCtrlV(conn *dbus.Conn, session dbus.ObjectPath) error {
	for _, ev := range []struct {
		code  int32
		state uint32
	}{
		{keyLeftCtrl, keyPressed},
		{keyV, keyPressed},
		{keyV, keyReleased},
		{keyLeftCtrl, keyReleased},
	} {
		if err := notifyKeycode(conn, session, ev.code, ev.state); err != nil {
			return err
		}
	}
	return nil
}

func tapKeycode(conn *dbus.Conn, session dbus.ObjectPath, code int32) error {
	if err := notifyKeycode(conn, session, code, keyPressed); err != nil {
		return err
	}
	return notifyKeycode(conn, session, code, keyReleased)
}

func notifyKeycode(conn *dbus.Conn, session dbus.ObjectPath, code int32, state uint32) error {
	call := conn.Object(xdgportal.Dest, dbus.ObjectPath(xdgportal.Path)).
		Call(ifaceRD+".NotifyKeyboardKeycode", 0, session, map[string]dbus.Variant{}, code, state)
	if call.Err != nil {
		// The compositor may have closed the session; force a rebuild next time.
		dropSession()
		return fmt.Errorf("portal keycode %d: %w", code, call.Err)
	}
	return nil
}

// typeKeysyms types text character by character. Unicode code points map to X11
// keysyms with the 0x01000000 offset, which is how you say "this character"
// without caring about the user's keyboard layout.
func typeKeysyms(conn *dbus.Conn, session dbus.ObjectPath, text string) error {
	obj := conn.Object(xdgportal.Dest, dbus.ObjectPath(xdgportal.Path))
	for _, r := range text {
		keysym := int32(r)
		switch {
		case r == '\n':
			keysym = 0xff0d // XK_Return
		case r > 0x7f:
			keysym = 0x01000000 + int32(r)
		}
		for _, state := range []uint32{keyPressed, keyReleased} {
			call := obj.Call(ifaceRD+".NotifyKeyboardKeysym", 0, session, map[string]dbus.Variant{}, keysym, state)
			if call.Err != nil {
				dropSession()
				return fmt.Errorf("portal keysym %q: %w", r, call.Err)
			}
		}
	}
	return nil
}
