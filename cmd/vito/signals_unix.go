//go:build unix

package main

import (
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"vito/internal/daemon"
)

// notifyUserSignals wires signal control so compositors can drive dictation
// without launching a CLI process — the recommended hotkey binding on Linux,
// since it needs no stable binary path and never mounts an AppImage:
//
//	SIGUSR2  toggle transcription   pkill -USR2 -f 'vito serve'
//	SIGUSR1  cancel the recording   pkill -USR1 -f 'vito serve'
//
// Cleanup is no longer a per-dictation signal variant; it's the settings toggle.
func notifyUserSignals(d *daemon.Daemon, log *slog.Logger) {
	ch := make(chan os.Signal, 4)
	signal.Notify(ch, syscall.SIGUSR1, syscall.SIGUSR2)
	go func() {
		for sig := range ch {
			if sig == syscall.SIGUSR1 {
				if err := d.Cancel(); err != nil {
					log.Warn("signal cancel rejected", "signal", sig.String(), "err", err)
					continue
				}
				log.Info("signal cancel", "signal", sig.String())
				continue
			}
			state, err := d.Toggle()
			if err != nil {
				log.Warn("signal toggle rejected", "signal", sig.String(), "err", err)
				continue
			}
			log.Info("signal toggle", "signal", sig.String(), "state", state)
		}
	}()
}
