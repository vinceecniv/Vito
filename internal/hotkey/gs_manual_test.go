//go:build linux

package hotkey

import (
	"os"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"vito/internal/xdgportal"
)

// TestGlobalShortcutsSession checks whether this desktop lets Vito create a
// GlobalShortcuts session at all — the question that decides the portal hotkey.
// Interactive (it may prompt), so it is env-gated.
//
//	VITO_GS_TEST=1 go test ./internal/hotkey/ -run TestGlobalShortcutsSession -v
func TestGlobalShortcutsSession(t *testing.T) {
	if os.Getenv("VITO_GS_TEST") == "" {
		t.Skip("interactive: set VITO_GS_TEST=1 to run")
	}
	conn, err := xdgportal.Bus()
	if err != nil {
		t.Fatalf("bus: %v", err)
	}
	ver, ok := xdgportal.Version(conn, ifaceGS)
	t.Logf("GlobalShortcuts advertised=%v version=%d", ok, ver)

	obj := conn.Object(xdgportal.Dest, dbus.ObjectPath(xdgportal.Path))
	res, err := xdgportal.Request(conn, 20*time.Second, func(opts map[string]dbus.Variant) *dbus.Call {
		opts["session_handle_token"] = dbus.MakeVariant(xdgportal.NewToken())
		return obj.Call(ifaceGS+".CreateSession", 0, opts)
	})
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	session, _ := xdgportal.SessionHandle(res)
	t.Logf("SESSION OK: %s", session)

	shortcuts := []shortcut{
		{ID: shortcutToggle, Props: props("Start / stop dictation", "ctrl+alt+space")},
		{ID: shortcutCancel, Props: props("Cancel the recording", "ctrl+alt+c")},
	}
	res, err = xdgportal.Request(conn, 2*time.Minute, func(opts map[string]dbus.Variant) *dbus.Call {
		return obj.Call(ifaceGS+".BindShortcuts", 0, session, shortcuts, "", opts)
	})
	if err != nil {
		t.Fatalf("BindShortcuts failed: %v", err)
	}
	t.Logf("BIND OK, raw result: %#v", res["shortcuts"].Value())
}
