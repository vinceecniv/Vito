package pill

import (
	"bytes"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
)

// The pill draws in Sora and Baloo 2, like the page, unless ui.font is
// "system": then in the desktop's own typeface, found per OS by
// systemFontFiles (sysfont_*.go). A weight that cannot be found or read
// (a CFF2 variable font, say, which x/image cannot parse) stays Sora.

// faceSet is every face the pictures use; it is swapped whole, so a picture
// being drawn while the font changes keeps its own.
type faceSet struct {
	system             bool
	label, main        font.Face
	body               font.Face // the island's running text
	word, figure, stat font.Face // the wordmark, a big figure, the day's figures
}

var (
	curFaces   atomic.Pointer[faceSet]
	facesMu    sync.Mutex
	fontSystem atomic.Bool
)

// setFont makes name (ui.font) the typeface drawn from now on. It reports
// whether that changed anything.
func setFont(name string) bool {
	sys := name == "system"
	if fontSystem.Swap(sys) == sys {
		return false
	}
	faces()
	return true
}

// fcs is the faces in use.
func fcs() *faceSet {
	if f := curFaces.Load(); f != nil {
		return f
	}
	faces()
	return curFaces.Load()
}

// faces makes the face set for the typeface wanted, if it is not the one in use.
func faces() error {
	want := fontSystem.Load()
	if f := curFaces.Load(); f != nil && f.system == want {
		return nil
	}
	facesMu.Lock()
	defer facesMu.Unlock()
	if f := curFaces.Load(); f != nil && f.system == want {
		return nil
	}
	f, err := buildFaces(want)
	if err != nil {
		return err
	}
	curFaces.Store(f)
	return nil
}

func buildFaces(system bool) (*faceSet, error) {
	reg, err := opentype.Parse(soraRegular)
	if err != nil {
		return nil, err
	}
	semi, err := opentype.Parse(soraSemiBold)
	if err != nil {
		return nil, err
	}
	bold, err := opentype.Parse(balooBold)
	if err != nil {
		return nil, err
	}
	if system {
		r, s, b := systemFontFiles()
		reg = loadFont(r, reg)
		semi = loadFont(s, semi)
		bold = loadFont(b, bold)
	}
	opts := func(size float64) *opentype.FaceOptions {
		return &opentype.FaceOptions{Size: size * scale, DPI: 72, Hinting: font.HintingFull}
	}
	f := &faceSet{system: system}
	for _, x := range []struct {
		dst  *font.Face
		src  *opentype.Font
		size float64
	}{
		{&f.label, reg, labelSize}, {&f.main, semi, mainSize}, {&f.body, reg, 12},
		{&f.word, bold, 19}, {&f.figure, bold, 24}, {&f.stat, bold, 17},
	} {
		if *x.dst, err = opentype.NewFace(x.src, opts(x.size)); err != nil {
			return nil, err
		}
	}
	return f, nil
}

// fontFile is a font on disk: its path and, in a collection, which face.
type fontFile struct {
	path  string
	index int
}

// loadFont reads ff, or keeps fallback when it cannot.
func loadFont(ff fontFile, fallback *opentype.Font) *opentype.Font {
	if ff.path == "" {
		return fallback
	}
	data, err := os.ReadFile(ff.path)
	if err != nil {
		slog.Debug("pill: system font", "path", ff.path, "err", err)
		return fallback
	}
	var f *sfnt.Font
	if bytes.HasPrefix(data, []byte("ttcf")) {
		var c *sfnt.Collection
		if c, err = sfnt.ParseCollection(data); err == nil {
			f, err = c.Font(ff.index)
		}
	} else {
		f, err = sfnt.Parse(data)
	}
	if err != nil {
		slog.Debug("pill: system font", "path", ff.path, "err", err)
		return fallback
	}
	return f
}
