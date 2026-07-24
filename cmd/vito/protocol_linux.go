//go:build linux

package main

import (
	"os"
	"os/exec"
	"path/filepath"

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
// still resolves after a reboot. The entry is deliberately NOT NoDisplay — an
// earlier hidden version shadowed the launcher entry that AppImage integrators
// (e.g. GearLever) or a distro package provide, making Vito vanish from the menu.
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
Categories=Utility;Accessibility;AudioVideo;
Keywords=dictation;speech;transcription;voice;
StartupNotify=false
X-GNOME-UsesNotifications=true
MimeType=x-scheme-handler/vito;
`
	if err := os.WriteFile(filepath.Join(appsDir, "vito.desktop"), []byte(desktop), 0o644); err != nil {
		return
	}
	// Best effort: register the scheme handler and refresh the desktop DB so the
	// new entry appears in the menu without a re-login.
	_ = exec.Command("xdg-mime", "default", "vito.desktop", "x-scheme-handler/vito").Run()
	_ = exec.Command("update-desktop-database", appsDir).Run()
}
