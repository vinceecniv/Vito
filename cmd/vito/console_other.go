//go:build !windows

package main

// hideOwnConsole is a no-op away from Windows: a daemon started from a desktop
// session or a systemd unit has no console of its own to hide.
func hideOwnConsole() {}
