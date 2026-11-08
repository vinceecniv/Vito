//go:build linux

package hotkey

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/godbus/dbus/v5"
)

// CapsAsMenu makes Caps Lock reach the desktop as Menu (the XKB option
// caps:menu), or stops it. A desktop updates its keyboard state before it
// looks for shortcuts, so bound as itself Caps Lock would still lock capitals;
// as Menu it locks nothing and can be bound like any key. Each desktop keeps
// the option in its own place: KDE in kxkbrc, GNOME in its settings, Hyprland
// at runtime. niri keeps it in the user's config.kdl, which Vito changes only
// when asked (SetNiriCaps).
func CapsAsMenu(on bool) error {
	desktop := strings.ToLower(os.Getenv("XDG_CURRENT_DESKTOP"))
	switch {
	case strings.Contains(desktop, "kde"):
		return capsKDE(on)
	case strings.Contains(desktop, "gnome"):
		return capsGNOME(on)
	case strings.Contains(desktop, "cinnamon"):
		return capsCinnamon(on)
	case os.Getenv("HYPRLAND_INSTANCE_SIGNATURE") != "":
		return capsHyprland(on)
	}
	if os.Getenv("DISPLAY") != "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return capsX11(on)
	}
	return fmt.Errorf("set caps:menu in the desktop's keyboard options")
}

// withOption adds or removes caps:menu in a comma-separated XKB option list.
func withOption(list string, on bool) string {
	var out []string
	for _, o := range strings.Split(list, ",") {
		if o = strings.TrimSpace(o); o != "" && o != "caps:menu" && !(on && strings.HasPrefix(o, "caps:")) {
			out = append(out, o)
		}
	}
	if on {
		out = append(out, "caps:menu")
	}
	return strings.Join(out, ",")
}

func capsKDE(on bool) error {
	dir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	b, _ := os.ReadFile(filepath.Join(dir, "kxkbrc"))
	cur := ""
	inLayout := false
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "[") {
			inLayout = l == "[Layout]"
		} else if inLayout && strings.HasPrefix(l, "Options=") {
			cur = strings.TrimPrefix(l, "Options=")
		}
	}
	next := withOption(cur, on)
	if next == cur {
		return nil
	}
	tool := "kwriteconfig6"
	if _, err := exec.LookPath(tool); err != nil {
		tool = "kwriteconfig5"
	}
	for _, kv := range [][2]string{{"ResetOldOptions", "true"}, {"Options", next}} {
		if err := exec.Command(tool, "--file", "kxkbrc", "--group", "Layout", "--key", kv[0], kv[1]).Run(); err != nil {
			return fmt.Errorf("%s: %w", tool, err)
		}
	}
	// The keyboard module rereads the file when told.
	conn, err := dbus.SessionBus()
	if err != nil {
		return err
	}
	return conn.Emit("/Layouts", "org.kde.keyboard.reloadConfig")
}

func capsGNOME(on bool) error { return capsGsettings("org.gnome.desktop.input-sources", on) }

// capsCinnamon: Cinnamon 6.4 and later keep the options where GNOME does, in
// their own schema; older ones in libgnomekbd's, as "group<TAB>option".
func capsCinnamon(on bool) error {
	if exec.Command("gsettings", "get", "org.cinnamon.desktop.input-sources", "xkb-options").Run() == nil {
		return capsGsettings("org.cinnamon.desktop.input-sources", on)
	}
	out, err := exec.Command("gsettings", "get", "org.gnome.libgnomekbd.keyboard", "options").Output()
	if err != nil {
		return capsX11(on)
	}
	var keep []string
	for _, o := range parseStrv(string(out)) {
		if !strings.HasPrefix(o, "caps\t") {
			keep = append(keep, o)
		}
	}
	if on {
		keep = append(keep, "caps\tcaps:menu")
	}
	return exec.Command("gsettings", "set", "org.gnome.libgnomekbd.keyboard", "options", strv(keep)).Run()
}

// capsX11 sets the option on the X server directly: any X11 desktop, until
// its own keyboard settings are applied again.
func capsX11(on bool) error {
	out, err := exec.Command("setxkbmap", "-query").Output()
	if err != nil {
		return fmt.Errorf("setxkbmap: %w", err)
	}
	var cur string
	for _, line := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == "options" {
			cur = strings.TrimSpace(v)
		}
	}
	next := withOption(cur, on)
	if next == cur {
		return nil
	}
	return exec.Command("setxkbmap", "-option", "", "-option", next).Run()
}

// parseStrv reads a GVariant string array as gsettings prints it.
func parseStrv(s string) []string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "@as ")
	s = strings.Trim(s, "[]")
	var list []string
	for _, o := range strings.Split(s, ",") {
		if o = strings.Trim(strings.TrimSpace(o), "'"); o != "" {
			list = append(list, strings.ReplaceAll(o, "\\t", "\t"))
		}
	}
	return list
}

func strv(list []string) string {
	var quoted []string
	for _, o := range list {
		quoted = append(quoted, "'"+strings.ReplaceAll(o, "\t", "\\t")+"'")
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

func capsGsettings(schema string, on bool) error {
	out, err := exec.Command("gsettings", "get", schema, "xkb-options").Output()
	if err != nil {
		return fmt.Errorf("gsettings: %w", err)
	}
	// A GVariant string array: ['a', 'b'] or @as [].
	cur := strings.TrimSpace(string(out))
	cur = strings.TrimPrefix(cur, "@as ")
	cur = strings.Trim(cur, "[]")
	var list []string
	for _, o := range strings.Split(cur, ",") {
		if o = strings.Trim(strings.TrimSpace(o), "'"); o != "" {
			list = append(list, o)
		}
	}
	joined := strings.Join(list, ",")
	next := withOption(joined, on)
	if next == joined {
		return nil
	}
	var quoted []string
	for _, o := range strings.Split(next, ",") {
		if o != "" {
			quoted = append(quoted, "'"+o+"'")
		}
	}
	return exec.Command("gsettings", "set", schema, "xkb-options", "["+strings.Join(quoted, ", ")+"]").Run()
}

// capsHyprland sets caps:menu and binds Menu's press and release to the
// signals Gesture reads (cmd/vito/signals_unix.go), all at runtime: nothing in
// the user's own config changes, and Vito sets it again at every start (a
// config reload drops it; the settings page shows the lines to keep it).
// Hyprland 0.55 and later take Lua (hyprctl eval); older ones keywords.
func capsHyprland(on bool) error {
	pid := strconv.Itoa(os.Getpid())
	if out, err := exec.Command("hyprctl", "eval", "return 1").CombinedOutput(); err == nil && strings.TrimSpace(string(out)) == "ok" {
		// The binds are kept in a Lua global, so a later start can switch the
		// previous ones (aimed at an old pid) off first.
		lua := `for _, b in ipairs(_G.vito_caps or {}) do b:set_enabled(false) end; _G.vito_caps = {}`
		if on {
			lua += `; hl.config({ input = { kb_options = "caps:menu" } })` +
				`; _G.vito_caps = { hl.bind("Menu", hl.dsp.exec_cmd("kill -s RTMIN+1 ` + pid + `")),` +
				` hl.bind("Menu", hl.dsp.exec_cmd("kill -s RTMIN+2 ` + pid + `"), { release = true }) }`
		} else {
			lua += `; hl.config({ input = { kb_options = "" } })`
		}
		return exec.Command("hyprctl", "eval", lua).Run()
	}
	run := func(args ...string) error { return exec.Command("hyprctl", args...).Run() }
	if !on {
		run("keyword", "unbind", ", Menu")
		return run("keyword", "input:kb_options", "")
	}
	if err := run("keyword", "input:kb_options", "caps:menu"); err != nil {
		return err
	}
	run("keyword", "unbind", ", Menu")
	if err := run("keyword", "bind", ", Menu, exec, kill -s RTMIN+1 "+pid); err != nil {
		return err
	}
	return run("keyword", "bindr", ", Menu, exec, kill -s RTMIN+2 "+pid)
}
