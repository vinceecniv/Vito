//go:build linux

package inject

import (
	"fmt"
	"sync"

	"github.com/godbus/dbus/v5"

	"vito/internal/config"
)

// The XDG RemoteDesktop portal backend. See docs/linux-portals.md for why this
// exists: it is the Wayland-native way to press a key in another application —
// no ydotoold, no /dev/uinput rule — and the only one a Flatpak can use.
//
// Injection goes over plain D-Bus (NotifyKeyboardKeycode / NotifyKeyboardKeysym)
// rather than libei, so this stays pure Go with no cgo.

const (
	portalDest = "org.freedesktop.portal.Desktop"
	portalPath = "/org/freedesktop/portal/desktop"
	ifaceRD    = "org.freedesktop.portal.RemoteDesktop"
)

// portalInjectionReady flips to true once the session + notify calls below are
// implemented (phase 2). Until then "auto" keeps choosing ydotool, so nothing
// changes for existing installs — the portal is detected but not used.
const portalInjectionReady = false

var portalProbe struct {
	sync.Once
	present bool
	version uint32
}

// portalPresent reports whether the RemoteDesktop portal is on the session bus,
// and caches the answer: the portal cannot appear and disappear mid-session in
// any way that matters here.
func portalPresent() bool {
	portalProbe.Do(func() {
		conn, err := dbus.SessionBus() // shared connection; do not close
		if err != nil {
			return
		}
		v, err := conn.Object(portalDest, dbus.ObjectPath(portalPath)).
			GetProperty(ifaceRD + ".version")
		if err != nil {
			return
		}
		if ver, ok := v.Value().(uint32); ok {
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

// portalUsable says whether "auto" should pick the portal.
func portalUsable() bool { return portalInjectionReady && portalPresent() }

func portalInject(cfg config.Injection, mode Mode, text string) error {
	return fmt.Errorf("portal injection not implemented yet (see docs/linux-portals.md)")
}

func portalPressEnter() error {
	return fmt.Errorf("portal injection not implemented yet (see docs/linux-portals.md)")
}
