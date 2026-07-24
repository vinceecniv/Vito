//go:build !windows && !linux

package autostart

import "errors"

// Supported reports whether autostart can be configured on this OS.
func Supported() bool { return false }

// Enabled always reports false on unsupported platforms.
func Enabled() (bool, error) { return false, nil }

// Set is unsupported on this platform.
func Set(enable bool) error {
	return errors.New("autostart is not supported on this platform")
}
