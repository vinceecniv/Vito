//go:build linux

package media

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// Linux pauses media over MPRIS, spoken directly on the session bus.
//
// This used to shell out to playerctl, which is only an MPRIS client itself —
// so talking to the bus removes an external dependency without losing anything.
// It also matters for the Flatpak: a bundled binary would be one more thing to
// build and ship, whereas `--talk-name=org.mpris.MediaPlayer2.*` is a line in
// the manifest.
//
// **Ducking is gone on Linux.** It used to lower each stream's volume through
// pactl, which meant parsing pactl's text output, and it never worked reliably.
// PulseAudio speaks its own binary protocol rather than D-Bus, so doing it
// properly would be a project of its own for a feature that was already the
// weakest part of this package. Since "duck" is the default action, it now falls
// back to pausing rather than silently doing nothing — otherwise upgrading would
// quietly leave music blaring through every dictation.
//
// Everything here stays best-effort with a short timeout: a hung media player
// must never stall a dictation.

const (
	callTimeout = 800 * time.Millisecond

	mprisPrefix = "org.mpris.MediaPlayer2."
	mprisPath   = "/org/mpris/MediaPlayer2"
	mprisPlayer = "org.mpris.MediaPlayer2.Player"
)

func suppressPlatform(a Action, log *slog.Logger) any {
	switch a {
	case ActionPause, ActionDuck: // see the note above: duck degrades to pause
		if players := pausePlayers(log); len(players) > 0 {
			log.Info("paused media for dictation", "players", players)
			return players
		}
	}
	return nil
}

func restorePlatform(a Action, token any, log *slog.Logger) {
	players, _ := token.([]string)
	if len(players) == 0 {
		return
	}
	conn, err := dbus.SessionBus()
	if err != nil {
		return
	}
	for _, name := range players {
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		call := conn.Object(name, mprisPath).CallWithContext(ctx, mprisPlayer+".Play", 0)
		cancel()
		if call.Err != nil {
			log.Debug("could not resume player", "player", name, "err", call.Err)
		}
	}
	log.Info("resumed media after dictation", "players", players)
}

// pausePlayers pauses every MPRIS player that is actually playing, and returns
// their bus names so Restore resumes exactly those — not whatever happens to be
// paused by the time the dictation ends.
func pausePlayers(log *slog.Logger) []string {
	conn, err := dbus.SessionBus()
	if err != nil {
		return nil
	}
	var names []string
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	err = conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.ListNames", 0).Store(&names)
	cancel()
	if err != nil {
		log.Debug("could not list bus names", "err", err)
		return nil
	}

	var paused []string
	for _, name := range names {
		if !strings.HasPrefix(name, mprisPrefix) {
			continue
		}
		obj := conn.Object(name, mprisPath)
		if playbackStatus(obj) != "Playing" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		call := obj.CallWithContext(ctx, mprisPlayer+".Pause", 0)
		cancel()
		if call.Err != nil {
			log.Debug("could not pause player", "player", name, "err", call.Err)
			continue
		}
		paused = append(paused, name)
	}
	return paused
}

func playbackStatus(obj dbus.BusObject) string {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	var v dbus.Variant
	err := obj.CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0,
		mprisPlayer, "PlaybackStatus").Store(&v)
	if err != nil {
		return ""
	}
	s, _ := v.Value().(string)
	return s
}
