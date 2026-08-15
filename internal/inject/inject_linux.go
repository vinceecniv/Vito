//go:build linux

package inject

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"

	"vito/internal/config"
	"vito/internal/xdgportal"
)

// Linux has three ways to press keys in another application, and Vito keeps all
// three because no single one covers the field (see docs/linux-portals.md):
//
//	wayland  wtype over the virtual-keyboard protocol. No daemon, no udev rule,
//	         no permission prompt — only the Wayland socket, which a Flatpak
//	         already has. Works on niri/wlroots/KDE; Mutter refuses it.
//	portal   the XDG RemoteDesktop portal over D-Bus. Asks permission once, and
//	         is the route that works on GNOME. Also sandbox-friendly.
//	ydotool  the original uinput path: needs ydotoold plus a /dev/uinput rule,
//	         and cannot work from inside a sandbox. The last resort — X11, or a
//	         compositor that offers neither of the above.
//
// "auto" tries them in that order, from least to most intrusive. The first two
// are exact complements: where one is unavailable the other generally isn't.
//
// The mode (paste/type/clipboard_only) is orthogonal: it says *what* to deliver,
// the backend says *how*.
const (
	backendAuto    = "auto"
	backendWayland = "wayland"
	backendPortal  = "portal"
	backendYdotool = "ydotool"
	// backendClipboard is not a way of typing but the absence of one: nothing on
	// this system can press a key, so the text is left on the clipboard.
	backendClipboard = "clipboard"
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
	case backendWayland, backendPortal, backendYdotool:
		return cfg.Backend
	}
	if wtypeUsable() {
		return backendWayland
	}
	if portalUsable() {
		return backendPortal
	}
	return backendYdotool
}

// canType reports whether any backend can actually deliver keystrokes. Inside a
// Flatpak on a compositor with no RemoteDesktop portal there is none: the
// portal is missing, ydotool cannot work in a sandbox, and Flatpak's managed
// Wayland socket filters out the virtual-keyboard protocol — deliberately, since
// injecting input into other windows is exactly what a sandbox restricts.
func canType(cfg config.Injection) bool {
	if resolveBackend(cfg) != backendYdotool {
		return true
	}
	return ydotoolUsable()
}

// ydotoolUsable reports whether the uinput route could actually deliver.
//
// Inside a Flatpak the answer is always no, and that is worth short-circuiting
// rather than discovering through a failed exec: the binary is not shipped, the
// sandbox has no /dev/uinput and no daemon to talk to, and there is no version
// of "install ydotool" that would change any of it. Telling the user to follow
// the README there is advice they cannot act on.
func ydotoolUsable() bool {
	if xdgportal.InFlatpak() {
		return false
	}
	_, err := exec.LookPath("ydotool")
	return err == nil
}

// adjustMode downgrades paste/type to clipboard-only when nothing here can press
// a key. Failing with "ydotool not found" would point the user at something they
// cannot fix — and would throw away a perfectly good transcript.
func adjustMode(cfg config.Injection, mode Mode) Mode {
	if mode == ModeClipboardOnly || canType(cfg) {
		return mode
	}
	return ModeClipboardOnly
}

func injectPlatform(cfg config.Injection, mode Mode, text string) (Mode, error) {
	// Clipboard-only types nothing, so it is the same either way.
	if mode == ModeClipboardOnly {
		return mode, copyText(text)
	}
	backend := resolveBackend(cfg)
	setLastBackend(backend)

	switch backend {
	case backendWayland:
		return mode, wtypeInject(cfg, mode, text)
	case backendPortal:
		err := portalInject(cfg, mode, text)
		// errPortalUnavailable is only returned before any key was sent (no
		// portal, permission refused, timed out), so falling back cannot deliver
		// the text twice. Any other error means keys were already in flight.
		if err != nil && errors.Is(err, errPortalUnavailable) && cfg.Backend != backendPortal {
			return fallback(cfg, mode, text)
		}
		return mode, err
	}
	return fallback(cfg, mode, text)
}

// fallback delivers after the preferred route turned out to be unavailable.
//
// It exists because "the portal is advertised" and "the portal works" are
// different claims, and the difference only shows on the first dictation. When
// ydotool can still take over it does; when it cannot — a sandbox, or simply a
// machine without it — the text goes to the clipboard rather than surfacing an
// error about a tool the user may have no way to provide. The transcript is
// already earned by then, and losing it to a setup problem is the worst possible
// outcome.
//
// The returned mode is what actually happened, not what was asked for, so the
// caller can tell the user their text was copied instead of typed.
func fallback(cfg config.Injection, mode Mode, text string) (Mode, error) {
	if ydotoolUsable() {
		setLastBackend(backendYdotool)
		return mode, ydotoolInject(cfg, mode, text)
	}
	setLastBackend(backendClipboard)
	return ModeClipboardOnly, copyText(text)
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
	switch backend {
	case backendWayland:
		return wtypePressEnter()
	case backendPortal:
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

// ActiveBackend names the backend an injection would use right now, for the
// log line and the Linux diagnostics panel. Now that there are three of them,
// "which one am I actually on?" is the first question worth answering when
// delivery misbehaves.
// Sandboxed reports whether Vito is running inside a Flatpak. The settings page
// uses it to stop offering advice that only applies outside one — installing
// helpers it cannot see, or a delivery route the sandbox forbids.
func Sandboxed() bool { return xdgportal.InFlatpak() }

func ActiveBackend(cfg config.Injection) string {
	if !canType(cfg) {
		return backendClipboard
	}
	return resolveBackend(cfg)
}
