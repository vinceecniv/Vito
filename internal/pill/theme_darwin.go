//go:build darwin

package pill

/*
#include "window_darwin.h"
*/
import "C"

import (
	"log/slog"
	"time"

	"vito/internal/desktopcolors"
)

// watchColorScheme follows macOS' appearance (System Settings → Appearance,
// Auto included). AppKit announces the change only to a running app's own
// windows; reading the preference every few seconds is simpler.
func watchColorScheme(log *slog.Logger, changed func(dark bool)) {
	go func() {
		last, known, lastKey := false, false, desktopcolors.SystemKey()
		for {
			// The accent colour counts too: the "macos" theme wears it.
			key := desktopcolors.SystemKey()
			if dark := bool(C.vitoPillDark()); !known || dark != last || key != lastKey {
				last, known, lastKey = dark, true, key
				changed(dark)
			}
			time.Sleep(3 * time.Second)
		}
	}()
}
