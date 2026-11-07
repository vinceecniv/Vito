//go:build !linux

package pill

import "log/slog"

func openWindow() (window, error) { return nil, errUnsupported }

func openIdleWindow(output uint32) (window, error) { return nil, errUnsupported }

func watchOutputs(stop <-chan struct{}, changed func(outputs []uint32)) error {
	return errUnsupported
}

func watchColorScheme(log *slog.Logger, changed func(dark bool)) {}
