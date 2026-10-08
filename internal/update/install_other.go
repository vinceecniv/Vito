//go:build !windows && !linux && !darwin

package update

func platformInstaller() (installer, string) { return nil, "no automatic updates on this system" }
