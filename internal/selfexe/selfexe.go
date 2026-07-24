// Package selfexe resolves a stable filesystem path to the running Vito binary,
// suitable for baking into persistent launchers — the XDG autostart entry and
// the vito:// URL handler that relaunches the daemon for the PWA.
//
// The subtlety is AppImage. There, os.Executable() points inside the temporary
// squashfs mount (/tmp/.mount_XXXXXX/usr/bin/vito), which is unmounted when the
// process exits — so anything persisted with that path breaks on the next boot.
// The AppImage runtime exports $APPIMAGE with the real, stable location of the
// .AppImage file the user placed; when it is set we prefer it. Off AppImage
// (plain binary, Windows installer) the variable is absent and we fall back to
// os.Executable() with symlinks resolved, as before.
package selfexe

import (
	"os"
	"path/filepath"
)

// Path returns a stable path to the running program, preferring $APPIMAGE.
func Path() (string, error) {
	if ai := os.Getenv("APPIMAGE"); ai != "" {
		return ai, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe, nil
}
