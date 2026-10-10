//go:build linux

package xdgportal

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// Backend names the implementation ("gnome", "kde", "hyprland", …) that
// xdg-desktop-portal routes an interface to, such as
// "org.freedesktop.impl.portal.GlobalShortcuts", or "" when that cannot be
// worked out — inside a Flatpak, for one, the host's configuration is out of
// sight.
//
// It exists because the frontend's own answer is useless here: it advertises
// every interface whatever sits behind it (see docs/linux-portals.md). Knowing
// the backend lets a caller recognise combinations known not to work — the
// GNOME backend outside a GNOME Shell session — without first putting a
// dialog in front of the user to find out.
//
// It follows the frontend's rules (portals.conf(5)): the first portals.conf
// found decides, desktop-specific before generic, per directory; without one,
// the first .portal file whose UseIn names the desktop.
func Backend(iface string) string {
	desktops := currentDesktops()
	for _, dir := range configDirs() {
		names := make([]string, 0, len(desktops)+1)
		for _, d := range desktops {
			names = append(names, d+"-portals.conf")
		}
		names = append(names, "portals.conf")
		for _, n := range names {
			pref, ok := readPreferred(filepath.Join(dir, n))
			if !ok {
				continue
			}
			list, found := pref[iface]
			if !found {
				list = pref["default"]
			}
			for _, b := range strings.Split(list, ";") {
				b = strings.TrimSpace(b)
				switch b {
				case "", "*":
					continue
				case "none":
					return ""
				}
				if implements(b, iface) {
					return b
				}
			}
			return ""
		}
	}
	// No configuration at all: the frontend's legacy fallback.
	for _, dir := range dataDirs() {
		files, _ := filepath.Glob(filepath.Join(dir, "xdg-desktop-portal", "portals", "*.portal"))
		for _, f := range files {
			p := readPortalFile(f)
			if !hasItem(p["Interfaces"], iface) {
				continue
			}
			for _, use := range strings.Split(p["UseIn"], ";") {
				for _, d := range desktops {
					if strings.EqualFold(strings.TrimSpace(use), d) {
						return strings.TrimSuffix(filepath.Base(f), ".portal")
					}
				}
			}
		}
	}
	return ""
}

// The frontend's compiled-in sysconfdir and datadir; variables for the tests.
var (
	sysConfDir = "/etc"
	sysDataDir = "/usr/share"
)

func currentDesktops() []string {
	var out []string
	for _, d := range strings.Split(os.Getenv("XDG_CURRENT_DESKTOP"), ":") {
		if d = strings.ToLower(strings.TrimSpace(d)); d != "" {
			out = append(out, d)
		}
	}
	return out
}

// configDirs lists where portals.conf may live, in the frontend's search order.
func configDirs() []string {
	var dirs []string
	add := func(base string) {
		if base != "" {
			dirs = append(dirs, filepath.Join(base, "xdg-desktop-portal"))
		}
	}
	if h := os.Getenv("XDG_CONFIG_HOME"); h != "" {
		add(h)
	} else if home, err := os.UserHomeDir(); err == nil {
		add(filepath.Join(home, ".config"))
	}
	for _, d := range splitPath(os.Getenv("XDG_CONFIG_DIRS"), "/etc/xdg") {
		add(d)
	}
	add(sysConfDir)
	for _, d := range dataDirs() {
		add(d)
	}
	return dirs
}

func dataDirs() []string {
	var dirs []string
	if h := os.Getenv("XDG_DATA_HOME"); h != "" {
		dirs = append(dirs, h)
	} else if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".local", "share"))
	}
	dirs = append(dirs, splitPath(os.Getenv("XDG_DATA_DIRS"), "/usr/local/share:/usr/share")...)
	return append(dirs, sysDataDir)
}

func splitPath(v, def string) []string {
	if v == "" {
		v = def
	}
	var out []string
	for _, p := range strings.Split(v, ":") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// readPreferred returns the [preferred] section of a portals.conf.
func readPreferred(path string) (map[string]string, bool) {
	sections, ok := readINI(path)
	if !ok {
		return nil, false
	}
	return sections["preferred"], true
}

// implements reports whether the named backend's .portal file lists iface.
func implements(backend, iface string) bool {
	for _, dir := range dataDirs() {
		f := filepath.Join(dir, "xdg-desktop-portal", "portals", backend+".portal")
		if _, err := os.Stat(f); err == nil {
			return hasItem(readPortalFile(f)["Interfaces"], iface)
		}
	}
	return false
}

func readPortalFile(path string) map[string]string {
	sections, _ := readINI(path)
	return sections["portal"]
}

func hasItem(list, item string) bool {
	for _, s := range strings.Split(list, ";") {
		if strings.TrimSpace(s) == item {
			return true
		}
	}
	return false
}

// readINI is just enough of a key file parser for portal configuration:
// section names are lowercased, keys keep their case.
func readINI(path string) (map[string]map[string]string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	out := map[string]map[string]string{}
	var cur map[string]string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		if line[0] == '[' && line[len(line)-1] == ']' {
			name := strings.ToLower(line[1 : len(line)-1])
			cur = map[string]string{}
			out[name] = cur
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok && cur != nil {
			cur[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out, true
}
