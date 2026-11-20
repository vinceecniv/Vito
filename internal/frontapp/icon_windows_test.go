//go:build windows

package frontapp

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIconData(t *testing.T) {
	exe := filepath.Join(os.Getenv("SystemRoot"), "explorer.exe")
	data, ext := iconData(exe)
	if ext != ".png" || len(data) == 0 {
		t.Fatalf("no icon from %s", exe)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	b := img.Bounds()
	_, _, _, a0 := img.At(0, 0).RGBA()
	_, _, _, ac := img.At(b.Dx()/2, b.Dy()/2).RGBA()
	t.Logf("%dx%d, corner alpha %d, centre alpha %d", b.Dx(), b.Dy(), a0>>8, ac>>8)
	if b.Dx() < 32 || ac == 0 {
		t.Errorf("icon %dx%d, centre transparent", b.Dx(), b.Dy())
	}
	if os.Getenv("VITO_ICON_OUT") != "" {
		_ = os.WriteFile(os.Getenv("VITO_ICON_OUT"), data, 0o644)
	}
}

// VITO_ICON_SEED=1 keeps the icons of a few installed programs in the real
// cache, for trying the page without dictating into each.
func TestIconSeed(t *testing.T) {
	if os.Getenv("VITO_ICON_SEED") == "" {
		t.Skip("set VITO_ICON_SEED=1")
	}
	for id, exe := range map[string]string{
		"msedge":  `C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
		"code":    `C:\Program Files\Microsoft VS Code\Code.exe`,
		"outlook": `C:\Program Files\Microsoft Office\root\Office16\OUTLOOK.EXE`,
	} {
		keepIcon(id, exe)
	}
	time.Sleep(time.Second)
	for _, id := range []string{"msedge", "code", "outlook"} {
		p, _, ok := Icon(id)
		t.Log(id, p, ok)
	}
}
