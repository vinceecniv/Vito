//go:build !linux && !windows && !darwin

package pill

// systemFontFiles knows no system font here: the pill stays in Sora.
func systemFontFiles() (regular, semibold, bold fontFile) { return }
