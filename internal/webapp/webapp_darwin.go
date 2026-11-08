package webapp

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
)

// storeGlobs are the browser profiles under ~/Library/Application Support:
// Google/Chrome/Default, BraveSoftware/Brave-Browser/Default.
func storeGlobs() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	sup := filepath.Join(home, "Library", "Application Support")
	return []string{filepath.Join(sup, "*", "*"), filepath.Join(sup, "*", "*", "*")}
}

// launcher starts app id through the little app its browser made for it, in
// ~/Applications/Chrome Apps.localized (or Brave Browser Apps, Edge Apps…),
// whose Info.plist names the id as CrAppModeShortcutID.
func launcher(id string) func() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	plists, _ := filepath.Glob(filepath.Join(home, "Applications", "*", "*.app", "Contents", "Info.plist"))
	for _, p := range plists {
		b, err := os.ReadFile(p)
		if err != nil || !bytes.Contains(b, []byte(id)) {
			continue
		}
		app := filepath.Dir(filepath.Dir(p))
		return func() error { return exec.Command("open", app).Start() }
	}
	return nil
}

// focusApp: on macOS the app shim, opened again, brings the running app
// forward itself.
func focusApp(string) bool { return false }
