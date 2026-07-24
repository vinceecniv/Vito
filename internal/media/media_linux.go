//go:build linux

package media

import (
	"context"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Linux backends:
//
//   - pause: playerctl (MPRIS). Pause only the players actually Playing and
//     remember them so Restore plays back exactly those.
//   - duck:  pactl (PulseAudio/PipeWire). Lower the volume of every non-corked
//     sink-input and remember each one's original volume to restore it. This
//     reaches apps that expose no MPRIS controls (browser video, games).
//
// Every external call has a short timeout: a hung media stack must never stall
// a dictation.

const (
	cmdTimeout = 800 * time.Millisecond
	duckVolume = 25 // percent to duck playing streams down to
)

// duckedInput remembers a sink-input's index and original volume percentage.
type duckedInput struct {
	index  string
	volPct string
}

func suppressPlatform(a Action, log *slog.Logger) any {
	switch a {
	case ActionPause:
		if players := pausePlayers(log); len(players) > 0 {
			log.Info("paused media for dictation", "players", players)
			return players
		}
	case ActionDuck:
		if ducked := duckInputs(log); len(ducked) > 0 {
			log.Info("ducked media for dictation", "streams", len(ducked))
			return ducked
		}
	}
	return nil
}

func restorePlatform(a Action, token any, log *slog.Logger) {
	switch a {
	case ActionPause:
		players, _ := token.([]string)
		for _, p := range players {
			run(log, "playerctl", "-p", p, "play")
		}
		if len(players) > 0 {
			log.Info("resumed media after dictation", "players", players)
		}
	case ActionDuck:
		ducked, _ := token.([]duckedInput)
		for _, d := range ducked {
			run(log, "pactl", "set-sink-input-volume", d.index, d.volPct+"%")
		}
		if len(ducked) > 0 {
			log.Info("restored media volume after dictation", "streams", len(ducked))
		}
	}
}

// --- pause (playerctl) ---------------------------------------------------

func pausePlayers(log *slog.Logger) []string {
	if _, err := exec.LookPath("playerctl"); err != nil {
		return nil
	}
	out := output(nil, "playerctl", "-l")
	if out == "" || strings.Contains(out, "No players found") {
		return nil
	}
	var paused []string
	for _, p := range strings.Fields(out) {
		if output(log, "playerctl", "-p", p, "status") == "Playing" {
			if run(log, "playerctl", "-p", p, "pause") {
				paused = append(paused, p)
			}
		}
	}
	return paused
}

// --- duck (pactl) --------------------------------------------------------

func duckInputs(log *slog.Logger) []duckedInput {
	if _, err := exec.LookPath("pactl"); err != nil {
		return nil
	}
	blocks := strings.Split(output(log, "pactl", "list", "sink-inputs"), "Sink Input #")
	var ducked []duckedInput
	for _, b := range blocks[1:] {
		idx := strings.TrimSpace(strings.SplitN(b, "\n", 2)[0])
		if idx == "" || strings.Contains(b, "Corked: yes") {
			continue // no index or not actively playing
		}
		vol := firstVolumePct(b)
		if vol == "" || vol == "0" {
			continue
		}
		if run(log, "pactl", "set-sink-input-volume", idx, strconv.Itoa(duckVolume)+"%") {
			ducked = append(ducked, duckedInput{index: idx, volPct: vol})
		}
	}
	return ducked
}

// firstVolumePct extracts the first "NN%" from a sink-input's Volume: line.
func firstVolumePct(block string) string {
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Volume:") {
			continue
		}
		if i := strings.IndexByte(line, '%'); i > 0 {
			j := i
			for j > 0 && (line[j-1] >= '0' && line[j-1] <= '9') {
				j--
			}
			return line[j:i]
		}
	}
	return ""
}

// --- helpers -------------------------------------------------------------

// output runs a command and returns trimmed stdout ("" on any error).
func output(log *slog.Logger, name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		if log != nil {
			log.Debug("command failed", "cmd", name, "args", args, "err", err)
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
			log.Debug("command failed", "cmd", name, "args", args, "err", err)
		}
		return false
	}
	return true
}
