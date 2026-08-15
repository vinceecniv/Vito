//go:build linux

package autostart

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/godbus/dbus/v5"

	"vito/internal/xdgportal"
)

// Autostart inside a Flatpak has to go through the Background portal.
//
// The file-based route writes ~/.config/autostart/vito.desktop, but a sandbox
// redirects ~/.config to ~/.var/app/<id>/config — so the entry lands somewhere
// the desktop never reads, and the settings toggle silently does nothing. That
// is worse than not offering it.
//
// RequestBackground asks the desktop to do it instead: the user gets one prompt
// and the desktop owns the autostart entry from then on.

const ifaceBackground = "org.freedesktop.portal.Background"

// inFlatpak is xdgportal's detection under a local name. Outside a sandbox the
// file-based autostart route is kept: it works, it is inspectable, and it costs
// the user no extra permission dialog.
func inFlatpak() bool { return xdgportal.InFlatpak() }

// portalStatePath remembers what the portal last granted. The Background portal
// has no method to query the current state, so the alternative would be to
// re-request (and re-prompt) just to read it back.
func portalStatePath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "vito", "autostart-portal"), nil
}

func portalEnabled() (bool, error) {
	p, err := portalStatePath()
	if err != nil {
		return false, err
	}
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return string(b) == "1", nil
}

// portalSet asks the desktop to enable or disable autostart, and records what
// it actually granted — the user may say no, and the toggle must then reflect
// reality rather than the request.
func portalSet(enable bool) error {
	conn, err := xdgportal.Bus()
	if err != nil {
		return err
	}
	obj := conn.Object(xdgportal.Dest, dbus.ObjectPath(xdgportal.Path))

	res, err := xdgportal.Request(conn, 2*time.Minute, func(opts map[string]dbus.Variant) *dbus.Call {
		opts["reason"] = dbus.MakeVariant("Start Vito at login, so your dictation hotkey works straight away.")
		opts["autostart"] = dbus.MakeVariant(enable)
		// The command the desktop should run. Inside the sandbox this is resolved
		// against the app, so it is the plain command rather than a host path.
		opts["commandline"] = dbus.MakeVariant([]string{"vito", "serve"})
		return obj.Call(ifaceBackground+".RequestBackground", 0, "", opts)
	})
	if err != nil {
		return fmt.Errorf("background portal: %w", err)
	}

	granted := false
	if v, ok := res["autostart"]; ok {
		granted, _ = v.Value().(bool)
	}
	if enable && !granted {
		return fmt.Errorf("the desktop did not grant autostart")
	}

	p, err := portalStatePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	state := "0"
	if granted {
		state = "1"
	}
	return os.WriteFile(p, []byte(state), 0o600)
}
