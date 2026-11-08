//go:build linux

package pill

import (
	"embed"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Vito installs its GNOME Shell extension itself: the files go to the user's
// extension folder and GNOME is told to enable it. GNOME reads that folder
// only when the session starts, so a first install works after logging out and
// in once; GnomeExtension reports that state for the settings page.

//go:embed gnome/extension.js gnome/metadata.json
var gnomeFiles embed.FS

const gnomeUUID = "vito-pill@vito.talk"

// Extension states, for /api/linux-tools.
const (
	GnomeExtNone      = ""          // not GNOME, or the pill has its own window here
	GnomeExtRunning   = "running"   // drawing the pill
	GnomeExtRelogin   = "relogin"   // installed; GNOME starts it at the next login
	GnomeExtDisabled  = "disabled"  // the user switched extensions (or this one) off
	GnomeExtSandboxed = "sandboxed" // a Flatpak cannot write there
	GnomeExtFailed    = "failed"    // installing did not work
)

func isGNOME() bool {
	return strings.Contains(strings.ToLower(os.Getenv("XDG_CURRENT_DESKTOP")), "gnome")
}

func gnomeExtDir() string {
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dir, "gnome-shell", "extensions", gnomeUUID)
}

func embeddedVersion() int {
	b, _ := gnomeFiles.ReadFile("gnome/metadata.json")
	var m struct{ Version int }
	json.Unmarshal(b, &m)
	return m.Version
}

func installedVersion() int {
	b, err := os.ReadFile(filepath.Join(gnomeExtDir(), "metadata.json"))
	if err != nil {
		return 0
	}
	var m struct{ Version int }
	json.Unmarshal(b, &m)
	return m.Version
}

// ensureGnomeExtension puts the current extension in place on GNOME and
// enables it. Nothing to do elsewhere, or when it is already there.
func ensureGnomeExtension(log *slog.Logger) {
	if !isGNOME() || available() {
		return
	}
	if _, err := os.Stat("/.flatpak-info"); err == nil {
		return
	}
	if installedVersion() != embeddedVersion() {
		dir := gnomeExtDir()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Warn("could not install the GNOME extension for the pill", "err", err)
			return
		}
		for _, name := range []string{"extension.js", "metadata.json"} {
			b, _ := gnomeFiles.ReadFile("gnome/" + name)
			tmp := filepath.Join(dir, "."+name+".tmp")
			if err := os.WriteFile(tmp, b, 0o644); err != nil {
				log.Warn("could not install the GNOME extension for the pill", "err", err)
				return
			}
			os.Rename(tmp, filepath.Join(dir, name))
		}
		log.Info("installed the GNOME extension for the pill", "dir", dir)
	}
	if err := exec.Command("gnome-extensions", "enable", gnomeUUID).Run(); err != nil {
		// A shell that has not seen the extension yet refuses; the enabled
		// list is what it reads at the next login.
		enableAtLogin(log)
	}
}

// enableAtLogin adds the extension to GNOME's list of enabled extensions.
func enableAtLogin(log *slog.Logger) {
	out, err := exec.Command("gsettings", "get", "org.gnome.shell", "enabled-extensions").Output()
	if err != nil {
		log.Warn("could not enable the GNOME extension for the pill", "err", err)
		return
	}
	cur := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(out)), "@as"))
	if strings.Contains(cur, "'"+gnomeUUID+"'") {
		return
	}
	inner := strings.TrimSpace(strings.Trim(cur, "[]"))
	next := "['" + gnomeUUID + "']"
	if inner != "" {
		next = "[" + inner + ", '" + gnomeUUID + "']"
	}
	if err := exec.Command("gsettings", "set", "org.gnome.shell", "enabled-extensions", next).Run(); err != nil {
		log.Warn("could not enable the GNOME extension for the pill", "err", err)
	}
}

// GnomeExtension reports where the GNOME extension stands (GnomeExt*).
func GnomeExtension() string {
	if !isGNOME() || available() {
		return GnomeExtNone
	}
	if c := gnomePill(); c != nil {
		// An update waits for the next login too: GNOME keeps the old code.
		if v, err := c.obj.GetProperty(gnomeIface + ".Version"); err == nil {
			if u, ok := v.Value().(uint32); ok && int(u) < embeddedVersion() {
				return GnomeExtRelogin
			}
		}
		return GnomeExtRunning
	}
	if _, err := os.Stat("/.flatpak-info"); err == nil {
		return GnomeExtSandboxed
	}
	if installedVersion() == 0 {
		return GnomeExtFailed
	}
	if out, err := exec.Command("gsettings", "get", "org.gnome.shell", "disable-user-extensions").Output(); err == nil && strings.TrimSpace(string(out)) == "true" {
		return GnomeExtDisabled
	}
	if out, err := exec.Command("gsettings", "get", "org.gnome.shell", "disabled-extensions").Output(); err == nil && strings.Contains(string(out), gnomeUUID) {
		return GnomeExtDisabled
	}
	return GnomeExtRelogin
}
