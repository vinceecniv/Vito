package pill

import (
	"os"
	"path/filepath"
)

// systemFontFiles is Segoe UI: regular, semibold, bold. (Segoe UI Variable,
// Windows 11's own, is a variable font x/image draws only at its default.)
func systemFontFiles() (regular, semibold, bold fontFile) {
	dir := filepath.Join(os.Getenv("WINDIR"), "Fonts")
	if os.Getenv("WINDIR") == "" {
		dir = `C:\Windows\Fonts`
	}
	return fontFile{path: filepath.Join(dir, "segoeui.ttf")},
		fontFile{path: filepath.Join(dir, "seguisb.ttf")},
		fontFile{path: filepath.Join(dir, "segoeuib.ttf")}
}
