//go:build darwin

package media

import (
	"context"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// macOS backends, both driven through osascript — the same shape as the Linux
// backend shelling out to pactl/playerctl:
//
//   - duck:  the system output volume. macOS has no public per-application
//     volume, so this lowers the one slider everything shares. That is coarser
//     than Windows' per-session WASAPI volume, but it has the same virtue as
//     the Linux sink-input approach: it reaches apps with no media controls at
//     all, browser video included.
//   - pause: the scriptable players (Music, Spotify), and only the ones
//     actually playing. Browsers expose nothing scriptable on macOS, so pause
//     mode cannot reach them — duck is the default for exactly this reason.
//
// Checking `is running` before talking to a player is not optional: telling a
// non-running app to pause would launch it, so the check is what stops "quieten
// the music" from opening Music on a machine that never had it open.
//
// Every osascript call has a short timeout: a hung media stack must never stall
// a dictation.

const (
	cmdTimeout = 800 * time.Millisecond
	duckVolume = 25 // percent to duck the output down to, matching Linux
)

// scriptablePlayers are the apps pause mode can drive. Both use the same
// vocabulary (player state / pause / play), so one script shape covers them.
var scriptablePlayers = []string{"Music", "Spotify"}

func suppressPlatform(a Action, log *slog.Logger) any {
	switch a {
	case ActionPause:
		if players := pausePlayers(log); len(players) > 0 {
			log.Info("paused media for dictation", "players", players)
			return players
		}
	case ActionDuck:
		prev, ok := outputVolume(log)
		if !ok || prev <= duckVolume {
			return nil // already at or below the target: nothing worth changing
		}
		if !setOutputVolume(log, duckVolume) {
			return nil
		}
		log.Info("ducked media for dictation", "from", prev, "to", duckVolume)
		return prev
	}
	return nil
}

func restorePlatform(a Action, token any, log *slog.Logger) {
	switch a {
	case ActionPause:
		players, _ := token.([]string)
		for _, p := range players {
			playerCommand(log, p, "play")
		}
		if len(players) > 0 {
			log.Info("resumed media after dictation", "players", players)
		}
	case ActionDuck:
		prev, ok := token.(int)
		if !ok {
			return
		}
		if setOutputVolume(log, prev) {
			log.Info("restored media volume after dictation", "to", prev)
		}
	}
}

// --- pause (osascript) ----------------------------------------------------

func pausePlayers(log *slog.Logger) []string {
	var paused []string
	for _, p := range scriptablePlayers {
		if playerCommand(log, p, "pause") {
			paused = append(paused, p)
		}
	}
	return paused
}

// playerCommand runs "pause" or "play" against one player and reports whether
// it acted. For "pause" that means the player was running and playing; for
// "play" it means the player was still running.
//
// The player name is interpolated into the script, which is safe here because
// it only ever comes from scriptablePlayers — never from user input.
func playerCommand(log *slog.Logger, player, verb string) bool {
	guard := "player state is playing"
	if verb == "play" {
		// Resuming does not depend on the current state: the player was paused
		// by us, so anything other than "still running" is reason enough to try.
		guard = "true"
	}
	script := `if application "` + player + `" is running then
	tell application "` + player + `"
		if ` + guard + ` then
			` + verb + `
			return "yes"
		end if
	end tell
end if
return "no"`
	return output(log, "osascript", "-e", script) == "yes"
}

// --- duck (osascript) -----------------------------------------------------

// outputVolume reads the system output volume as a 0..100 percentage.
func outputVolume(log *slog.Logger) (int, bool) {
	out := output(log, "osascript", "-e", "output volume of (get volume settings)")
	// A device with no software volume control reports "missing value".
	v, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, false
	}
	return v, true
}

func setOutputVolume(log *slog.Logger, pct int) bool {
	if pct < 0 {
		pct = 0
	} else if pct > 100 {
		pct = 100
	}
	return run(log, "osascript", "-e", "set volume output volume "+strconv.Itoa(pct))
}

// --- helpers --------------------------------------------------------------

// output runs a command and returns trimmed stdout ("" on any error).
func output(log *slog.Logger, name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		if log != nil {
			log.Debug("command failed", "cmd", name, "err", err)
		}
		return ""
	}
	return strings.TrimSpace(string(out))
}

// run runs a command and reports whether it succeeded.
func run(log *slog.Logger, name string, args ...string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()
	if err := exec.CommandContext(ctx, name, args...).Run(); err != nil {
		if log != nil {
			log.Debug("command failed", "cmd", name, "err", err)
		}
		return false
	}
	return true
}
