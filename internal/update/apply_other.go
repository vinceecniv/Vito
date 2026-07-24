//go:build !windows

package update

import "errors"

// Apply is Windows-only for now: that is where an installer exists to hand the
// job to. Elsewhere the card sends you to the release page instead.
func Apply(path string) error {
	return errors.New("installing an update from inside Vito is only supported on Windows")
}

// CanApply reports whether Vito can install an update itself on this platform.
func CanApply() bool { return false }
