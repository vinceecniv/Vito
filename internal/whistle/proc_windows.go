package whistle

import (
	"os/exec"
	"syscall"
)

// hideWindow keeps the console engine from flashing a window per clip.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
