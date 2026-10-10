//go:build linux

package xdgportal

import (
	"os"
	"path/filepath"
	"testing"
)

const gsImpl = "org.freedesktop.impl.portal.GlobalShortcuts"

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// isolate points every XDG directory into a temp dir, so the host's own
// portal configuration cannot leak into the test.
func isolate(t *testing.T, desktop string) (config, data string) {
	root := t.TempDir()
	config, data = filepath.Join(root, "config"), filepath.Join(root, "data")
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("XDG_CONFIG_DIRS", filepath.Join(root, "nowhere"))
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("XDG_DATA_DIRS", filepath.Join(root, "nowhere"))
	t.Setenv("XDG_CURRENT_DESKTOP", desktop)
	oldConf, oldData := sysConfDir, sysDataDir
	sysConfDir, sysDataDir = filepath.Join(root, "etc"), filepath.Join(root, "usr")
	t.Cleanup(func() { sysConfDir, sysDataDir = oldConf, oldData })
	writeFile(t, filepath.Join(data, "xdg-desktop-portal/portals/gnome.portal"),
		"[portal]\nDBusName=org.freedesktop.impl.portal.desktop.gnome\nInterfaces=org.freedesktop.impl.portal.GlobalShortcuts;org.freedesktop.impl.portal.Screenshot;\nUseIn=gnome\n")
	writeFile(t, filepath.Join(data, "xdg-desktop-portal/portals/gtk.portal"),
		"[portal]\nInterfaces=org.freedesktop.impl.portal.FileChooser;\nUseIn=*\n")
	writeFile(t, filepath.Join(data, "xdg-desktop-portal/portals/hyprland.portal"),
		"[portal]\nInterfaces=org.freedesktop.impl.portal.GlobalShortcuts;\nUseIn=Hyprland\n")
	return config, data
}

func TestBackendFromDesktopConfig(t *testing.T) {
	config, _ := isolate(t, "niri")
	writeFile(t, filepath.Join(config, "xdg-desktop-portal/niri-portals.conf"),
		"[preferred]\ndefault=gnome;gtk;\norg.freedesktop.impl.portal.Access=gtk;\n")
	if got := Backend(gsImpl); got != "gnome" {
		t.Fatalf("got %q, want gnome", got)
	}
}

func TestBackendSkipsBackendsWithoutTheInterface(t *testing.T) {
	config, _ := isolate(t, "niri")
	writeFile(t, filepath.Join(config, "xdg-desktop-portal/portals.conf"),
		"[preferred]\ndefault=gtk;gnome;\n")
	if got := Backend(gsImpl); got != "gnome" {
		t.Fatalf("got %q, want gnome", got)
	}
}

func TestBackendInterfaceKeyWins(t *testing.T) {
	config, _ := isolate(t, "niri")
	writeFile(t, filepath.Join(config, "xdg-desktop-portal/portals.conf"),
		"[preferred]\ndefault=gnome;\norg.freedesktop.impl.portal.GlobalShortcuts=none\n")
	if got := Backend(gsImpl); got != "" {
		t.Fatalf("got %q, want none", got)
	}
}

func TestBackendFallsBackToUseIn(t *testing.T) {
	isolate(t, "Hyprland")
	if got := Backend(gsImpl); got != "hyprland" {
		t.Fatalf("got %q, want hyprland", got)
	}
}
