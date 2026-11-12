package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"vito/internal/tray"
)

// The launcher's icon follows Vito's look (ui.accent, ui.radius), as the
// tray's does and as the window's taskbar icon does on Windows. On Wayland a
// dock shows the icon of the desktop entry a window's app id names, and a
// window cannot bring its own, so the entry's Icon= is what changes: an icon
// drawn in the colour theme's gradient and corners (tray.BrandPNG), named
// after its picture so a desktop that caches icons sees a new one. The entry
// names it by its full path: GNOME Shell did not find a new file in the icon
// theme until it rescanned, and showed a generic icon meanwhile.

func userDataDir() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share")
}

var themedIcon = regexp.MustCompile(`^vito-[0-9a-f]{10}\.png$`)

// launcherIcon writes the icon for this look and returns its name: "vito"
// for Vito's own colours and corners. Only the daemon prunes the older ones
// (the window is another process and leaves them).
func launcherIcon(accent, radius string, prune bool) string {
	if accent == "" && radius == "" {
		return "vito"
	}
	data := userDataDir()
	if data == "" {
		return "vito"
	}
	png := tray.BrandPNG(accent, radius, 512)
	sum := sha256.Sum256(png)
	name := "vito-" + hex.EncodeToString(sum[:5])
	dir := filepath.Join(data, "icons", "hicolor", "512x512", "apps")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "vito"
	}
	file := filepath.Join(dir, name+".png")
	if _, err := os.Stat(file); err != nil {
		if os.WriteFile(file+".tmp", png, 0o644) != nil || os.Rename(file+".tmp", file) != nil {
			return "vito"
		}
	}
	// The looks before this one go; one other stays, for a desktop still
	// showing it while it reads the entry again.
	if old, _ := os.ReadDir(dir); prune && len(old) > 0 {
		for _, e := range old {
			if themedIcon.MatchString(e.Name()) && e.Name() != name+".png" && e.Name() != prevIcon+".png" {
				_ = os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
	return name
}

var (
	iconMu   sync.Mutex // one refresh at a time: a page saves colours and corners apart
	prevIcon string
)

// refreshLauncherIcon points Vito's desktop entry at the icon for this look.
// Only the entry Vito writes itself (registerLaunchProtocol) is changed.
func refreshLauncherIcon(accent, radius string) {
	iconMu.Lock()
	defer iconMu.Unlock()
	name := launcherIcon(accent, radius, true)
	if name != "vito" {
		name = filepath.Join(userDataDir(), "icons", "hicolor", "512x512", "apps", name+".png")
	}
	file := filepath.Join(userDataDir(), "applications", "vito.desktop")
	b, err := os.ReadFile(file)
	if err != nil || !strings.Contains(string(b), "x-scheme-handler/vito") {
		return
	}
	lines := strings.Split(string(b), "\n")
	changed := false
	for i, l := range lines {
		if strings.HasPrefix(l, "Icon=") {
			if old := strings.TrimPrefix(l, "Icon="); old != name {
				prevIcon = strings.TrimSuffix(filepath.Base(old), ".png")
				lines[i] = "Icon=" + name
				changed = true
			}
			break
		}
	}
	if !changed {
		return
	}
	tmp := file + ".tmp"
	if os.WriteFile(tmp, []byte(strings.Join(lines, "\n")), 0o644) == nil {
		_ = os.Rename(tmp, file)
	}
}
