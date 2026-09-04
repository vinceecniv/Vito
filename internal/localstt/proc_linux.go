package localstt

import (
	"os/exec"
	"syscall"
)

// prepare asks the kernel to kill the server the moment Vito's thread that
// started it dies, so a crashed or force-killed Vito leaves no orphan holding
// a gigabyte of model. (Pdeathsig is tied to the parent *thread*; the Go
// runtime keeps the starting thread around for the child's lifetime.)
func prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}

func tieToParent(*exec.Cmd) {}
