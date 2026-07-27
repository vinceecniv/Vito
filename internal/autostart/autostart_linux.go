//go:build linux

package autostart

import (
	"fmt"
	"os"
	"path/filepath"
)

// Supported reports whether autostart can be configured on this OS.
func Supported() bool { return true }

// desktopPath is ~/.config/autostart/vito.desktop.
func desktopPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "autostart", "vito.desktop"), nil
}

// Enabled reports whether autostart is on: what the Background portal granted
// inside a Flatpak, or whether the XDG autostart entry exists outside one.
func Enabled() (bool, error) {
	if inFlatpak() {
		return portalEnabled()
	}
	p, err := desktopPath()
	if err != nil {
		return false, err
	}
	_, err = os.Stat(p)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

// Set turns autostart on or off.
//
// Inside a Flatpak this goes through the Background portal: the sandbox
// redirects ~/.config, so writing the entry ourselves would put it where no
// desktop looks and the setting would silently do nothing.
//
// Outside a sandbox it writes the XDG autostart .desktop entry directly, which
// is recognised by most desktops; some Wayland compositors (e.g. niri) need
// xdg-desktop-autostart wired up to honour it.
func Set(enable bool) error {
	if inFlatpak() {
		return portalSet(enable)
	}
	p, err := desktopPath()
	if err != nil {
		return err
	}
	if !enable {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	exe, err := executablePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	entry := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=%s
Comment=Voice dictation daemon
Exec="%s" serve
Terminal=false
X-GNOME-Autostart-enabled=true
`, appName, exe)
	return os.WriteFile(p, []byte(entry), 0o644)
}
