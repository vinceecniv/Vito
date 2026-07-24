//go:build linux

package audio

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// On Linux the OS input level is the default PulseAudio/PipeWire source volume,
// controlled via pactl. A specific device name falls back to the default source.

func LevelSupported() bool { _, err := exec.LookPath("pactl"); return err == nil }

func InputLevel(deviceName string) (float64, error) {
	out := pactl("get-source-volume", "@DEFAULT_SOURCE@")
	if out == "" {
		return 0, errors.New("pactl get-source-volume failed")
	}
	if i := strings.IndexByte(out, '%'); i > 0 {
		j := i
		for j > 0 && out[j-1] >= '0' && out[j-1] <= '9' {
			j--
		}
		if n, err := strconv.Atoi(out[j:i]); err == nil {
			return float64(n) / 100.0, nil
		}
	}
	return 0, errors.New("could not parse source volume")
}

func SetInputLevel(deviceName string, level float64) error {
	if level < 0 {
		level = 0
	} else if level > 1 {
		level = 1
	}
	pct := strconv.Itoa(int(level*100 + 0.5))
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	return exec.CommandContext(ctx, "pactl", "set-source-volume", "@DEFAULT_SOURCE@", pct+"%").Run()
}

func pactl(args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	out, err := exec.CommandContext(ctx, "pactl", args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
