package pill

import "sync/atomic"

// The colour themes (ui.accent) as the pill draws them. Keep in step with the
// [data-accent] palettes in web/index.html: coral is the warm end of the
// gradient, violet the cool end, violetL its light tint, whatever the theme.
type palette struct {
	coral, violet, violetL rgb
	violetDark             rgb // violet on a dark surface (Graphite's is too dark there)
}

func pal4(coral, violet, violetL, violetDark uint32) *palette {
	return &palette{hex(coral), hex(violet), hex(violetL), hex(violetDark)}
}

var palettes = map[string]*palette{
	"":         pal4(0xFF6B5E, 0x7C3AED, 0xA78BFA, 0x7C3AED),
	"sunset":   pal4(0xF7A21B, 0xE11D48, 0xFDA4AF, 0xE11D48),
	"forest":   pal4(0x7CB342, 0x0F766E, 0x5EEAD4, 0x0F766E),
	"plum":     pal4(0xE8B04B, 0x6D2E5B, 0xE7A9CF, 0xA2477F),
	"ocean":    pal4(0x05D7C9, 0x6462FE, 0xA5A4FF, 0x6462FE),
	"graphite": pal4(0x94A3B8, 0x334155, 0xCBD5E1, 0x64748B),
}

var curPalette atomic.Pointer[palette]

// setAccent makes name the palette drawn from now on; an unknown name is the
// original pair. It reports whether that changed anything.
func setAccent(name string) bool {
	p := palettes[name]
	if p == nil {
		p = palettes[""]
	}
	return curPalette.Swap(p) != p
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
