//go:build !linux && !windows && !darwin

package pill

import "log/slog"

func available() bool { return false }

func openWindow(output uint32, input [4]int) (window, error) { return nil, errUnsupported }

func openIdleWindow(output uint32, input [4]int) (window, error) { return nil, errUnsupported }

func openIslandWindow(output uint32, lw, lh int, input [4]int) (window, error) {
	return nil, errUnsupported
}

func watchOutputs(stop <-chan struct{}, changed func(outputs []uint32)) error {
	return errUnsupported
}

func watchColorScheme(log *slog.Logger, changed func(dark bool)) {}

func gnomeAvailable() bool { return false }

func ensureGnomeExtension(log *slog.Logger) {}

// GnomeExtension is a Linux matter.
func GnomeExtension() string { return "" }
