//go:build windows

package update

import (
	"fmt"
	"os"
	"os/exec"

	"golang.org/x/sys/windows"
)

// Apply launches a downloaded installer and returns immediately.
//
// It has to outlive us: the installer's first act is to ask the running Vito to
// quit so it can replace the executable, so the process is started detached
// rather than as a child that would die with its parent. The caller shuts the
// daemon down right after, and the installer starts the new one when it is done.
func Apply(path string) error {
	if _, err := os.Stat(path); err != nil {
		return err
	}
	cmd := exec.Command(path, "/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART")
	cmd.SysProcAttr = &windows.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start installer: %w", err)
	}
	return cmd.Process.Release()
}

// CanApply reports whether Vito can install an update itself on this platform.
func CanApply() bool { return true }
