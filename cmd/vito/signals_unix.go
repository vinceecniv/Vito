//go:build unix

package main

import (
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"vito/internal/daemon"
	"vito/internal/hotkey"
)

// The real-time signals `pkill -RTMIN+1` and `pkill -RTMIN+2` send on Linux.
// Real-time signals queue in order, where a press and a release as plain signals
// could merge. macOS has none.
const (
	sigKeyDown = syscall.Signal(35)
	sigKeyUp   = syscall.Signal(36)
)

// notifyUserSignals wires signal control so compositors can drive dictation
// without launching a CLI process — the recommended hotkey binding on Linux,
// since it needs no stable binary path and never mounts an AppImage:
//
//	SIGUSR2   toggle transcription   pkill -USR2 -f 'vito serve'
//	SIGUSR1   cancel the recording   pkill -USR1 -f 'vito serve'
//	RTMIN+1   dictation key down     pkill -RTMIN+1 -f 'vito serve'
//	RTMIN+2   dictation key up       pkill -RTMIN+2 -f 'vito serve'
//
// The last two are for a compositor that reports press and release (bindr on
// Hyprland, press/release on niri): hold to dictate, double-tap for hands-free.
//
// Cleanup is no longer a per-dictation signal variant; it's the settings toggle.
func notifyUserSignals(d *daemon.Daemon, log *slog.Logger) {
	ch := make(chan os.Signal, 16)
	signal.Notify(ch, syscall.SIGUSR1, syscall.SIGUSR2)
	if runtime.GOOS == "linux" {
		signal.Notify(ch, sigKeyDown, sigKeyUp)
	}
	gesture := hotkey.NewGesture(d, log)
	go func() {
		for sig := range ch {
			switch sig {
			case sigKeyDown:
				log.Debug("dictation key down (signal)")
				gesture.Down()
				continue
			case sigKeyUp:
				log.Debug("dictation key up (signal)")
				gesture.Up()
				continue
			}
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
