//go:build linux

package inject

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"vito/internal/config"
	"vito/internal/xdgportal"
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

// HostWaylandLink is where a Flatpak finds a direct connection to the
// compositor, relative to $XDG_RUNTIME_DIR: a hard link to the compositor's own
// socket, made on the host by one line in the compositor's startup.
//
// A Flatpak's Wayland socket is a managed one that filters out the
// virtual-keyboard protocol, so wtype is refused in the sandbox even where the
// compositor implements it (niri, Hyprland, sway…). The real socket cannot be
// handed in by name — Flatpak already has a symlink of its own at that path,
// which shadows the grant (docs/flatpak.md). A hard link under another name is
// the same socket without that clash, and connecting to it bypasses the filter.
// The manifest grants xdg-run/vito with :create, so the directory exists
// whichever starts first, and a link made later shows up in a running sandbox.
const HostWaylandLink = "vito/wayland"

// hostWayland returns the path of that link when Vito runs in a Flatpak and the
// link is there, else "". Outside a sandbox wtype uses $WAYLAND_DISPLAY as is.
func hostWayland() string {
	if !xdgportal.InFlatpak() {
		return ""
	}
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		return ""
	}
	p := filepath.Join(dir, HostWaylandLink)
	if fi, err := os.Stat(p); err != nil || fi.Mode()&os.ModeSocket == 0 {
		return ""
	}
	return p
}

// HasHostWaylandLink reports whether the link exists — which, while delivery is
// still clipboard-only, means it points at a socket that no longer serves
// (a previous session) and wants making again.
func HasHostWaylandLink() bool { return hostWayland() != "" }

// wtypeCommand runs wtype over the direct connection when there is one.
// libwayland accepts an absolute path in WAYLAND_DISPLAY.
func wtypeCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "wtype", args...)
	if p := hostWayland(); p != "" {
		cmd.Env = append(os.Environ(), "WAYLAND_DISPLAY="+p)
	}
	return cmd
}

var wtypeProbe struct {
	sync.Mutex
	done bool
	key  string
	ok   bool
}

// wtypeProbeKey names the connection the probe answers for. On the host that
// never changes, so wtype is probed once. In a Flatpak it is the link's inode:
// a link made (or remade for a new session) while Vito runs is probed afresh,
// so it takes effect without a restart.
func wtypeProbeKey() string {
	p := hostWayland()
	if p == "" {
		return ""
	}
	if fi, err := os.Stat(p); err == nil {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			return fmt.Sprintf("%s:%d", p, st.Ino)
		}
	}
	return p
}

// wtypeUsable reports whether wtype is installed *and* this compositor actually
// implements the protocol. Typing the empty string is the cheapest honest probe
// there is: it binds the virtual keyboard and types nothing, so it fails exactly
// when the compositor refuses (Mutter, an X11 session, or a Flatpak without the
// direct connection above).
func wtypeUsable() bool {
	key := wtypeProbeKey()
	wtypeProbe.Lock()
	defer wtypeProbe.Unlock()
	if wtypeProbe.done && wtypeProbe.key == key {
		return wtypeProbe.ok
	}
	wtypeProbe.done, wtypeProbe.key, wtypeProbe.ok = true, key, false
	if os.Getenv("WAYLAND_DISPLAY") == "" {
		return false
	}
	if _, err := exec.LookPath("wtype"); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	wtypeProbe.ok = wtypeCommand(ctx, "").Run() == nil
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
	cmd := wtypeCommand(ctx, "-")
	cmd.Stdin = strings.NewReader(text)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("wtype: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func wtypeKeys(timeout time.Duration, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if out, err := wtypeCommand(ctx, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("wtype %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
