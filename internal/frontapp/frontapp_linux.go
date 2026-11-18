//go:build linux

package frontapp

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jezek/xgb/xproto"

	"vito/internal/x11"
)

// On Linux each compositor answers in its own way: niri and Hyprland over
// their IPC, an X11 session through _NET_ACTIVE_WINDOW, GNOME through Vito's
// Shell extension and KDE through a KWin script Vito loads (desktop_linux.go).
func current() App {
	var id string
	switch {
	case os.Getenv("NIRI_SOCKET") != "":
		id = niriApp()
	case os.Getenv("HYPRLAND_INSTANCE_SIGNATURE") != "":
		id = hyprApp()
	case os.Getenv("XDG_SESSION_TYPE") == "x11":
		id = x11App()
	case desktopIs("gnome"):
		var name string
		if id, name = gnomeApp(); id != "" && name != "" {
			keepIcon(strings.ToLower(id), cachedName("icon:"+strings.ToLower(id), func() string { _, ic := desktopEntry(id); return ic }))
			return App{ID: id, Name: name}
		}
	case desktopIs("kde"):
		id = kdeApp()
	}
	if id == "" {
		return App{}
	}
	keepIcon(strings.ToLower(id), cachedName("icon:"+strings.ToLower(id), func() string { _, ic := desktopEntry(id); return ic }))
	return App{ID: id, Name: cachedName(strings.ToLower(id), func() string { return desktopName(id) })}
}

func niriApp() string {
	conn, err := net.DialTimeout("unix", os.Getenv("NIRI_SOCKET"), time.Second)
	if err != nil {
		return ""
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(time.Second))
	if _, err := conn.Write([]byte("\"FocusedWindow\"\n")); err != nil {
		return ""
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return ""
	}
	var r struct {
		Ok *struct {
			FocusedWindow *struct {
				AppID string `json:"app_id"`
			}
		}
	}
	if json.Unmarshal(line, &r) != nil || r.Ok == nil || r.Ok.FocusedWindow == nil {
		return ""
	}
	return r.Ok.FocusedWindow.AppID
}

func hyprApp() string {
	sock := filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "hypr", os.Getenv("HYPRLAND_INSTANCE_SIGNATURE"), ".socket.sock")
	conn, err := net.DialTimeout("unix", sock, time.Second)
	if err != nil {
		return ""
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(time.Second))
	if _, err := conn.Write([]byte("j/activewindow")); err != nil {
		return ""
	}
	b, err := io.ReadAll(conn)
	if err != nil {
		return ""
	}
	var w struct{ Class string }
	if json.Unmarshal(b, &w) != nil {
		return ""
	}
	return w.Class
}

// x11App reads the active window's WM_CLASS: instance and class, NUL apart;
// the class is the program's.
func x11App() string {
	c, err := x11.Open()
	if err != nil {
		return ""
	}
	defer c.Close()
	v := c.Cardinals(c.Root, "_NET_ACTIVE_WINDOW")
	if len(v) == 0 || v[0] == 0 {
		return ""
	}
	p := c.Property(xproto.Window(v[0]), "WM_CLASS")
	if p == nil || len(p.Value) == 0 {
		return ""
	}
	parts := strings.Split(strings.TrimRight(string(p.Value), "\x00"), "\x00")
	return parts[len(parts)-1]
}

// desktopName finds what the program's .desktop file calls it: the file named
// after the id, else the one whose StartupWMClass is the id; else the id.
func desktopName(id string) string {
	if n, _ := desktopEntry(id); n != "" {
		return n
	}
	return id
}

// dataDirs are the XDG data folders, the user's first, Flatpak's exports last.
func dataDirs() []string {
	var dirs []string
	home, _ := os.UserHomeDir()
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		dirs = append(dirs, d)
	} else if home != "" {
		dirs = append(dirs, filepath.Join(home, ".local/share"))
	}
	data := os.Getenv("XDG_DATA_DIRS")
	if data == "" {
		data = "/usr/local/share:/usr/share"
	}
	dirs = append(dirs, filepath.SplitList(data)...)
	dirs = append(dirs, "/var/lib/flatpak/exports/share")
	if home != "" {
		dirs = append(dirs, filepath.Join(home, ".local/share/flatpak/exports/share"))
	}
	return dirs
}

// desktopEntry finds the program's .desktop file — the one named after the
// id, else the one whose StartupWMClass is the id — and gives its Name= and
// Icon=.
func desktopEntry(id string) (name, icon string) {
	dirs := dataDirs()
	for _, d := range dirs {
		for _, f := range []string{id, strings.ToLower(id)} {
			if n, ic := entryName(filepath.Join(d, "applications", f+".desktop"), ""); n != "" {
				return n, ic
			}
		}
	}
	for _, d := range dirs {
		files, _ := filepath.Glob(filepath.Join(d, "applications", "*.desktop"))
		for _, f := range files {
			if n, ic := entryName(f, id); n != "" {
				return n, ic
			}
		}
	}
	return "", ""
}

// entryName reads Name= and Icon= from a desktop entry; with wmClass set,
// only when its StartupWMClass is that (any case).
func entryName(path, wmClass string) (string, string) {
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	var name, class, icon string
	inEntry := false
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
		if v, ok := strings.CutPrefix(l, "Name="); ok && name == "" {
			name = v
		} else if v, ok := strings.CutPrefix(l, "StartupWMClass="); ok {
			class = v
		} else if v, ok := strings.CutPrefix(l, "Icon="); ok && icon == "" {
			icon = strings.TrimSpace(v)
		}
	}
	if wmClass != "" && !strings.EqualFold(class, wmClass) {
		return "", ""
	}
	return name, icon
}

// iconData reads a desktop entry's Icon=: a path, or a name looked up in the
// hicolor theme (which every icon theme falls back to), largest bitmap first,
// then the scalable one, then pixmaps.
func iconData(icon string) ([]byte, string) {
	var cands []string
	if filepath.IsAbs(icon) {
		cands = []string{icon}
	} else {
		for _, d := range dataDirs() {
			for _, sz := range []string{"256x256", "128x128", "96x96", "64x64", "48x48"} {
				cands = append(cands, filepath.Join(d, "icons/hicolor", sz, "apps", icon+".png"))
			}
			cands = append(cands, filepath.Join(d, "icons/hicolor/scalable/apps", icon+".svg"))
		}
		for _, d := range dataDirs() {
			cands = append(cands, filepath.Join(d, "pixmaps", icon+".png"), filepath.Join(d, "pixmaps", icon+".svg"))
		}
	}
	for _, c := range cands {
		ext := strings.ToLower(filepath.Ext(c))
		if ext != ".png" && ext != ".svg" {
			continue
		}
		if st, err := os.Stat(c); err != nil || st.Size() == 0 || st.Size() > 2<<20 {
			continue
		}
		if b, err := os.ReadFile(c); err == nil {
			return b, ext
		}
	}
	return nil, ""
}
