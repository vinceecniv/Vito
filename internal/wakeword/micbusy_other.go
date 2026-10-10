//go:build !windows

package wakeword

// MicInUseByOthers is only known on Windows; elsewhere Vito keeps listening.
func MicInUseByOthers() bool { return false }
