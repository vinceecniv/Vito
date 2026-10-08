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
// "" when the user cancels: Finder's on macOS, zenity or kdialog on Linux.
func PickFolder(ctx context.Context, title, start string) (string, error) {
	var cmd *exec.Cmd
	switch {
	case runtime.GOOS == "darwin":
		cmd = exec.CommandContext(ctx, "osascript", "-e",
			`POSIX path of (choose folder with prompt "`+strings.ReplaceAll(title, `"`, `'`)+`")`)
	case hasCmd("zenity"):
		args := []string{"--file-selection", "--directory", "--title=" + title}
		if start != "" {
			args = append(args, "--filename="+start+"/")
		}
		cmd = exec.CommandContext(ctx, "zenity", args...)
	case hasCmd("kdialog"):
		if start == "" {
			start = "~"
		}
		cmd = exec.CommandContext(ctx, "kdialog", "--title", title, "--getexistingdirectory", start)
	default:
		return "", ErrNoPicker
	}
	out, err := cmd.Output()
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
