//go:build linux

package hotkey

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const niriSample = `// a niri config
input {
    keyboard {
        xkb {
            // options "grp:win_space_toggle,ctrl:nocaps"
            layout "us"
            options "compose:ralt,ctrl:nocaps"
        }
        numlock
    }
}

binds {
    Mod+T { spawn "alacritty"; }
}
`

func TestNiriEditRoundTrip(t *testing.T) {
	on, err := niriEdit(niriSample, true, niriBind)
	if err != nil {
		t.Fatal(err)
	}
	if opt, bind := niriHas(on); !opt || !bind {
		t.Fatalf("not set up:\n%s", on)
	}
	if !strings.Contains(on, `options "compose:ralt,caps:menu"`) {
		t.Errorf("options not merged:\n%s", on)
	}
	if !strings.Contains(on, `// options "grp:win_space_toggle,ctrl:nocaps"`) {
		t.Error("a commented example was changed")
	}
	off, err := niriEdit(on, false, niriBind)
	if err != nil {
		t.Fatal(err)
	}
	if opt, bind := niriHas(off); opt || bind {
		t.Fatalf("still there:\n%s", off)
	}
	if strings.Contains(off, "Vito") {
		t.Errorf("Vito's block left behind:\n%s", off)
	}
	niriValid(t, on)
	niriValid(t, off)
}

func TestNiriEditInsertsBlocks(t *testing.T) {
	for name, src := range map[string]string{
		"no options": "input {\n    keyboard {\n        xkb {\n            layout \"de\"\n        }\n    }\n}\n",
		"no xkb":     "input {\n    keyboard {\n        numlock\n    }\n}\n",
		"no input":   "binds {\n    Mod+T { spawn \"foot\"; }\n}\n",
	} {
		out, err := niriEdit(src, true, niriBind)
		if err != nil {
			t.Fatal(name, err)
		}
		if opt, bind := niriHas(out); !opt || !bind {
			t.Errorf("%s: not set up:\n%s", name, out)
		}
		niriValid(t, out)
	}
}

func TestNiriEditMenuTaken(t *testing.T) {
	src := "binds {\n    Menu { spawn \"rofi\"; }\n}\n"
	if _, err := niriEdit(src, true, niriBind); err == nil {
		t.Error("a Menu bind of the user's own was overwritten")
	}
}

// niriValid asks niri itself, where it is installed.
func niriValid(t *testing.T, src string) {
	t.Helper()
	if _, err := exec.LookPath("niri"); err != nil {
		return
	}
	p := filepath.Join(t.TempDir(), "config.kdl")
	os.WriteFile(p, []byte(src), 0o600)
	if out, err := exec.Command("niri", "validate", "-c", p).CombinedOutput(); err != nil {
		if strings.Contains(src, "release {") && strings.Contains(string(out), "only one action") {
			t.Logf("this niri has no press/release binds yet (SetNiriCaps falls back to niriToggle)")
			return
		}
		t.Errorf("niri rejects it: %s\n%s", lastLine(out), src)
	}
}

// The toggle bind is what a niri without press/release binds gets.
func TestNiriEditToggle(t *testing.T) {
	on, err := niriEdit(niriSample, true, niriToggle)
	if err != nil {
		t.Fatal(err)
	}
	if opt, bind := niriHas(on); !opt || !bind {
		t.Fatalf("not set up:\n%s", on)
	}
	off, err := niriEdit(on, false, niriBind)
	if err != nil {
		t.Fatal(err)
	}
	if opt, bind := niriHas(off); opt || bind || strings.Contains(off, "Vito") {
		t.Fatalf("still there:\n%s", off)
	}
	niriValid(t, on)
	// A user's own start/stop bind on another key is not Vito's Caps Lock.
	if _, bind := niriHas("binds {\n    Mod+D { spawn \"pkill\" \"-USR2\" \"-f\" \"vito serve\"; }\n}\n"); bind {
		t.Error("a Mod+D bind counted as Caps Lock")
	}
}
