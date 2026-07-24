//go:build windows

package main

import (
	"log/slog"

	"vito/internal/daemon"
)

// Windows has no SIGUSR signals; hotkeys arrive via RegisterHotKey (phase 2).
func notifyUserSignals(d *daemon.Daemon, log *slog.Logger) {}
