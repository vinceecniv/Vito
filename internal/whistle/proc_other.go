//go:build !windows

package whistle

import "os/exec"

func hideWindow(*exec.Cmd) {}
