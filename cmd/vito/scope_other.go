//go:build !linux

package main

import "log/slog"

// ensureAppScope only matters on Linux, where the XDG portals identify a host
// application by the systemd scope it runs in. Elsewhere there is nothing to do.
func ensureAppScope(log *slog.Logger) {}
