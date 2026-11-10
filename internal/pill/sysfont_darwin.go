package pill

// systemFontFiles is San Francisco. SFNS is a variable font, which x/image
// draws only at its default (regular), so the bolder weights come from
// Helvetica Neue's collection (face 1, its Bold; written without a Mac, so untested).
func systemFontFiles() (regular, semibold, bold fontFile) {
	sf := fontFile{path: "/System/Library/Fonts/SFNS.ttf"}
	hn := fontFile{path: "/System/Library/Fonts/HelveticaNeue.ttc", index: 1}
	return sf, hn, hn
}
