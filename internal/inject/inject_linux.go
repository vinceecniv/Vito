//go:build linux

package inject

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"vito/internal/config"
)

// injectPlatform implements clipboard+paste on Wayland: wl-copy for the
// clipboard, ydotool (uinput) for the Ctrl+V keystroke — wtype does not work
// on GNOME/Mutter, ydotool works on both GNOME and niri.
func injectPlatform(cfg config.Injection, mode Mode, text string) error {
	switch mode {
	case ModeClipboardOnly:
		return copyText(text)

	case ModeType:
		if err := checkTool("ydotool"); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := ydotoolCmd(ctx, "type", "--file=-")
		cmd.Stdin = strings.NewReader(text)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("ydotool type: %w: %s", err, strings.TrimSpace(string(out)))
		}
		return nil

	case ModePaste:
		if err := checkTool("ydotool"); err != nil {
			return err
		}
		prev, prevOK := readClipboardText()
		if err := copyText(text); err != nil {
			return err
		}
		time.Sleep(time.Duration(cfg.PasteDelayMS) * time.Millisecond)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// key codes: 29 = LEFTCTRL, 47 = V
		if out, err := ydotoolCmd(ctx, "key", "29:1", "47:1", "47:0", "29:0").CombinedOutput(); err != nil {
			return fmt.Errorf("ydotool key (is ydotoold running?): %w: %s", err, strings.TrimSpace(string(out)))
		}
		if cfg.RestoreClipboard && prevOK {
			time.Sleep(time.Duration(cfg.RestoreDelayMS) * time.Millisecond)
			_ = copyText(prev) // best effort
		}
		return nil
	}
	return fmt.Errorf("unknown injection mode %q", mode)
}

// pressEnter presses Return via ydotool, used to auto-submit after injection.
func pressEnter() error {
	if err := checkTool("ydotool"); err != nil {
		return err
	}
	time.Sleep(40 * time.Millisecond) // let the paste settle before submitting
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// key code 28 = KEY_ENTER
	if out, err := ydotoolCmd(ctx, "key", "28:1", "28:0").CombinedOutput(); err != nil {
		return fmt.Errorf("ydotool enter: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func copyText(text string) error {
	if err := checkTool("wl-copy"); err != nil {
		return err
	}
	cmd := exec.Command("wl-copy")
	cmd.Stdin = strings.NewReader(text)
	// Do NOT capture output here: wl-copy forks a child that keeps serving
	// the clipboard, and reading the shared pipe would block until that
	// child exits (minutes later). Only wait for the direct process.
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("wl-copy: %w", err)
	}
	return nil
}

// readClipboardText returns the current clipboard contents when they are
// plain text; restoring non-text content (images, files) is skipped.
func readClipboardText() (string, bool) {
	types, err := exec.Command("wl-paste", "--list-types").Output()
	if err != nil || !strings.Contains(string(types), "text/") {
		return "", false
	}
	out, err := exec.Command("wl-paste", "--no-newline").Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}

// ydotoolCmd builds a ydotool invocation that finds the ydotoold socket in
// either location: the client defaults to $XDG_RUNTIME_DIR/.ydotool_socket,
// but Fedora's system service creates /tmp/.ydotool_socket. Point the client
// at whichever exists so no environment setup is required.
func ydotoolCmd(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "ydotool", args...)
	if os.Getenv("YDOTOOL_SOCKET") == "" {
		userSock := filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), ".ydotool_socket")
		if _, err := os.Stat(userSock); err != nil {
			if _, err := os.Stat("/tmp/.ydotool_socket"); err == nil {
				cmd.Env = append(os.Environ(), "YDOTOOL_SOCKET=/tmp/.ydotool_socket")
			}
		}
	}
	return cmd
}

func checkTool(name string) error {
	if _, err := exec.LookPath(name); err != nil {
		return fmt.Errorf("%s not found in PATH (see README for setup)", name)
	}
	return nil
}
