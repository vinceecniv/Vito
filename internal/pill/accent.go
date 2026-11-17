package pill

import (
	"math"
	"strconv"
	"sync"
	"sync/atomic"

	"vito/internal/desktopcolors"
)

// The colour themes (ui.accent) as the pill draws them. Keep in step with the
// [data-accent] palettes in web/index.html: coral is the warm end of the
// gradient, violet the cool end, violetL its light tint, whatever the theme.
type palette struct {
	coral, violet, violetL rgb
	violetDark             rgb  // violet on a dark surface, where it differs
	mono                   bool // the accent is white on dark, black on light
}

func pal4(coral, violet, violetL, violetDark uint32) *palette {
	return &palette{coral: hex(coral), violet: hex(violet), violetL: hex(violetL), violetDark: hex(violetDark)}
}

var palettes = map[string]*palette{
	"":      pal4(0xFF6B5E, 0x7C3AED, 0xA78BFA, 0x7C3AED),
	"ocean": pal4(0x05D7C9, 0x6462FE, 0xA5A4FF, 0x6462FE),
	// Community schemes, by their own names.
	"catppuccin":  pal4(0xEA76CB, 0x8839EF, 0xCBA6F7, 0x8839EF),
	"tokyo-night": pal4(0x7AA2F7, 0x9854F1, 0xBB9AF7, 0x9854F1),
	"nord":        pal4(0x8FBCBB, 0x5E81AC, 0x81A1C1, 0x5E81AC),
	"gruvbox":     pal4(0xD65D0E, 0x458588, 0x83A598, 0x458588),
	"rose-pine":   pal4(0xD7827E, 0x907AA9, 0xC4A7E7, 0x907AA9),
	"kanagawa":    pal4(0xFFA066, 0x4D699B, 0x7E9CD8, 0x4D699B),
	"everforest":  pal4(0xA7C080, 0x35A77C, 0x83C092, 0x35A77C),
	"ristretto":   pal4(0xF38D70, 0xD65A73, 0xFD6883, 0xD65A73),
	"solarized":   pal4(0x2AA198, 0x6C71C4, 0x9EA2E0, 0x6C71C4),
	"flexoki":     pal4(0xDA702C, 0x205EA6, 0x4385BE, 0x205EA6),
	"dracula":     pal4(0xFF79C6, 0x8B6CEF, 0xBD93F9, 0x8B6CEF),
	// High contrast: the dark side's bright accents, which the pill's
	// fills (never text) carry on either surface.
	"hc": pal4(0xFFE600, 0x0035D4, 0x00E5FF, 0x00B8E6),
	// Black and white: no colour; themeFor draws the accent as the ink of
	// each side (mono). coral and violet are what the brand tile shows.
	"hc-bw": {coral: hex(0x404040), violet: hex(0x000000), violetL: hex(0xFFFFFF), violetDark: hex(0xFFFFFF), mono: true},
}

// The character in the black-and-white theme: a grey of its own per side,
// light on dark and mid-grey on light, so its dark eyes and mouth still read.
// Keep in step with .vm-svg under [data-accent="hc-bw"] in web/index.html.
var (
	monoFigureDark  = pal4(0xF2F2F2, 0x9A9A9A, 0xD9D9D9, 0x9A9A9A)
	monoFigureLight = pal4(0xD9D9D9, 0x5C5C5C, 0x8C8C8C, 0x5C5C5C)
)

// figurePal is the palette the character is drawn in on that side.
func figurePal(dark bool) *palette {
	if p := pal(); !p.mono {
		return p
	}
	if dark {
		return monoFigureDark
	}
	return monoFigureLight
}

var curPalette atomic.Pointer[palette]

// setAccent makes name the palette drawn from now on; an unknown name is the
// original pair. It reports whether that changed anything.
func setAccent(name string) bool {
	p := palettes[name]
	if FollowsDesktop(name) {
		p = desktopPalette(name)
	}
	if p == nil {
		p = palettes[""]
	}
	p = graded(p)
	return curPalette.Swap(p) != p
}

// Gradient (ui.gradient) is how far the cool end of the theme's gradient
// moves towards the warm end: "" none of the way (the theme as it is),
// "medium", "subtle", or "none" — one flat colour. The page mixes the same
// fractions (GRADIENTS in web/index.html).
var gradMix atomic.Uint64 // math.Float64bits of the fraction

func gradientFraction(name string) float64 {
	return map[string]float64{"medium": .4, "subtle": .75, "none": 1}[name]
}

// SetGradient makes name (ui.gradient) the gradient drawn from now on, by
// the pill, the tray and the window's icon. It reports whether that changed
// anything.
func SetGradient(name string) bool {
	v := math.Float64bits(gradientFraction(name))
	return gradMix.Swap(v) != v
}

var (
	gradMu   sync.Mutex
	gradMemo = map[gradKey]*palette{}
)

type gradKey struct {
	p *palette
	t float64
}

// graded is p with its cool end moved towards the warm end by the current
// gradient. The same *palette for the same input, so setAccent's comparison
// still tells a change. A black-and-white palette keeps its two ends.
func graded(p *palette) *palette {
	t := math.Float64frombits(gradMix.Load())
	if t == 0 || p == nil || p.mono {
		return p
	}
	gradMu.Lock()
	defer gradMu.Unlock()
	k := gradKey{p, t}
	if g := gradMemo[k]; g != nil {
		return g
	}
	if len(gradMemo) > 64 { // desktop palettes come and go
		clear(gradMemo)
	}
	g := &palette{coral: p.coral, violet: blend(p.violet, p.coral, t),
		violetL:    blend(p.violetL, blend(p.coral, rgb{1, 1, 1}, .3), t),
		violetDark: blend(p.violetDark, p.coral, t)}
	gradMemo[k] = g
	return g
}

func pal() *palette {
	if p := curPalette.Load(); p != nil {
		return p
	}
	return palettes[""]
}

// violetOn is the cool end on the given side of the theme.
func (p *palette) violetOn(dark bool) rgb {
	if dark {
		return p.violetDark
	}
	return p.violet
}

// The desktop themes ("dms", "omarchy", "windows", "macos"): the shell's palette, as the page
// maps it — primary, and a deep primary for the cool end. Built again only
// when the shell's scheme changed, so setAccent sees the same palette from
// picture to picture.
var (
	deskMu  sync.Mutex
	deskSrc *desktopcolors.Palette
	deskPal *palette
)

func desktopPalette(name string) *palette {
	src := desktopcolors.For(name)
	if src == nil {
		return nil
	}
	deskMu.Lock()
	defer deskMu.Unlock()
	if src != deskSrc {
		// As the page: primary, and a deep primary for the cool end.
		lp, dp := hexStr(src.Light["primary"]), hexStr(src.Dark["primary"])
		deskPal = &palette{coral: hexStr(src.Side()["primary"]), violet: blend(lp, rgb{}, .38),
			violetL: blend(dp, rgb{1, 1, 1}, .4), violetDark: dp}
		deskSrc = src
	}
	return deskPal
}

// hexStr reads "#rrggbb"; desktopcolors checked the form.
func hexStr(s string) rgb {
	v, _ := strconv.ParseUint(s[1:], 16, 32)
	return hex(uint32(v))
}

// FollowsDesktop reports whether accent is a desktop theme ("dms",
// "omarchy", "windows", "macos"), whose colours change with the shell's scheme and side rather
// than with Vito's settings: what draws them once (the tray, the launcher's
// icon) has to look again now and then.
func FollowsDesktop(accent string) bool {
	return accent == "dms" || accent == "omarchy" || accent == "windows" || accent == "macos"
}

// BrandColors is the colour theme's gradient as the app icon draws it, for the
// tray and the window's icon: the warm end, the cool end, and the cool end's
// light tint. An unknown name is the original pair.
func BrandColors(accent string) (coral, violet, violetL [3]uint8) {
	p := palettes[accent]
	if FollowsDesktop(accent) {
		p = desktopPalette(accent)
	}
	if p == nil {
		p = palettes[""]
	}
	// A desktop theme's icon is the page's gradient on the shell's side:
	// primary into a deep primary. p.violet is the light side's, which on a
	// shell whose two sides differ (DMS: orange dark, violet light) turned
	// the icon into another theme's.
	if p != palettes[""] && FollowsDesktop(accent) {
		if src := desktopcolors.For(accent); src != nil {
			if s := src.Side()["primary"]; s != "" {
				pr := hexStr(s)
				deep := blend(pr, rgb{}, .38)
				p = &palette{coral: pr, violet: deep, violetL: blend(pr, rgb{1, 1, 1}, .4)}
			}
		}
	}
	p = graded(p)
	b := func(c rgb) [3]uint8 {
		return [3]uint8{uint8(clamp(c.r)*255 + 0.5), uint8(clamp(c.g)*255 + 0.5), uint8(clamp(c.b)*255 + 0.5)}
	}
	return b(p.coral), b(p.violet), b(p.violetL)
}
