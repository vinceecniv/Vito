// Package desktopcolors reads the colour scheme a desktop shell publishes, so
// Vito can wear it (ui.accent "dms" or "omarchy").
//
// Dux Modular Shell (formerly DankMaterialShell, the name its files and
// folders still carry) writes its Material palette — generated from the
// wallpaper by matugen, or one of its registry themes — to
// ~/.cache/DankMaterialShell/dms-colors.json, a light and a dark set of
// Material roles each, and rewrites it whenever the scheme changes.
// Omarchy keeps the active theme's colors.toml in its current/theme folder
// (omarchy.go): one side, light or dark, in names of its own, which are
// mapped onto the same roles.
package desktopcolors

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Roles are the Material roles Vito maps onto its own tokens; the rest of
// the file is left out of what the page and the pill get.
var Roles = []string{
	"primary", "secondary", "tertiary", "on_primary",
	"surface", "surface_container_lowest", "surface_container_low", "surface_container",
	"surface_container_high", "surface_container_highest",
	"on_surface", "on_surface_variant", "outline",
}

// Palette is one shell's scheme: the roles on each side, and the side the
// shell itself is on. A shell with only one side (Omarchy) gives the other
// side its accents alone — primary and secondary — and no surfaces.
type Palette struct {
	Source string            `json:"source"` // dms | omarchy
	Mode   string            `json:"mode"`   // dark | light, as the shell is now
	Dark   map[string]string `json:"dark"`
	Light  map[string]string `json:"light"`
}

// Side is the roles on the side the shell is on.
func (p *Palette) Side() map[string]string {
	if p.Mode == "light" {
		return p.Light
	}
	return p.Dark
}

// ErrNone: no shell publishes a scheme here.
var ErrNone = errors.New("no desktop colour scheme")

// All returns every scheme available here, by its ui.accent name.
func All() map[string]*Palette {
	out := map[string]*Palette{}
	if p, err := DMS(); err == nil {
		out["dms"] = p
	}
	if p, err := Omarchy(); err == nil {
		out["omarchy"] = p
	}
	return out
}

// For returns the scheme for a ui.accent name, or nil.
func For(name string) *Palette {
	var p *Palette
	switch name {
	case "dms":
		p, _ = DMS()
	case "omarchy":
		p, _ = Omarchy()
	}
	return p
}

// source reads one shell's file again only when it changed, and looks no
// more than once a second, so the pill can ask before every picture.
type source struct {
	running func() bool
	paths   func() []string
	read    func(path string) (*Palette, error)

	mu      sync.Mutex
	cached  *Palette
	key     string // the file as resolved, and its modification time
	checked time.Time
}

func (s *source) get() (*Palette, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Since(s.checked) < time.Second {
		if s.cached == nil {
			return nil, ErrNone
		}
		return s.cached, nil
	}
	s.checked = time.Now()
	if !s.running() {
		s.cached = nil
		return nil, ErrNone
	}
	for _, p := range s.paths() {
		real, err := filepath.EvalSymlinks(p) // a theme switch moves a link
		if err != nil {
			continue
		}
		st, err := os.Stat(real)
		if err != nil {
			continue
		}
		key := real + "@" + st.ModTime().String()
		if s.cached != nil && key == s.key {
			return s.cached, nil
		}
		pal, err := s.read(real)
		if err != nil {
			continue
		}
		s.cached, s.key = pal, key
		return pal, nil
	}
	s.cached = nil
	return nil, ErrNone
}

var reHex = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// inFlatpak: the sandbox sees neither the host's processes nor its runtime
// folder, so there the shell's file alone has to tell.
func inFlatpak() bool { return os.Getenv("FLATPAK_ID") != "" }

// homeFile is a path under the host's home. Inside a Flatpak the XDG
// variables point at the app's own folders, so the shells' files are found
// by their usual place instead (shared read-only by the manifest).
func homeFile(parts ...string) string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(append([]string{h}, parts...)...)
}

var dms = &source{running: dmsRunning, paths: dmsPaths, read: readDMS}

// DMS returns Dux Modular Shell's current palette.
func DMS() (*Palette, error) { return dms.get() }

func dmsPaths() []string {
	var out []string
	if d, err := os.UserCacheDir(); err == nil && !inFlatpak() {
		out = append(out, filepath.Join(d, "DankMaterialShell", "dms-colors.json"))
	}
	if p := homeFile(".cache", "DankMaterialShell", "dms-colors.json"); p != "" && (len(out) == 0 || out[0] != p) {
		out = append(out, p)
	}
	return out
}

// dmsRunning reports whether Dux Modular Shell runs in this session: its
// colour file outlives the shell, so the file alone says too little. dms
// leaves dms-<pid>.pid in $XDG_RUNTIME_DIR.
func dmsRunning() bool {
	if inFlatpak() {
		return true
	}
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		return false
	}
	pids, _ := filepath.Glob(filepath.Join(dir, "dms-*.pid"))
	for _, f := range pids {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		pid := strings.TrimSpace(string(b))
		if pid == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join("/proc", pid)); err == nil {
			return true
		}
	}
	return false
}

func readDMS(path string) (*Palette, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f struct {
		Mode   string                       `json:"mode"`
		Colors map[string]map[string]string `json:"colors"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	pick := func(side string) (map[string]string, error) {
		src := f.Colors[side]
		out := make(map[string]string, len(Roles))
		for _, r := range Roles {
			v := src[r]
			if !reHex.MatchString(v) {
				return nil, errors.New("dms-colors.json: " + side + "." + r + " missing")
			}
			out[r] = v
		}
		return out, nil
	}
	dark, err := pick("dark")
	if err != nil {
		return nil, err
	}
	light, err := pick("light")
	if err != nil {
		return nil, err
	}
	mode := f.Mode
	if mode != "light" {
		mode = "dark"
	}
	return &Palette{Source: "dms", Mode: mode, Dark: dark, Light: light}, nil
}
