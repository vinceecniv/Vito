//go:build windows

package pill

import (
	"log/slog"
	"time"

	"vito/internal/desktopcolors"

	"golang.org/x/sys/windows/registry"
)

// watchColorScheme follows Windows' app mode (Settings → Personalisation →
// Colours). There is a message for its change, but only for a top-level
// window; reading the value every few seconds is simpler and costs nothing.
func watchColorScheme(log *slog.Logger, changed func(dark bool)) {
	read := func() (dark, ok bool) {
		k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
		if err != nil {
			return false, false
		}
		defer k.Close()
		v, _, err := k.GetIntegerValue("AppsUseLightTheme")
		return err == nil && v == 0, err == nil
	}
	go func() {
		last, known, lastKey := false, false, desktopcolors.SystemKey()
		for {
			// The accent colour counts too: the "windows" theme wears it.
			key := desktopcolors.SystemKey()
			if dark, ok := read(); ok && (!known || dark != last || key != lastKey) {
				last, known, lastKey = dark, true, key
				changed(dark)
			}
			time.Sleep(3 * time.Second)
		}
	}()
}
