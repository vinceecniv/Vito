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

// The uinput backend: wl-copy for the clipboard, ydotool for the keystroke —
// wtype does not work on GNOME/Mutter, ydotool works on both GNOME and niri.
// It needs a running ydotoold and a udev rule for /dev/uinput, which is exactly
// the setup the portal backend removes; kept for X11 and for compositors that
// don't implement the RemoteDesktop portal.
func ydotoolInject(cfg config.Injection, mode Mode, text string) error {
	switch mode {
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

// ydotoolPressEnter presses Return, used to auto-submit after injection.
func ydotoolPressEnter() error {
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
