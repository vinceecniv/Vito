//go:build !windows

package cloudsync

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strings"
)

// PickFolder opens the system's folder picker and returns the chosen path, or
// "" when the user cancels: Finder's on macOS; on Linux the desktop's own
// FileChooser portal (GNOME, KDE, and inside a Flatpak), else zenity or
// kdialog. ErrNoPicker when there is none; the settings page then takes a
// typed path.
func PickFolder(ctx context.Context, title, start string) (string, error) {
	if runtime.GOOS == "darwin" {
		out, err := exec.CommandContext(ctx, "osascript", "-e",
			`POSIX path of (choose folder with prompt "`+strings.ReplaceAll(title, `"`, `'`)+`")`).Output()
		return trimmed(out, err)
	}
	if p, err := portalPickFolder(title, start); !errors.Is(err, errNoPortal) {
		return p, err
	}
	switch {
	case hasCmd("zenity"):
		args := []string{"--file-selection", "--directory", "--title=" + title}
		if start != "" {
			args = append(args, "--filename="+start+"/")
		}
		return trimmed(exec.CommandContext(ctx, "zenity", args...).Output())
	case hasCmd("kdialog"):
		if start == "" {
			start = "~"
		}
		return trimmed(exec.CommandContext(ctx, "kdialog", "--title", title, "--getexistingdirectory", start).Output())
	}
	return "", ErrNoPicker
}

// errNoPortal means the FileChooser portal isn't there to ask.
var errNoPortal = errors.New("no FileChooser portal")

func trimmed(out []byte, err error) (string, error) {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return "", nil // cancelled: the pickers exit non-zero
	}
	if err != nil {
		return "", err
	}
	return strings.TrimRight(strings.TrimSpace(string(out)), "/"), nil
}

func hasCmd(name string) bool { _, err := exec.LookPath(name); return err == nil }
