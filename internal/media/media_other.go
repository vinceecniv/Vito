//go:build !linux && !windows && !darwin

package media

import "log/slog"

// No media-control backend on this platform: everything is a no-op.
func suppressPlatform(a Action, log *slog.Logger) any { return nil }

func restorePlatform(a Action, token any, log *slog.Logger) {}
