//go:build darwin

package pill

/*
#include "window_darwin.h"
*/
import "C"

import (
	"log/slog"
	"time"
)

// watchColorScheme follows macOS' appearance (System Settings → Appearance,
// Auto included). AppKit announces the change only to a running app's own
// windows; reading the preference every few seconds is simpler.
func watchColorScheme(log *slog.Logger, changed func(dark bool)) {
	go func() {
		last, known := false, false
		for {
			if dark := bool(C.vitoPillDark()); !known || dark != last {
				last, known = dark, true
				changed(dark)
			}
			time.Sleep(3 * time.Second)
		}
	}()
}
