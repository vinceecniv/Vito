//go:build !windows && !linux

package localstt

import "os/exec"

// macOS has neither a parent-death signal nor job objects, so a Vito that is
// killed outright (not quit) can leave the server behind until the next login.
// A clean quit stops it; that is the common case.
func prepare(*exec.Cmd)     {}
func tieToParent(*exec.Cmd) {}
