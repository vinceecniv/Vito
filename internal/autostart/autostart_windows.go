//go:build windows

package autostart

import (
	"errors"

	"golang.org/x/sys/windows/registry"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// Supported reports whether autostart can be configured on this OS.
func Supported() bool { return true }

// Enabled reports whether the HKCU Run value for Vito exists.
func Enabled() (bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false, err
	}
	defer k.Close()
	_, _, err = k.GetStringValue(appName)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// Set adds or removes the HKCU Run value that launches `vito serve` at login.
func Set(enable bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !enable {
		if err := k.DeleteValue(appName); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return err
		}
		return nil
	}
	exe, err := executablePath()
	if err != nil {
		return err
	}
	// Plain quotes, not %q: that is a Go string literal, so it escapes every
	// backslash in the path and the Run entry ends up reading
	// "C:\\Users\\…\\vito.exe" — which Windows only tolerates by accident.
	return k.SetStringValue(appName, `"`+exe+`" serve`)
}
