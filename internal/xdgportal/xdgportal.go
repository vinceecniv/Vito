//go:build linux

// Package xdgportal is a thin helper over the XDG desktop portals.
//
// Every portal method works the same awkward way: it returns immediately with a
// Request object path and delivers the real answer later over a signal. The
// path is derived from the caller's bus name plus a token the caller chooses,
// which is what makes it possible to subscribe *before* issuing the call —
// without that, a fast reply can arrive before anyone is listening.
//
// Request() encapsulates that dance so the callers (text injection via
// RemoteDesktop, hotkeys via GlobalShortcuts) can read as ordinary code.
package xdgportal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	// Dest and Path address the portal frontend.
	Dest = "org.freedesktop.portal.Desktop"
	Path = "/org/freedesktop/portal/desktop"

	ifaceRequest = "org.freedesktop.portal.Request"
)

// ErrCancelled is returned when the user dismissed the portal's dialog, as
// opposed to something going wrong.
var ErrCancelled = errors.New("cancelled by the user")

// Bus returns the shared session bus. Do not close it: portal sessions are tied
// to the connection, so closing it would tear them down.
// InFlatpak reports whether we are running inside a Flatpak sandbox.
//
// It decides more than cosmetics: inside the sandbox ydotool cannot work at all
// (no /dev/uinput, no daemon, and the binary is not shipped), and Flatpak hands
// the app a proxied Wayland socket with the virtual-keyboard protocol filtered
// out. So a sandboxed Vito has exactly one way to press a key — the
// RemoteDesktop portal — and callers need to know that before offering a route
// that cannot exist.
//
// FLATPAK_ID is set for the app's own processes; /.flatpak-info exists for every
// process in the sandbox, including ones spawned without that environment.
func InFlatpak() bool {
	if os.Getenv("FLATPAK_ID") != "" {
		return true
	}
	_, err := os.Stat("/.flatpak-info")
	return err == nil
}

func Bus() (*dbus.Conn, error) { return dbus.SessionBus() }

// Version reports the version of a portal interface, and whether it is present
// at all.
//
// Beware what a "yes" means here: xdg-desktop-portal advertises interfaces its
// backend may not implement, so a version number proves the frontend exists,
// not that the feature works. Only attempting the real call proves that.
func Version(conn *dbus.Conn, iface string) (uint32, bool) {
	v, err := conn.Object(Dest, dbus.ObjectPath(Path)).GetProperty(iface + ".version")
	if err != nil {
		return 0, false
	}
	ver, ok := v.Value().(uint32)
	return ver, ok
}

// Request performs one portal call and waits for its Response signal.
//
// The callback receives the options map with "handle_token" already set, and
// must issue the D-Bus call with it.
func Request(conn *dbus.Conn, timeout time.Duration, call func(map[string]dbus.Variant) *dbus.Call) (map[string]dbus.Variant, error) {
	token := NewToken()
	reqPath := dbus.ObjectPath("/org/freedesktop/portal/desktop/request/" + SenderPart(conn) + "/" + token)

	match := []dbus.MatchOption{
		dbus.WithMatchObjectPath(reqPath),
		dbus.WithMatchInterface(ifaceRequest),
		dbus.WithMatchMember("Response"),
	}
	if err := conn.AddMatchSignal(match...); err != nil {
		return nil, err
	}
	defer conn.RemoveMatchSignal(match...)

	ch := make(chan *dbus.Signal, 4)
	conn.Signal(ch)
	defer conn.RemoveSignal(ch)

	opts := map[string]dbus.Variant{"handle_token": dbus.MakeVariant(token)}
	if c := call(opts); c.Err != nil {
		return nil, c.Err
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for {
		select {
		case sig := <-ch:
			if sig.Path != reqPath || len(sig.Body) < 2 {
				continue
			}
			code, _ := sig.Body[0].(uint32)
			results, _ := sig.Body[1].(map[string]dbus.Variant)
			switch code {
			case 0:
				return results, nil
			case 1:
				return nil, ErrCancelled
			default:
				return nil, fmt.Errorf("portal request ended (code %d)", code)
			}
		case <-ctx.Done():
			return nil, errors.New("timed out waiting for the portal")
		}
	}
}

// SenderPart is the caller's unique bus name in the form the portal uses to
// build request and session paths: ":1.42" becomes "1_42".
func SenderPart(conn *dbus.Conn) string {
	names := conn.Names()
	if len(names) == 0 {
		return ""
	}
	return strings.ReplaceAll(strings.TrimPrefix(names[0], ":"), ".", "_")
}

// SessionHandle digs the session path out of a CreateSession response, which
// portals have been known to type as either an object path or a string.
func SessionHandle(res map[string]dbus.Variant) (dbus.ObjectPath, bool) {
	v, ok := res["session_handle"]
	if !ok {
		return "", false
	}
	switch h := v.Value().(type) {
	case dbus.ObjectPath:
		return h, true
	case string:
		return dbus.ObjectPath(h), true
	}
	return "", false
}

var tokenSeq struct {
	sync.Mutex
	n int
}

// NewToken returns a handle token unique within this process. It only has to be
// unique per connection, so a counter beats randomness: the derived object
// paths stay predictable while debugging.
func NewToken() string {
	tokenSeq.Lock()
	tokenSeq.n++
	n := tokenSeq.n
	tokenSeq.Unlock()
	return fmt.Sprintf("vito%d_%d", os.Getpid(), n)
}
