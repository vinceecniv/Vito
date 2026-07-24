// Package autostart enables or disables launching `vito serve` when the user
// logs in, using each OS's native mechanism:
//
//   - Windows: the HKCU\...\CurrentVersion\Run registry value
//   - Linux:   an XDG autostart .desktop entry (~/.config/autostart/vito.desktop)
//   - other:   unsupported (Set returns an error, Supported reports false)
//
// The command registered is the current executable followed by `serve`, so an
// installed binary re-launches itself as the daemon at login.
package autostart

import "vito/internal/selfexe"

const appName = "Vito"

// executablePath returns a stable path to the running binary for the autostart
// entry. It defers to selfexe, which prefers $APPIMAGE so an AppImage install
// points the login entry at the real .AppImage file rather than the throwaway
// squashfs mount that vanishes on the next boot.
func executablePath() (string, error) {
	return selfexe.Path()
}
