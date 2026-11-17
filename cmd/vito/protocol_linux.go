//go:build linux

package main

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"vito/internal/selfexe"
	"vito/web"
)

// registerLaunchProtocol installs Vito's XDG desktop integration: a visible
// launcher entry — so Vito shows up in the application menu with its icon — that
// doubles as the "vito://" URL-scheme handler the web UI uses to relaunch the
// daemon when it's down.
//
// It is rewritten on every start so the Exec path stays valid: selfexe prefers
// $APPIMAGE (a stable location) over the throwaway squashfs mount, so the entry
// still resolves after a reboot. The entry is NoDisplay only when another
// visible entry already starts this same file (otherLauncher): an AppImage
// manager (DMS's Software Depot, Gear Lever, AppImageLauncher) writes its own
// under its own id, and the menu then listed Vito twice. Hidden, the entry
// still names the icon a dock finds by the window's app id ("vito"). Always
// hiding it was tried once: a distro package's vito.desktop has the same id,
// so a hidden user copy made Vito vanish from the menu.
func registerLaunchProtocol() {
	exe, err := selfexe.Path()
	if err != nil {
		return
	}
	dataDir := os.Getenv("XDG_DATA_HOME")
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return
		}
		dataDir = filepath.Join(home, ".local", "share")
	}

	// Icon, written from the embedded 512px PNG, so the launcher entry has an
	// image no matter how Vito was installed (AppImage, package or plain binary).
	iconDir := filepath.Join(dataDir, "icons", "hicolor", "512x512", "apps")
	if err := os.MkdirAll(iconDir, 0o755); err == nil {
		_ = os.WriteFile(filepath.Join(iconDir, "vito.png"), web.Icon512, 0o644)
	}

	appsDir := filepath.Join(dataDir, "applications")
	if err := os.MkdirAll(appsDir, 0o755); err != nil {
		return
	}
	desktop := `[Desktop Entry]
Type=Application
Name=Vito
GenericName=Voice dictation
Comment=Voice In, Text Out — press a hotkey, speak, and your words are pasted as clean text
Comment[nl]=Voice In, Text Out — druk een sneltoets, spreek, en je woorden worden als schone tekst geplakt
Exec="` + exe + `" serve %u
Icon=vito
Terminal=false
Categories=Utility;Accessibility;
Keywords=dictation;speech;transcription;voice;
StartupNotify=false
X-GNOME-UsesNotifications=true
MimeType=x-scheme-handler/vito;
`
	if otherLauncher(exe, dataDir) {
		desktop += "NoDisplay=true\n"
	}
	if err := os.WriteFile(filepath.Join(appsDir, "vito.desktop"), []byte(desktop), 0o644); err != nil {
		return
	}
	// Best effort: register the scheme handler and refresh the desktop DB so the
	// new entry appears in the menu without a re-login.
	_ = exec.Command("xdg-mime", "default", "vito.desktop", "x-scheme-handler/vito").Run()
	_ = exec.Command("update-desktop-database", appsDir).Run()
}

// otherLauncher reports whether a visible desktop entry other than Vito's own
// vito.desktop starts exe — the entry an AppImage manager wrote.
func otherLauncher(exe, dataDir string) bool {
	dirs := []string{dataDir}
	sys := os.Getenv("XDG_DATA_DIRS")
	if sys == "" {
		sys = "/usr/local/share:/usr/share"
	}
	dirs = append(dirs, filepath.SplitList(sys)...)
	for _, d := range dirs {
		files, _ := filepath.Glob(filepath.Join(d, "applications", "*.desktop"))
		for _, f := range files {
			if filepath.Base(f) != "vito.desktop" && launches(f, exe) {
				return true
			}
		}
	}
	return false
}

// launches reports whether the desktop entry in file is shown in menus and its
// Exec runs exe.
func launches(file, exe string) bool {
	f, err := os.Open(file)
	if err != nil {
		return false
	}
	defer f.Close()
	runs, inEntry := false, false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(l, "[") {
			inEntry = l == "[Desktop Entry]"
			continue
		}
		if !inEntry {
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "NoDisplay", "Hidden":
			if strings.TrimSpace(v) == "true" {
				return false
			}
		case "Exec":
			fields := strings.Fields(strings.TrimSpace(v))
			if len(fields) > 0 && strings.Trim(fields[0], `"'`) == exe {
				runs = true
			} else if strings.HasPrefix(strings.TrimSpace(v), `"`+exe+`"`) {
				runs = true // a quoted path with spaces
			}
		}
	}
	return runs
}
