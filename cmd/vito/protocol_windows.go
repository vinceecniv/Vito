//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows/registry"
)

// registerLaunchProtocol registers a "vito://" URL protocol pointing at this
// executable, so the web UI can relaunch the daemon (e.g. from the "start
// background process" button) even when it isn't running. The registration
// persists in the registry, so it stays available while the daemon is down.
func registerLaunchProtocol() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	base, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Classes\vito`, registry.SET_VALUE)
	if err != nil {
		return
	}
	defer base.Close()
	_ = base.SetStringValue("", "URL:Vito Protocol")
	_ = base.SetStringValue("URL Protocol", "")

	cmd, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Classes\vito\shell\open\command`, registry.SET_VALUE)
	if err != nil {
		return
	}
	defer cmd.Close()
	// Windows appends the launched URL as an extra argument; `serve` ignores it.
	_ = cmd.SetStringValue("", `"`+exe+`" serve`)
}
