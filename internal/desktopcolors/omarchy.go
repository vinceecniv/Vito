package desktopcolors

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

var omarchy = &source{running: omarchyRunning, paths: omarchyPaths, read: readOmarchy}

// Omarchy returns the colours of the active Omarchy theme.
func Omarchy() (*Palette, error) { return omarchy.get() }

// omarchyPaths: Omarchy 3 keeps the active theme under ~/.local/state, older
// releases under ~/.config. Both are a link (or copy) named current/theme.
func omarchyPaths() []string {
	return []string{
		homeFile(".local", "state", "omarchy", "current", "theme", "colors.toml"),
		homeFile(".config", "omarchy", "current", "theme", "colors.toml"),
	}
}

// omarchyRunning: an Omarchy session sets OMARCHY_PATH (its uwsm env). A
// Hyprland session with Omarchy installed counts too, for a daemon started
// without that environment.
func omarchyRunning() bool {
	if inFlatpak() || os.Getenv("OMARCHY_PATH") != "" {
		return true
	}
	if os.Getenv("HYPRLAND_INSTANCE_SIGNATURE") == "" {
		return false
	}
	_, err := os.Stat(homeFile(".local", "share", "omarchy"))
	return err == nil
}

// readOmarchy maps a theme's colors.toml onto the Material roles. The file
// is flat key = "#rrggbb" lines, so it needs no TOML library.
func readOmarchy(path string) (*Palette, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	c := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		// A quoted value ends at its closing quote; a comment may follow.
		if q := v[:min(1, len(v))]; q == `"` || q == "'" {
			if j := strings.Index(v[1:], q); j >= 0 {
				v = v[1 : j+1]
			}
		}
		c[k] = v
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	get := func(keys ...string) string {
		for _, k := range keys {
			if reHex.MatchString(c[k]) {
				return strings.ToLower(c[k])
			}
		}
		return ""
	}
	bg, fg, accent := get("background"), get("foreground"), get("accent", "blue")
	if bg == "" || fg == "" || accent == "" {
		return nil, errors.New("colors.toml: background, foreground or accent missing")
	}
	or := func(v, fallback string) string {
		if v == "" {
			return fallback
		}
		return v
	}
	dimFg := or(get("dark_foreground"), fg)
	high := or(get("lighter_background"), bg)
	side := map[string]string{
		"primary":                   accent,
		"secondary":                 or(get("magenta", "bright_magenta", "cyan"), accent),
		"tertiary":                  or(get("cyan", "bright_cyan"), accent),
		"on_primary":                bg,
		"surface":                   bg,
		"surface_container_lowest":  or(get("darker_background"), bg),
		"surface_container_low":     or(get("dark_background"), bg),
		"surface_container":         bg,
		"surface_container_high":    high,
		"surface_container_highest": or(get("selection"), high),
		"on_surface":                fg,
		// Secondary text: dark_foreground is a comment colour, too faint
		// for words on its own, so halfway to the foreground.
		"on_surface_variant": mixHex(fg, dimFg, .5),
		"outline":            or(get("muted"), dimFg),
	}
	accents := map[string]string{"primary": side["primary"], "secondary": side["secondary"]}
	p := &Palette{Source: "omarchy", Mode: "dark", Dark: side, Light: accents}
	if c["mode"] == "light" {
		p.Mode, p.Dark, p.Light = "light", accents, side
	}
	return p, nil
}

// mixHex goes the fraction t from a towards b.
func mixHex(a, b string, t float64) string {
	pa, pb := parseHex(a), parseHex(b)
	var out [3]int
	for i := range out {
		out[i] = int(float64(pa[i]) + (float64(pb[i])-float64(pa[i]))*t + .5)
	}
	return fmt.Sprintf("#%02x%02x%02x", out[0], out[1], out[2])
}

func parseHex(s string) [3]int {
	v, _ := strconv.ParseUint(s[1:], 16, 32)
	return [3]int{int(v >> 16 & 255), int(v >> 8 & 255), int(v & 255)}
}
