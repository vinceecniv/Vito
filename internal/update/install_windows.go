//go:build windows

package update

import (
	"os"
	"path/filepath"
	"strings"
)

// winInstaller runs the release's setup silently. The setup asks the running
// Vito to quit, replaces the files and starts the new one ([Run] in vito.iss
// has no skipifsilent for exactly this).
type winInstaller struct{}

func (winInstaller) asset(rel *Release) string { return rel.Installer }

func (winInstaller) install(path string) error {
	if err := Apply(path); err != nil {
		return err
	}
	os.Exit(0) // out of the installer's way; it cannot replace a running exe
	return nil
}

// platformInstaller updates only a copy the installer put there: one with its
// uninstaller beside it. A loose vito.exe would get a second, installed copy
// next to it instead of being replaced.
func platformInstaller() (installer, string) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err.Error()
	}
	if m, _ := filepath.Glob(filepath.Join(filepath.Dir(exe), "unins*.exe")); len(m) == 0 {
		return nil, "not installed with the Vito installer"
	}
	if strings.Contains(strings.ToLower(exe), `\windowsapps\`) {
		return nil, "installed from a store"
	}
	return winInstaller{}, ""
}
