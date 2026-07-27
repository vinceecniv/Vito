//go:build linux

package main

import (
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"vito/internal/selfexe"
)

// The GlobalShortcuts portal refuses callers it cannot identify: CreateSession
// fails outright with "An app id is required". For a sandboxed app that id comes
// from the sandbox; for a host application the portal derives it from the
// systemd scope the process sits in — the app-<id>-<random>.scope convention
// that desktop launchers follow.
//
// Vito is usually started in ways that produce no such scope: from a terminal,
// or from an XDG autostart entry. So before serving, put ourselves in one by
// re-executing through systemd-run. It costs one exec at startup and is what
// makes portal hotkeys work for a normally-installed Vito rather than only for
// someone who knew to type systemd-run.
//
// Everything here fails open: if anything is missing or unexpected, Vito starts
// exactly as before and simply falls back to the SIGUSR1/SIGUSR2 hotkey route.
func ensureAppScope(log *slog.Logger) {
	// Guard against re-exec loops above all else: the child must never try again,
	// whatever the cgroup ends up looking like.
	if os.Getenv(vitoScopedEnv) != "" {
		return
	}
	if os.Getenv("FLATPAK_ID") != "" {
		return // inside a Flatpak the app id is already known
	}
	if hasAppScope() {
		return // launched by a desktop launcher, which already scoped us
	}
	runner, err := exec.LookPath("systemd-run")
	if err != nil {
		return // no systemd (or no user session): nothing to do
	}
	exe, err := selfexe.Path()
	if err != nil {
		return
	}
	argv := append([]string{
		"systemd-run", "--user", "--scope", "--quiet",
		"--unit=app-vito-" + strconv.Itoa(os.Getpid()),
		exe,
	}, os.Args[1:]...)

	log.Debug("re-executing inside a systemd app scope so the portal can identify us")
	// Only returns if the exec itself failed, in which case carrying on unscoped
	// is still a perfectly working Vito.
	_ = syscall.Exec(runner, argv, append(os.Environ(), vitoScopedEnv+"=1"))
}

// vitoScopedEnv marks the re-executed child.
const vitoScopedEnv = "VITO_SCOPED"

// hasAppScope reports whether this process already sits in an app-*.scope, which
// is what the portal reads to work out who is calling.
func hasAppScope() bool {
	b, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		// cgroup v2: "0::/user.slice/.../app-gnome-vito-1234.scope"
		if i := strings.LastIndex(line, "/"); i >= 0 {
			leaf := line[i+1:]
			if strings.HasPrefix(leaf, "app-") && strings.HasSuffix(leaf, ".scope") {
				return true
			}
		}
	}
	return false
}
