package pill

import (
	"bytes"
	"embed"
	"image"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// The pill draws in the typeface of ui.font, like the page: "" is Sora with
// Baloo 2 for the wordmark and the figures; "system" the desktop's own, found
// per OS by systemFontFiles (sysfont_*.go); the others are static cuts of the
// page's fonts in font/ (made by scripts/pill-fonts.py, OFL). A character the
// chosen face lacks is drawn from the system's font, then from Sora, so a
// Russian or Greek dictation never turns into boxes.

//go:embed font/*-regular.ttf font/*-semibold.ttf font/*-bold.ttf
var pillFonts embed.FS

// faceSet is every face the pictures use; it is swapped whole, so a picture
// being drawn while the font changes keeps its own.
type faceSet struct {
	name               string
	label, main        font.Face
	body               font.Face // the island's running text
	word, figure, stat font.Face // the wordmark, a big figure, the day's figures
}

var (
	curFaces atomic.Pointer[faceSet]
	facesMu  sync.Mutex
	fontName atomic.Value // string: ui.font as the pill knows it
)

// setFont makes name (ui.font) the typeface drawn from now on. It reports
// whether that changed anything.
func setFont(name string) bool {
	if name != "" && name != "system" {
		if _, err := pillFonts.Open("font/" + name + "-regular.ttf"); err != nil {
			name = ""
		}
	}
	if old, _ := fontName.Swap(name).(string); old == name {
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
	want, _ := fontName.Load().(string)
	if f := curFaces.Load(); f != nil && f.name == want {
		return nil
	}
	facesMu.Lock()
	defer facesMu.Unlock()
	if f := curFaces.Load(); f != nil && f.name == want {
		return nil
	}
	f, err := buildFaces(want)
	if err != nil {
		return err
	}
	curFaces.Store(f)
	return nil
}

// trio is one typeface in the three weights the pill uses.
type trio struct{ regular, semibold, bold *opentype.Font }

var (
	soraOnce sync.Once
	soraTrio trio // Sora, with Baloo 2 as its bold
	soraErr  error
	sysOnce  sync.Once
	sysTrio  trio // nil members: not found or unreadable
)

func sora() (trio, error) {
	soraOnce.Do(func() {
		var t trio
		if t.regular, soraErr = opentype.Parse(soraRegular); soraErr != nil {
			return
		}
		if t.semibold, soraErr = opentype.Parse(soraSemiBold); soraErr != nil {
			return
		}
		if t.bold, soraErr = opentype.Parse(balooBold); soraErr != nil {
			return
		}
		soraTrio = t
	})
	return soraTrio, soraErr
}

func system() trio {
	sysOnce.Do(func() {
		r, s, b := systemFontFiles()
		sysTrio = trio{loadFont(r, nil), loadFont(s, nil), loadFont(b, nil)}
	})
	return sysTrio
}

func embedded(name string) trio {
	var t trio
	for _, w := range []struct {
		dst  **opentype.Font
		file string
	}{{&t.regular, "regular"}, {&t.semibold, "semibold"}, {&t.bold, "bold"}} {
		data, err := pillFonts.ReadFile("font/" + name + "-" + w.file + ".ttf")
		if err == nil {
			*w.dst, err = opentype.Parse(data)
		}
		if err != nil {
			slog.Debug("pill: font", "name", name, "weight", w.file, "err", err)
		}
	}
	return t
}

func buildFaces(name string) (*faceSet, error) {
	base, err := sora()
	if err != nil {
		return nil, err
	}
	// The chain per weight: the chosen face, the system's, Sora.
	// Sora lacks Cyrillic and Greek: for "" the system's font fills those in.
	var chain []trio
	switch name {
	case "":
		chain = []trio{base, system()}
	case "system":
		chain = []trio{system(), base}
	default:
		chain = []trio{embedded(name), system(), base}
	}
	opts := func(size float64) *opentype.FaceOptions {
		return &opentype.FaceOptions{Size: size * scale, DPI: 72, Hinting: font.HintingFull}
	}
	face := func(pick func(trio) *opentype.Font, size float64) (font.Face, error) {
		var c chainFace
		for _, t := range chain {
			if f := pick(t); f != nil {
				ff, err := opentype.NewFace(f, opts(size))
				if err != nil {
					return nil, err
				}
				c = append(c, ff)
			}
		}
		if len(c) == 1 {
			return c[0], nil
		}
		return c, nil
	}
	reg := func(t trio) *opentype.Font { return t.regular }
	semi := func(t trio) *opentype.Font { return t.semibold }
	bold := func(t trio) *opentype.Font { return t.bold }
	f := &faceSet{name: name}
	for _, x := range []struct {
		dst  *font.Face
		pick func(trio) *opentype.Font
		size float64
	}{
		{&f.label, reg, labelSize}, {&f.main, semi, mainSize}, {&f.body, reg, 12},
		{&f.word, bold, 19}, {&f.figure, bold, 24}, {&f.stat, bold, 17},
	} {
		if *x.dst, err = face(x.pick, x.size); err != nil {
			return nil, err
		}
	}
	return f, nil
}

// chainFace draws each character with the first face that has it; its
// metrics are the first face's.
type chainFace []font.Face

func (c chainFace) pick(r rune) font.Face {
	for _, f := range c {
		if _, ok := f.GlyphAdvance(r); ok {
			return f
		}
	}
	return c[len(c)-1] // its plain box, not a "no glyph" drawing
}

func (c chainFace) Close() error { return nil }

func (c chainFace) Glyph(dot fixed.Point26_6, r rune) (image.Rectangle, image.Image, image.Point, fixed.Int26_6, bool) {
	return c.pick(r).Glyph(dot, r)
}

func (c chainFace) GlyphBounds(r rune) (fixed.Rectangle26_6, fixed.Int26_6, bool) {
	return c.pick(r).GlyphBounds(r)
}

func (c chainFace) GlyphAdvance(r rune) (fixed.Int26_6, bool) { return c.pick(r).GlyphAdvance(r) }

func (c chainFace) Kern(r0, r1 rune) fixed.Int26_6 {
	if f := c.pick(r0); f == c.pick(r1) {
		return f.Kern(r0, r1)
	}
	return 0
}

func (c chainFace) Metrics() font.Metrics { return c[0].Metrics() }

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
