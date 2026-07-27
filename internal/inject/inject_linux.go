//go:build linux

package inject

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"

	"vito/internal/config"
)

// Linux has two ways to press keys in another application, and Vito keeps both:
//
//	portal   the XDG RemoteDesktop portal over D-Bus — Wayland-native, needs no
//	         ydotoold and no /dev/uinput rule, and is the only route that works
//	         inside a Flatpak (see docs/linux-portals.md).
//	ydotool  the original uinput path, for X11 and for compositors that don't
//	         implement the portal.
//
// The mode (paste/type/clipboard_only) is orthogonal: it says *what* to deliver,
// the backend says *how*.
const (
	backendAuto    = "auto"
	backendPortal  = "portal"
	backendYdotool = "ydotool"
)

// lastBackend remembers what the most recent injection used, so the optional
// trailing Enter is pressed the same way the text was. pressEnter takes no
// config of its own (it is shared with Windows, which has only one backend).
var lastBackend struct {
	sync.Mutex
	name string
}

// resolveBackend decides the backend up front rather than trying one and
// falling back on error: a half-delivered injection must never be retried
// through the other backend, or the text would land twice.
func resolveBackend(cfg config.Injection) string {
	switch cfg.Backend {
	case backendPortal, backendYdotool:
		return cfg.Backend
	}
	if portalUsable() {
		return backendPortal
	}
	return backendYdotool
}

func injectPlatform(cfg config.Injection, mode Mode, text string) error {
	// Clipboard-only types nothing, so it is the same either way.
	if mode == ModeClipboardOnly {
		return copyText(text)
	}
	backend := resolveBackend(cfg)
	setLastBackend(backend)

	if backend == backendPortal {
		err := portalInject(cfg, mode, text)
		// errPortalUnavailable is only returned before any key was sent (no
		// portal, permission refused, timed out), so falling back cannot deliver
		// the text twice. Any other error means keys were already in flight.
		if err != nil && errors.Is(err, errPortalUnavailable) && cfg.Backend != backendPortal {
			setLastBackend(backendYdotool)
			return ydotoolInject(cfg, mode, text)
		}
		return err
	}
	return ydotoolInject(cfg, mode, text)
}

func setLastBackend(name string) {
	lastBackend.Lock()
	lastBackend.name = name
	lastBackend.Unlock()
}

// pressEnter submits the just-injected text, through whichever backend
// delivered it.
func pressEnter() error {
	lastBackend.Lock()
	backend := lastBackend.name
	lastBackend.Unlock()
	if backend == backendPortal {
		return portalPressEnter()
	}
	return ydotoolPressEnter()
}

// --- clipboard (shared by both backends) ---------------------------------
//
// wl-copy/wl-paste serve both backends today. Inside a Flatpak they are not on
// PATH unless bundled; the Clipboard portal is the alternative once injection
// lands (phase 4 in docs/linux-portals.md).

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

// ReadClipboard returns the current clipboard text (best-effort), for voice
// commands that operate on already-copied text.
func ReadClipboard() (string, bool) { return readClipboardText() }

func checkTool(name string) error {
	if _, err := exec.LookPath(name); err != nil {
		return fmt.Errorf("%s not found in PATH (see README for setup)", name)
	}
	return nil
}
