//go:build darwin

package main

import (
	"os"
	"strings"
)

// defaultCommand decides what running Vito with no arguments means.
//
// Double-clicking Vito.app — and launchd starting it at login — runs the
// executable with no arguments at all, and there the only useful meaning is
// "run the daemon": a usage screen nobody can see would simply exit again.
// Typing `vito` in a terminal should still print that usage, so the two cases
// are told apart by where the binary sits rather than by a flag.
func defaultCommand() string {
	exe, err := os.Executable()
	if err != nil {
		return "help"
	}
	if strings.Contains(exe, ".app/Contents/MacOS/") {
		return "serve"
	}
	return "help"
}

// isLaunchArg reports whether an argument is noise from LaunchServices rather
// than a real subcommand. Bundled apps can be handed a -psn_0_… process serial
// number, which would otherwise be read as an unknown command and abort the
// launch. A vito:// URL arrives as an Apple Event, not on the command line, so
// there is nothing else to filter here: opening the URL is only ever meant to
// bring the daemon back up, which launching the bundle already does.
func isLaunchArg(arg string) bool {
	return strings.HasPrefix(arg, "-psn_")
}
