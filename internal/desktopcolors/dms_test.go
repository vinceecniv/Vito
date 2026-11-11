package desktopcolors

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReadDMS(t *testing.T) {
	side := func(primary string) string {
		var b strings.Builder
		b.WriteString("{")
		for i, r := range Roles {
			if i > 0 {
				b.WriteString(",")
			}
			v := "#112233"
			if r == "primary" {
				v = primary
			}
			b.WriteString(`"` + r + `":"` + v + `"`)
		}
		b.WriteString(`,"scrim":"#000000"}`)
		return b.String()
	}
	p := filepath.Join(t.TempDir(), "dms-colors.json")
	os.WriteFile(p, []byte(`{"mode":"light","dank16":{},"colors":{"dark":`+side("#fab387")+`,"light":`+side("#8839ef")+`}}`), 0o600)
	pal, err := readDMS(p)
	if err != nil {
		t.Fatal(err)
	}
	if pal.Mode != "light" || pal.Dark["primary"] != "#fab387" || pal.Light["primary"] != "#8839ef" {
		t.Errorf("read %+v", pal)
	}
	if _, ok := pal.Dark["scrim"]; ok {
		t.Error("a role Vito does not use came along")
	}
	os.WriteFile(p, []byte(`{"colors":{"dark":{"primary":"#fab387"}}}`), 0o600)
	if _, err := readDMS(p); err == nil {
		t.Error("an incomplete file was accepted")
	}
}

// Two themes as Omarchy ships them (basecamp/omarchy, themes/*/colors.toml).
func TestReadOmarchy(t *testing.T) {
	p, err := readOmarchy("testdata/omarchy-tokyo-night.toml")
	if err != nil {
		t.Fatal(err)
	}
	if p.Mode != "dark" || p.Dark["primary"] != "#7aa2f7" || p.Dark["surface"] != "#1a1b26" {
		t.Errorf("tokyo night: %+v", p.Dark)
	}
	for _, r := range Roles {
		if !reHex.MatchString(p.Dark[r]) {
			t.Errorf("tokyo night: %s is %q", r, p.Dark[r])
		}
	}
	if len(p.Light) != 2 || p.Light["primary"] != "#7aa2f7" {
		t.Errorf("the other side carries more than the accents: %+v", p.Light)
	}
	p, err = readOmarchy("testdata/omarchy-catppuccin-latte.toml")
	if err != nil {
		t.Fatal(err)
	}
	if p.Mode != "light" || p.Light["surface"] != "#eff1f5" || p.Light["on_surface"] != "#4c4f69" {
		t.Errorf("latte: %+v", p.Light)
	}
	f := filepath.Join(t.TempDir(), "colors.toml")
	os.WriteFile(f, []byte("accent = \"#123456\" # a comment\nbackground = '#000000'\nforeground = \"#ffffff\"\n"), 0o600)
	if p, err := readOmarchy(f); err != nil || p.Dark["primary"] != "#123456" {
		t.Errorf("a commented value: %v %+v", err, p)
	}
	if mixHex("#000000", "#ffffff", .5) != "#808080" {
		t.Error(mixHex("#000000", "#ffffff", .5))
	}
}

// dms 2.2 moved its cache to ~/.cache/dms; older versions used DankMaterialShell.
func TestDMSPaths(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the shell runs on Linux; os.UserCacheDir is elsewhere on other systems")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("FLATPAK_ID", "")
	got := dmsPaths()
	want := []string{
		filepath.Join(home, ".cache", "dms", "dms-colors.json"),
		filepath.Join(home, ".cache", "DankMaterialShell", "dms-colors.json"),
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("dmsPaths() = %v, want %v", got, want)
	}
}
