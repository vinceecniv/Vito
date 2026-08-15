//go:build !windows && !linux && !darwin

package audio

import "errors"

// OS input-level control is unsupported on this platform.
func LevelSupported() bool { return false }

func InputLevel(deviceName string) (float64, error) {
	return 0, errors.New("input level control not supported")
}

func SetInputLevel(deviceName string, level float64) error {
	return errors.New("input level control not supported")
}
