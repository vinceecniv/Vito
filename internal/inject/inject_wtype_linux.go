//go:build linux

package inject

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"vito/internal/config"
)

// The virtual-keyboard backend: wtype, which speaks the zwp_virtual_keyboard_v1
// Wayland protocol.
//
// This is the least demanding of the three. It needs no daemon, no udev rule and
// no permission dialog — only the Wayland socket, which a Flatpak already has.
// Where it works, it is the nicest experience by a distance.
//
// It is also the exact complement of the RemoteDesktop portal: Mutter
// deliberately does not implement the virtual-keyboard protocol (so wtype fails
// on GNOME), while compositors like niri implement it but have no RemoteDesktop
// backend. Between the two, almost every Wayland desktop is covered without
// asking the user to install anything.
//
// Verified on niri 26.04: ASCII, accents, CJK and symbols all arrive correctly,
// Ctrl+V pastes, and Return submits.

var wtypeProbe struct {
	sync.Once
	ok bool
}

// wtypeUsable reports whether wtype is installed *and* this compositor actually
// implements the protocol. Typing the empty string is the cheapest honest probe
// there is: it binds the virtual keyboard and types nothing, so it fails exactly
// when the compositor refuses (Mutter, or an X11 session).
func wtypeUsable() bool {
	wtypeProbe.Do(func() {
		if os.Getenv("WAYLAND_DISPLAY") == "" {
			return
		}
		if _, err := exec.LookPath("wtype"); err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		wtypeProbe.ok = exec.CommandContext(ctx, "wtype", "").Run() == nil
	})
	return wtypeProbe.ok
}

func wtypeInject(cfg config.Injection, mode Mode, text string) error {
	switch mode {
	case ModeType:
		return wtypeText(text)

	case ModePaste:
		prev, prevOK := readClipboardText()
		if err := copyText(text); err != nil {
			return err
		}
		time.Sleep(time.Duration(cfg.PasteDelayMS) * time.Millisecond)
		if err := wtypeKeys(5*time.Second, "-M", "ctrl", "-k", "v", "-m", "ctrl"); err != nil {
			return err
		}
		if cfg.RestoreClipboard && prevOK {
			time.Sleep(time.Duration(cfg.RestoreDelayMS) * time.Millisecond)
			_ = copyText(prev) // best effort
		}
		return nil
	}
	return fmt.Errorf("unknown injection mode %q", mode)
}

func wtypePressEnter() error {
	time.Sleep(40 * time.Millisecond) // let the paste settle before submitting
	return wtypeKeys(5*time.Second, "-k", "Return")
}

// wtypeText types arbitrary text, handed over on stdin rather than as an
// argument: a dictation that happens to start with "-" must never be parsed as
// an option.
func wtypeText(text string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "wtype", "-")
	cmd.Stdin = strings.NewReader(text)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("wtype: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func wtypeKeys(timeout time.Duration, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "wtype", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("wtype %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
