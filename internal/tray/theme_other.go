//go:build !windows

package tray

// darkTaskbar defaults to the light icon variant on non-Windows platforms.
func darkTaskbar() bool { return false }
