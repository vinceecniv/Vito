package pill

import (
	"fmt"
	"image"
	"math"
	"strconv"
	"strings"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"

	"vito/internal/cleanup"
	"vito/internal/config"
	"vito/internal/daemon"
	"vito/internal/inject"
	"vito/internal/stt"
	"vito/internal/uitext"
	"vito/internal/webapp"
)

// The island is what the resting pill opens into when the pointer stays on it:
// a small panel over the spot, with the day in figures, the last dictation to
// paste again and a way into Vito, and behind the menu button the settings you
// change while working. The microphone at its foot sits exactly where the
// resting pill was, so it reads as the pill grown up, and a click there
// dictates as a click on the pill does.
//
// It has its own window, on the screen whose pill was pointed at, and only for
// as long as the pointer is on it. Three views share the panel: main, the
// settings, and the list of cleanup rule sets; the panel's height follows the
// view.
//
// Pinned (the pin in its header) it stays: a window of its own that is dragged
// by its free space and remembers where (ui.island_pin). The pills on the
// screens give way to it, and a dictation shows in it, where the microphone
// is: the pill draws into an embedWindow and the island lays its pictures in.

// Today is the day in figures, as the stats page counts it.
type Today struct {
	Words, Sentences, Activations int
	Saved, Spoken                 int // seconds: typing time saved, time dictated
}

// Sources is what the island shows that the daemon does not know.
type Sources struct {
	// Today is what was dictated today.
	Today func() Today
	// Last is the most recent dictation's text, "" when there is none.
	Last func() string
}

// The island's window and panel, in logical pixels. The window reaches lower
// than the pill's, so the panel has room below the microphone, and is tall
// enough for the longest view; the pill's middle is where it is on screen all
// the same.
const (
	islandWinW, islandWinH = 300, 420
	islandMargin           = 8
	islandMarkY            = islandWinH - (surfaceH - pillY) - (marginBottom - islandMargin)
	panelW                 = 272.0
	// The panel ends above the microphone, which stands free under it, on
	// the transparent window: panelFoot is the panel's lower edge, panelBottom
	// the island's, under the microphone, and footH the room between.
	panelBottom = islandMarkY + 30
	panelFoot   = islandMarkY - micR - 10
	footH       = panelBottom - panelFoot
	panelPad    = 18.0
	micR        = 20.0
	ruleRow     = 28.0
	maxRules    = 10

	islandDwell = 400 * time.Millisecond // the pointer on the pill this long opens it
	islandGrace = 350 * time.Millisecond // off the island this long closes it
	islandOpen  = 220 * time.Millisecond
	islandView  = 200 * time.Millisecond
	tipDelay    = 550 * time.Millisecond // the pointer on a button this long explains it
	// optionsGrace: the pointer away from a pinned island this long closes its
	// options. No desktop tells a window about clicks elsewhere (layer-shell
	// surfaces get none), but every one reports the pointer leaving.
	optionsGrace = 2500 * time.Millisecond
	copiedFor    = 1500 * time.Millisecond // "Copied" stands in for the label this long
	panelGap     = 8                       // window above a pinned panel, logical pixels

	// A pinned island left alone this long fades to dimTo, and comes back at
	// the pointer or a dictation.
	dimAfter = time.Minute
	dimTo    = 0.45
	dimTime  = 400 * time.Millisecond
	// Faded this much longer, it folds back into the resting pill where it
	// stands, which is all that takes the pointer then; the pointer on it or
	// a dictation unfolds it.
	compactAfter = 4 * time.Minute
	compactTime  = 450 * time.Millisecond

	// embedLiveUp is how far over the microphone the live words stand in the
	// pinned island: where the last dictation is, which makes way for them.
	embedLiveUp = 90

	// statsH is the room today's figures take in the main view.
	statsH = 34
)

type view int

const (
	viewMain view = iota
	viewSettings
	viewRules
)

// Things on the island a click does something with. Rule sets follow tRule0.
type target int

const (
	tNone target = iota
	tMic
	tOpenVito
	tMenu
	tRepaste
	tCleanup
	tRules
	tEnter
	tLive
	tStats
	tStyleBars
	tStyleGlow
	tStyleVito
	tPin
	tCopy
	tRestore // the cancelled dictation, kept a while
	tWords   // the stats, which only explain themselves
	tSaved
	tRule0 target = 100
)

type rect struct{ x, y, w, h float64 }

func (r rect) has(x, y float64) bool { return x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h }

type ruleSet struct{ id, name string }

// islandState is one picture of the island.
type islandState struct {
	open       float64 // 0 the hovered pill .. 1 the island
	view, prev view
	fade       float64 // 0 showing prev .. 1 showing view
	ph         float64 // the panel's height now, between the views' heights
	over       target
	dark       bool
	pinned     bool
	tip        float64 // how far the tooltip of over is in
	embed      float64 // how far the pill has taken the microphone's place
	copied     bool    // the last dictation was just copied; its label says so
	rest       Frame   // the character's moment, where it is the microphone
	compact    float64 // 0 the island .. 1 only the resting pill, long left alone

	day         Today
	last        string
	kept        bool   // a cancelled dictation can be restored; it takes last's place
	heard       string // what was heard of it
	cleanup     bool
	enter, live bool
	stats       bool // today's figures shown (ui.island_stats)
	partials    bool // the speech service sends text while you speak
	style       Style
	rules       []ruleSet
	active      string // the active rule set's id
}

// height is the panel's height for a view.
func (st *islandState) height(v view) float64 {
	switch v {
	case viewSettings:
		return 292 - footH - 5
	case viewRules:
		return 45 + ruleRow*float64(min(len(st.rules), maxRules)) + 64 - footH
	}
	if !st.stats {
		return 209 - footH - statsH
	}
	return 209 - footH
}

func (st *islandState) top() float64 { return panelFoot - st.ph }

func (st *islandState) activeName() string {
	for _, r := range st.rules {
		if r.id == st.active {
			return r.name
		}
	}
	if len(st.rules) > 0 {
		return st.rules[0].name
	}
	return ""
}

type islandPainter struct {
	t       func(string) string
	lang    string
	targets map[target]rect
	texts   map[string]*image.Alpha
	frame   []byte // the embedded pill's latest picture
}

func newIslandPainter(lang string) *islandPainter {
	return &islandPainter{t: uitext.Translator(lang), lang: lang, texts: map[string]*image.Alpha{}}
}

// text returns s drawn in face, from a cache: the island draws the same few
// lines over and over.
func (ip *islandPainter) text(face font.Face, s string) (*image.Alpha, float64) {
	k := fmt.Sprintf("%p\x00%s", face, s)
	m, ok := ip.texts[k]
	if !ok {
		m, _ = textMask(face, s)
		ip.texts[k] = m
	}
	return m, float64(font.MeasureString(face, s).Ceil()) / scale
}

// line draws s with its baseline at y, from x; it returns the width.
func (ip *islandPainter) line(buf []byte, face font.Face, s string, x, baseline float64, c rgb, a float64) float64 {
	w, h := islandWinW*scale, islandWinH*scale
	m, width := ip.text(face, s)
	stamp(buf, w, h, m, int(x*scale)-1, int(baseline*scale)-face.Metrics().Ascent.Ceil()-1, c, a)
	return width
}

func (ip *islandPainter) draw(buf []byte, st islandState) {
	k := smooth(st.compact)
	if k < 1 {
		ip.drawFull(buf, st)
	}
	if k <= 0 {
		return
	}
	// Folded: the island goes back to the resting pill, where its foot is;
	// the pointer on that pill opens it again.
	if k >= 1 {
		clear(buf)
	} else {
		for i := range buf {
			buf[i] = byte(float64(buf[i]) * (1 - k))
		}
	}
	ip.targets = map[target]rect{}
	var pic []byte
	if st.style == StyleMascot {
		pic = make([]byte, surfaceW*scale*surfaceH*scale*4)
		DrawIdleMascot(pic, surfaceW*scale, surfaceH*scale, st.dark, 1, 0, st.rest)
	} else {
		pic = idlePicture(st.dark, 1, 0, st.style, nil, false)
	}
	over(buf, islandWinW*scale, islandWinH*scale, pic, surfaceW*scale, surfaceH*scale, foldDX*scale, foldDY*scale, k)
}

// The resting pill's picture stands this far from the island window's top
// left when the island is folded: the pill at the mark it grew out of.
const (
	foldDX = (islandWinW - surfaceW) / 2
	foldDY = islandMarkY - pillY
)

// over lays the premultiplied picture src (sw × sh) on dst at dx, dy, faded
// by a, clipping what falls outside.
func over(dst []byte, dw, dh int, src []byte, sw, sh, dx, dy int, a float64) {
	for y := max(0, -dy); y < sh && y+dy < dh; y++ {
		for x := max(0, -dx); x < sw && x+dx < dw; x++ {
			si := (y*sw + x) * 4
			sa := float64(src[si+3]) * a / 255
			if sa <= 0 {
				continue
			}
			di := ((y+dy)*dw + x + dx) * 4
			keep := 1 - sa
			for c := range 4 {
				dst[di+c] = uint8(min(255, float64(dst[di+c])*keep+float64(src[si+c])*a+0.5))
			}
		}
	}
}

func (ip *islandPainter) drawFull(buf []byte, st islandState) {
	clear(buf)
	w, h := islandWinW*scale, islandWinH*scale
	s := float64(scale)
	th := themeFor(st.dark)
	ip.targets = map[target]rect{}

	// The panel grows out of the hovered pill (52 × 20 on the pill's spot),
	// keeping its foot there.
	e := easeOut(st.open)
	cx := float64(w) / 2
	pw, ph := mix(52, panelW, e), mix(20, st.ph, e)
	bottom := mix(islandMarkY+10, panelFoot, e)
	pcy := bottom - ph/2
	r := mix(10, 22, e)
	shadowR(buf, w, h, cx, (pcy+2)*s, pw*s/2, ph*s/2, r*s, 14*s, th.shadow*1.3*clamp(st.open*2))
	box(buf, w, h, cx, pcy*s, pw*s/2, ph*s/2, r*s, th.body, 0.97)
	ringR(buf, w, h, cx, pcy*s, pw*s/2, ph*s/2, r*s, th.edge, 0.22, 0.5*s)

	ip.mic(buf, w, h, th, st)

	in := smooth(clamp((st.open - 0.55) / 0.45))
	if in <= 0 {
		return
	}
	left := (islandWinW - panelW) / 2
	top := st.top()
	ink, muted := inkFor(st.dark), mutedFor(st.dark)
	tint := blend(th.body, th.to, 0.07)
	if st.dark {
		tint = blend(th.body, rgb{1, 1, 1}, 0.06)
	}

	// Header: the mark and the name as in the app, which open Vito, and on
	// the right the menu button, which turns into a cross in the settings and
	// an arrow back in the list of rule sets.
	// The pin on the left, the menu on the right and the wordmark between
	// them, alone: a calm title bar.
	hy := top + 25.0
	_, ww := ip.text(wordFace, "vito")
	brand := rect{left + panelW/2 - ww/2 - 12, hy - 16, ww + 24, 32}
	ip.targets[tOpenVito] = brand
	if st.over == tOpenVito {
		fill(buf, w, h, brand, 16, blend(tint, th.to, 0.08), in)
	}
	ip.line(buf, wordFace, "vito", left+panelW/2-ww/2, hy+6.5, ink, in)
	menu := rect{left + panelW - panelPad - 28 + 4, hy - 14, 28, 28}
	pin := rect{left + panelPad - 4, hy - 14, 28, 28}
	ip.iconButton(buf, th, st, tPin, pin, tint, in, func(cx, cy float64, c rgb, a float64) {
		if st.pinned {
			c = th.to
		}
		pushpin(buf, w, h, cx, cy, s, c, a, st.pinned)
	})
	t := smooth(st.fade)
	ip.iconButton(buf, th, st, tMenu, menu, tint, in, func(cx, cy float64, c rgb, a float64) {
		if t < 1 {
			menuIcon(buf, w, h, st.prev, cx, cy, s, c, a*(1-t))
		}
		menuIcon(buf, w, h, st.view, cx, cy, s, c, a*t)
	})

	// Body: the view that goes fades first, then the new one comes in.
	shift := 8.0
	if st.prev < st.view {
		shift = -8
	}
	if t < 1 {
		ip.body(buf, th, st, st.prev, left+shift*t, top, ink, muted, tint, in*smooth(clamp(1-2*t)))
	}
	ip.body(buf, th, st, st.view, left-shift*(1-t), top, ink, muted, tint, in*smooth(clamp(2*t-1)))

	if st.embed > 0 && ip.frame != nil {
		ip.embedded(buf, st, left, top)
	}
	if st.tip > 0 {
		var text string
		var at rect
		switch st.over {
		case tPin:
			at = pin
			text = ip.t("Pin the window: it stays open and you can drag it anywhere")
			if st.pinned {
				text = ip.t("Unpin: back to the pill at the bottom of the screen")
			}
		case tOpenVito:
			at, text = brand, ip.t("Open Vito")
			if !st.stats && st.day.Words > 0 {
				// Today's figures switched off: here they are at a glance.
				text += "\n" + ip.number(st.day.Words) + " " + ip.t("words today") +
					"\n" + ip.duration(st.day.Saved) + " " + ip.t("saved typing time")
			}
		case tMenu:
			at = menu
			text = map[view]string{viewMain: ip.t("Quick settings"), viewSettings: ip.t("Close"),
				viewRules: ip.t("Back to quick settings")}[st.view]
		case tWords:
			at, text = ip.targets[st.over], ip.t("words today")
			if st.day.Words > 0 {
				text += "\n" + ip.number(st.day.Sentences) + " " + ip.t("sentences") + "\n" + ip.number(st.day.Activations) + " " + ip.t("activations")
			}
		case tSaved:
			at, text = ip.targets[st.over], ip.t("saved typing time")
			if st.day.Spoken > 0 {
				text += "\n" + ip.t("spoken today") + " " + ip.duration(st.day.Spoken)
			}
		case tRepaste, tMic, tCopy, tRestore:
			// Placed where they were drawn this frame, which the views
			// and the microphone decide.
			at, text = ip.targets[st.over], map[target]string{
				tCopy:    ip.t("Copy"),
				tRestore: ip.t("Restore and paste"),
				tRepaste: ip.t("Paste this dictation again"),
				tMic:     ip.t("Dictate: click to start, click again to stop"),
			}[st.over]
		}
		if text != "" {
			ip.tooltip(buf, th, st, at, text, left, in*smooth(st.tip))
		}
	}
}

// embedded lays the pill's picture over the panel, its middle on the
// microphone's, inside the panel's edges.
func (ip *islandPainter) embedded(buf []byte, st islandState, left, top float64) {
	w, h := islandWinW*scale, islandWinH*scale
	sw := surfaceW * scale
	dx := (islandWinW - surfaceW) / 2 * scale
	dy := (islandMarkY - pillY) * scale
	x0, x1 := int((left+3)*scale), int((left+panelW-3)*scale)
	y0, y1 := int(max(top+51, islandMarkY-embedLiveUp-blockLine-6)*scale), int((panelBottom-3)*scale)
	a := st.embed
	for y := max(0, y0); y < min(h, y1); y++ {
		sy := y - dy
		if sy < 0 || sy >= surfaceH*scale {
			continue
		}
		for x := max(0, x0); x < min(w, x1); x++ {
			sx := x - dx
			if sx < 0 || sx >= sw {
				continue
			}
			si, di := (sy*sw+sx)*4, (y*w+x)*4
			sa := float64(ip.frame[si+3]) / 255 * a
			if sa <= 0 {
				continue
			}
			for c := 0; c < 4; c++ {
				buf[di+c] = byte(min(255, float64(ip.frame[si+c])*a+float64(buf[di+c])*(1-sa)))
			}
		}
	}
}

// tooltip explains a button in a small dark bubble under it, kept inside the
// panel; over it where the panel ends too soon below (the microphone).
func (ip *islandPainter) tooltip(buf []byte, th theme, st islandState, at rect, text string, left, a float64) {
	w, h := islandWinW*scale, islandWinH*scale
	room := fixed.I(int((panelW - 40) * scale))
	// A line of its own for each thing it says (\n), each wrapped to the room.
	var lines []string
	for _, para := range strings.Split(text, "\n") {
		for para != "" {
			var first string
			first, para = wrap(labelFace, para, room)
			lines = append(lines, first)
		}
	}
	bw := 0.0
	for _, l := range lines {
		_, lw := ip.text(labelFace, l)
		bw = max(bw, lw)
	}
	bw += 20
	bh := 8 + 16*float64(len(lines))
	x := min(max(at.x+at.w/2-bw/2, left+8), left+panelW-8-bw)
	y := at.y + at.h + 6
	if y+bh > panelFoot-4 {
		y = at.y - 6 - bh
	}
	bg, fg := inkFor(st.dark), th.body
	fill(buf, w, h, rect{x, y, bw, bh}, 8, bg, 0.94*a)
	for i, l := range lines {
		ip.line(buf, labelFace, l, x+10, y+16+16*float64(i), fg, a)
	}
}

// pushpin is the pin in the header: tilted while the island comes and goes,
// upright once it is pinned.
func pushpin(buf []byte, w, h int, cx, cy, s float64, c rgb, a float64, upright bool) {
	ang := math.Pi / 4
	if upright {
		ang = 0
	}
	sin, cos := math.Sincos(ang)
	at := func(x, y float64) (float64, float64) {
		return cx + (x*cos-y*sin)*s, cy + (x*sin+y*cos)*s
	}
	seg := func(x0, y0, x1, y1, th float64) {
		ax, ay := at(x0, y0)
		bx, by := at(x1, y1)
		line(buf, w, h, ax, ay, bx, by, th*s, c, a)
	}
	// The body is the part a finger holds, so it is the longest; the point
	// below the collar is shorter than it.
	seg(-3.2, -6, 3.2, -6, 1.6) // the head
	seg(-2, -6, -2, 1, 1.5)     // the body
	seg(2, -6, 2, 1, 1.5)
	seg(-4.5, 1, 4.5, 1, 1.6) // the collar
	seg(0, 1, 0, 6.5, 1.3)    // the point
}

func (ip *islandPainter) body(buf []byte, th theme, st islandState, v view, left, top float64, ink, muted, tint rgb, a float64) {
	if a <= 0.01 {
		return
	}
	switch v {
	case viewMain:
		ip.mainView(buf, th, st, left, top, ink, muted, tint, a)
	case viewSettings:
		ip.settingsView(buf, th, st, left, top, ink, muted, tint, a)
	case viewRules:
		ip.rulesView(buf, th, st, left, top, ink, muted, tint, a)
	}
}

func (ip *islandPainter) mainView(buf []byte, th theme, st islandState, left, top float64, ink, muted, tint rgb, a float64) {
	w, h := islandWinW*scale, islandWinH*scale
	s := float64(scale)
	// The day in two figures, each with the icon its tile has on the stats
	// page: the words on the left, the typing time they saved on the right,
	// the icons at the outer ends. What they are is the tooltip's to say; the
	// row stays quiet, and can be switched off (ui.island_stats).
	if st.stats {
		cy := top + 55.0
		chip := blend(th.body, th.to, 0.13)
		if st.dark {
			chip = blend(th.body, th.to, 0.2)
		}
		// figureW measures a figure: the units smaller than the digits, so
		// 59m 59s fits its half.
		figureW := func(figure []figPart) float64 {
			fw := 0.0
			for i, p := range figure {
				if p.unit {
					_, pw := ip.text(labelFace, p.s)
					fw += pw + 0.5
					if i < len(figure)-1 {
						fw += 3
					}
				} else {
					_, pw := ip.text(statFace, p.s)
					fw += pw
				}
			}
			return fw
		}
		stat := func(tg target, iconX, fx float64, icon func(cx, cy float64), figure []figPart) {
			// The stats page's chip: 34 px round 9 for an 18 px icon; here 24 for 13.
			fill(buf, w, h, rect{iconX, cy - 12, 24, 24}, 7, chip, a)
			icon((iconX+12)*s, cy*s)
			x0 := min(iconX, fx)
			for i, p := range figure {
				if p.unit {
					fx += ip.line(buf, labelFace, p.s, fx+0.5, cy+5.5, ink, a) + 0.5
					if i < len(figure)-1 {
						fx += 3
					}
				} else {
					fx += ip.line(buf, statFace, p.s, fx, cy+6, ink, a)
				}
			}
			x1 := max(iconX+24, fx)
			ip.targets[tg] = rect{x0 - 4, cy - 15, x1 - x0 + 8, 30}
		}
		k := 0.8 * s // the icons a size smaller than in a 30 px chip
		lx, rx := left+panelPad, left+panelW-panelPad-24
		words := []figPart{{s: ip.number(st.day.Words)}}
		saved := ip.durationParts(st.day.Saved)
		stat(tWords, lx, lx+32, func(cx, cy float64) { wordsIcon(buf, w, h, cx, cy, k, th.to, a) }, words)
		stat(tSaved, rx, rx-8-figureW(saved), func(cx, cy float64) { clock(buf, w, h, cx, cy, k, th.to, a) }, saved)
	}

	// The last dictation, to paste again; in the pinned island it makes way
	// for the live words while a dictation runs.
	a *= 1 - smooth(st.embed)
	if a <= 0.01 {
		return
	}
	boxY := top + 77
	if !st.stats {
		boxY -= statsH
	}
	lastBox := rect{left + panelPad - 6, boxY, panelW - 2*panelPad + 12, 68}
	boxTint := tint
	if st.kept {
		// A cancelled dictation, for a few minutes: the whole box restores it.
		ip.targets[tRestore] = lastBox
		if st.over == tRestore {
			boxTint = blend(tint, th.to, 0.08)
		}
		fill(buf, w, h, lastBox, 12, boxTint, a)
		ip.line(buf, labelFace, ip.t("Dictation cancelled"), lastBox.x+12, lastBox.y+20, th.to, a)
		heard := strings.Join(strings.Fields(st.heard), " ")
		if heard == "" {
			heard = ip.t("Click to restore it")
		}
		room := lastBox.w - 12 - 46
		ip.line(buf, bodyFace, tailFit(bodyFace, heard, fixed.I(int(room*s))), lastBox.x+12, lastBox.y+38, ink, a)
		bx, by := lastBox.x+lastBox.w-26, lastBox.y+lastBox.h/2
		if st.over == tRestore {
			fill(buf, w, h, rect{bx - 14, by - 14, 28, 28}, 8, blend(boxTint, th.to, 0.08), a)
		}
		repasteIcon(buf, w, h, bx*s, by*s, s, th.to, a)
		return
	}
	if st.last != "" {
		// The text itself copies it; the button pastes it again.
		ip.targets[tCopy] = rect{lastBox.x, lastBox.y, lastBox.w - 46, lastBox.h}
		if st.over == tCopy {
			boxTint = blend(tint, th.to, 0.08)
		}
	}
	fill(buf, w, h, lastBox, 12, boxTint, a)
	// No label: what the box holds speaks for itself. Up to three lines,
	// the last one shortened, standing in the middle of the box.
	last, c := strings.Join(strings.Fields(st.last), " "), ink
	switch {
	case st.copied:
		last, c = ip.t("Copied"), th.to
	case last == "":
		last, c = ip.t("Nothing dictated yet"), muted
	}
	room := fixed.I(int((lastBox.w - 12 - 46) * s))
	var lines []string
	for rest := last; rest != "" && len(lines) < 3; {
		var line string
		if len(lines) == 2 {
			line, rest = fit(bodyFace, rest, room), ""
		} else {
			line, rest = wrap(bodyFace, rest, room)
		}
		lines = append(lines, line)
	}
	const lastLine = 16.0
	y := lastBox.y + (lastBox.h-float64(len(lines))*lastLine)/2 + 12
	for i, l := range lines {
		ip.line(buf, bodyFace, l, lastBox.x+12, y+float64(i)*lastLine, c, a)
	}
	if st.last != "" {
		btn := rect{lastBox.x + lastBox.w - 40, lastBox.y + lastBox.h/2 - 14, 28, 28}
		// Its hover lights up against the box as the header's do against the
		// panel.
		ip.iconButton(buf, th, st, tRepaste, btn, blend(boxTint, th.to, 0.08), a, func(cx, cy float64, c rgb, al float64) {
			repasteIcon(buf, w, h, cx, cy, s, c, al)
		})
	}
}

func (ip *islandPainter) settingsView(buf []byte, th theme, st islandState, left, top float64, ink, muted, tint rgb, a float64) {
	w, h := islandWinW*scale, islandWinH*scale
	s := float64(scale)
	right := left + panelW - panelPad
	row := func(i int, label string, tg target) float64 {
		y := top + 45 + float64(i)*30
		r := rect{left + panelPad - 6, y - 3, panelW - 2*panelPad + 12, 28}
		if tg != tNone {
			ip.targets[tg] = r
			if st.over == tg {
				fill(buf, w, h, r, 9, tint, a)
			}
		}
		ip.line(buf, bodyFace, label, left+panelPad, y+15, ink, a)
		return y
	}
	toggle := func(i int, label string, on bool, tg target) {
		y := row(i, label, tg)
		switchIcon(buf, w, h, (right-15)*s, (y+11)*s, s, on, th, st.dark, a)
	}
	toggle(0, ip.t("AI cleanup"), st.cleanup, tCleanup)

	// The rule set: its name, and an arrow into the list.
	y := row(1, ip.t("Cleanup rules"), tRules)
	name := fit(labelFace, ip.t(st.activeName()), fixed.I(int(110*s)))
	_, nw := ip.text(labelFace, name)
	c := muted
	if st.over == tRules {
		c = th.to
	}
	ip.line(buf, labelFace, name, right-12-nw, y+15, c, a)
	chevron(buf, w, h, (right-3)*s, (y+11)*s, s, c, a, false)

	toggle(2, ip.t("Enter after text"), st.enter, tEnter)
	if st.partials {
		toggle(3, ip.t("Live text while speaking"), st.live, tLive)
	} else {
		// Nothing to show with a service that hears the recording only
		// whole: say so where the switch would be.
		y := row(3, "", tNone)
		ip.line(buf, bodyFace, ip.t("Live text"), left+panelPad, y+15, muted, a)
		note := ip.t("Not with this service")
		_, nw := ip.text(labelFace, note)
		ip.line(buf, labelFace, note, right-nw, y+15, muted, a)
	}

	toggle(5, ip.t("Today's figures"), st.stats, tStats)

	y = row(4, ip.t("Pill style"), tNone)
	// Three segments in one rounded bar, the chosen one lit.
	seg := 30.0
	x0 := right - 3*seg
	bar := rect{x0, y - 1, 3 * seg, 24}
	fill(buf, w, h, bar, 12, tint, a)
	// The glow first: it is the default.
	order := []struct {
		tg    target
		style Style
	}{{tStyleGlow, StyleGlow}, {tStyleBars, StyleBars}, {tStyleVito, StyleMascot}}
	for i, o := range order {
		tg := o.tg
		r := rect{x0 + float64(i)*seg, y - 1, seg, 24}
		ip.targets[tg] = r
		chosen := o.style == st.style
		c := muted
		if chosen {
			fill(buf, w, h, rect{r.x + 2, r.y + 2, r.w - 4, r.h - 4}, 10, th.body, a)
			ringR(buf, w, h, (r.x+r.w/2)*s, (r.y+r.h/2)*s, (r.w/2-2)*s, (r.h/2-2)*s, 10*s, th.edge, 0.25*a, 0.5*s)
			c = th.to
		} else if st.over == tg {
			c = ink
		}
		styleIcon(buf, w, h, o.style, (r.x+r.w/2)*s, (r.y+r.h/2)*s, s, c, th, a)
	}
}

// rulesView lists the cleanup rule sets, Vito's own first, the active one
// ticked.
func (ip *islandPainter) rulesView(buf []byte, th theme, st islandState, left, top float64, ink, muted, tint rgb, a float64) {
	w, h := islandWinW*scale, islandWinH*scale
	s := float64(scale)
	right := left + panelW - panelPad
	for i, r := range st.rules {
		if i >= maxRules {
			break
		}
		y := top + 45 + float64(i)*ruleRow
		tg := tRule0 + target(i)
		box := rect{left + panelPad - 6, y - 3, panelW - 2*panelPad + 12, ruleRow - 2}
		ip.targets[tg] = box
		if st.over == tg {
			fill(buf, w, h, box, 9, tint, a)
		}
		c := ink
		if r.id == st.active {
			c = th.to
			k := 3.4 * s
			cx, cy := (right-6)*s, (y+11)*s
			line(buf, w, h, cx-k*1.1, cy+0.1*k, cx-0.25*k, cy+0.9*k, 1.7*s, c, a)
			line(buf, w, h, cx-0.25*k, cy+0.9*k, cx+k*1.15, cy-0.8*k, 1.7*s, c, a)
		}
		ip.line(buf, bodyFace, fit(bodyFace, ip.t(r.name), fixed.I(int((panelW-2*panelPad-24)*s))), left+panelPad, y+15, c, a)
	}
}

// mic draws the button at the pill's spot: the hovered pill's microphone,
// becoming a round button in the mark's gradient.
func (ip *islandPainter) mic(buf []byte, w, h int, th theme, st islandState) {
	s := float64(scale)
	e := easeOut(st.open)
	cx, cy := float64(w)/2, islandMarkY*s
	r := mix(9, micR, e)
	gone := smooth(st.embed) // the pill takes its place
	if gone >= 1 {
		return
	}
	if gone < 0.5 {
		ip.targets[tMic] = rect{islandWinW/2 - micR, islandMarkY - micR, 2 * micR, 2 * micR}
	}
	if st.style == StyleMascot {
		// The character is the button: it grows out of the resting one and
		// leans forward under the pointer.
		f := st.rest
		f.Show, f.Dark, f.Fade = 1, st.dark, gone
		k := mix(restSize, 0.36, e)
		if st.over == tMic {
			k *= 1.08
		}
		// drawMascotOn measures in the pill's surface, wider than this window.
		u := float64(surfaceW) / islandWinW
		f.MascotK, f.ChinY = k*u, (islandMarkY+mix(7, 14, e))*u
		drawMascotOn(buf, w, h, f)
		return
	}
	e *= 1 - gone
	if e > 0.05 {
		lit := 0.0
		if st.over == tMic {
			lit = 0.12
		}
		shadow(buf, w, h, cx, cy+1.5*s, r*s, r*s, 6*s, th.shadow*e)
		eachPixel(w, h, cx, cy, r*s+1, r*s+1, func(x, y int, px, py float64) {
			d := math.Hypot(px-cx, py-cy) - r*s
			t := clamp((px - (cx - r*s)) / (2 * r * s))
			c := blend(blend(th.from, th.to, t), rgb{1, 1, 1}, lit)
			put(buf, w, x, y, c, e*clamp(0.5-d))
		})
	}
	micCol := blend(blend(th.from, th.to, 0.5), rgb{1, 1, 1}, e)
	microphone(buf, w, h, cx, cy, s*mix(1, 1.35, e), micCol, 1-gone)
}

// iconButton draws a round button that lights up under the pointer.
func (ip *islandPainter) iconButton(buf []byte, th theme, st islandState, tg target, r rect, tint rgb, a float64, icon func(cx, cy float64, c rgb, a float64)) {
	w, h := islandWinW*scale, islandWinH*scale
	s := float64(scale)
	ip.targets[tg] = r
	cx, cy := (r.x+r.w/2)*s, (r.y+r.h/2)*s
	c := mutedFor(st.dark)
	if st.over == tg {
		box(buf, w, h, cx, cy, r.w*s/2, r.h*s/2, r.h*s/2, blend(tint, th.to, 0.08), a)
		c = th.to
	}
	icon(cx, cy, c, a)
}

// tipped says which targets explain themselves in a tooltip: the icon
// buttons, not the rows that already say what they do.
func tipped(tg target) bool {
	switch tg {
	case tPin, tOpenVito, tMenu, tRepaste, tMic, tCopy, tRestore, tWords, tSaved:
		return true
	}
	return false
}

// clickable: the stats are targets for their tooltips only; a pinned island
// is dragged by them like by its free space.
func clickable(tg target) bool { return tg != tWords && tg != tSaved }

func (ip *islandPainter) hit(x, y float64) target {
	for tg, r := range ip.targets {
		if r.has(x, y) {
			return tg
		}
	}
	return tNone
}

// brandTile is Vito's mark as in the app: a rounded square in the coral to
// violet gradient with the five white bars hanging in it (favicon.svg).
func brandTile(buf []byte, w, h int, cx, cy, size float64, th theme, a float64) {
	half := size / 2
	eachPixel(w, h, cx, cy, half+1, half+1, func(x, y int, px, py float64) {
		d := roundBox(px-cx, py-cy, half, half, size*0.24)
		t := clamp(((px - (cx - half)) + (py - (cy - half))) / (2 * size))
		put(buf, w, x, y, blend(hex(0xFF6B5E), hex(0x7C3AED), t), a*clamp(0.5-d))
	})
	k := size / 100
	for i, bh := range []float64{20, 36, 56, 36, 20} {
		bx := cx - half + (14+0.72*(4+20*float64(i))+0.72*6)*k
		top := cy - half + (14+0.72*22)*k
		hw, hh := 0.72*6*k, 0.72*bh/2*k
		box(buf, w, h, bx, top+hh, hw, hh, hw, rgb{1, 1, 1}, a)
	}
}

// menuIcon is the header's menu button for a view: three lines on the main
// view, a cross in the settings, an arrow back in the list of rule sets.
func menuIcon(buf []byte, w, h int, v view, cx, cy, s float64, c rgb, a float64) {
	if a <= 0 {
		return
	}
	k := 4.6 * s
	switch v {
	case viewMain:
		for _, dy := range []float64{-3.6, 0, 3.6} {
			line(buf, w, h, cx-k, cy+dy*s, cx+k, cy+dy*s, 1.6*s, c, a)
		}
	case viewSettings:
		line(buf, w, h, cx-k*0.8, cy-k*0.8, cx+k*0.8, cy+k*0.8, 1.6*s, c, a)
		line(buf, w, h, cx-k*0.8, cy+k*0.8, cx+k*0.8, cy-k*0.8, 1.6*s, c, a)
	case viewRules:
		chevron(buf, w, h, cx, cy, s*1.2, c, a, true)
	}
}

// chevron is a small › (or ‹ when back), centred on cx, cy.
func chevron(buf []byte, w, h int, cx, cy, s float64, c rgb, a float64, back bool) {
	d := 1.0
	if back {
		d = -1
	}
	line(buf, w, h, cx-1.6*s*d, cy-3.6*s, cx+1.8*s*d, cy, 1.6*s, c, a)
	line(buf, w, h, cx+1.8*s*d, cy, cx-1.6*s*d, cy+3.6*s, 1.6*s, c, a)
}

// clock marks the saved time: violet once there is some, as on the stats page.
// clock is the stats page's time icon (I.time): a dial with its hands, drawn
// 16 px for its 24-unit box.
func clock(buf []byte, w, h int, cx, cy, s float64, c rgb, a float64) {
	u := s * 16 / 24
	// ring's thickness is half the stroke, inside its edge: an SVG stroke of
	// 1.7 is centred on the radius.
	ring(buf, w, h, cx, cy, 9.8*u, 9.8*u, c, a, 0.8*u)
	// Round ends read a little heavier than the SVG's butt ends.
	line(buf, w, h, cx, cy-5*u, cx, cy, 1.55*u, c, a)
	line(buf, w, h, cx, cy, cx+3*u, cy+2*u, 1.55*u, c, a)
}

// wordsIcon is the stats page's words icon (I.words): three lines of text,
// the last one short.
func wordsIcon(buf []byte, w, h int, cx, cy, s float64, c rgb, a float64) {
	u := s * 16 / 24
	for i, l := range []float64{16, 16, 10} {
		y := cy + float64(i-1)*5*u
		x0 := cx - 8*u
		line(buf, w, h, x0+0.4*u, y, x0+l*u-0.4*u, y, 1.55*u, c, a)
	}
}

// figPart is a piece of a stat's figure: a number, or its unit drawn smaller.
type figPart struct {
	s    string
	unit bool
}

// durationParts writes a time given in seconds short, so it fits the stat's
// column: 40s, 4m 3s, and from an hour on 1h 3m (no seconds then).
func (ip *islandPainter) durationParts(sec int) []figPart {
	h, m, s := sec/3600, sec%3600/60, sec%60
	var out []figPart
	add := func(n int, u string) { out = append(out, figPart{s: strconv.Itoa(n)}, figPart{s: ip.t(u), unit: true}) }
	switch {
	case h > 0:
		add(h, "h")
		add(m, "m")
	case m > 0:
		add(m, "m")
		if s > 0 {
			add(s, "s")
		}
	default:
		add(s, "s")
	}
	return out
}

// duration is durationParts as one string, for a tooltip.
func (ip *islandPainter) duration(sec int) string {
	var b strings.Builder
	for i, p := range ip.durationParts(sec) {
		if i > 0 && !p.unit {
			b.WriteRune(' ') // a tooltip wraps around the time, not in it
		}
		b.WriteString(p.s)
	}
	return b.String()
}

// number writes n with the thousands grouped the way the language does.
func (ip *islandPainter) number(n int) string {
	sep := ","
	switch ip.lang {
	case "nl", "de", "da", "es", "it", "id", "pt", "tr", "el", "ro", "sl", "hr", "sr", "bs":
		sep = "."
	case "fr", "fi", "sv", "nb", "cs", "sk", "pl", "ru", "uk", "bg", "hu", "et", "lt", "lv":
		sep = " "
	}
	str := strconv.Itoa(n)
	var b strings.Builder
	for i, c := range str {
		if i > 0 && (len(str)-i)%3 == 0 {
			b.WriteString(sep)
		}
		b.WriteRune(c)
	}
	return b.String()
}

func inkFor(dark bool) rgb {
	if dark {
		return rgb{0.95, 0.94, 0.98}
	}
	return rgb{0.17, 0.14, 0.25}
}

func mutedFor(dark bool) rgb {
	if dark {
		return rgb{0.66, 0.63, 0.74}
	}
	return rgb{0.45, 0.42, 0.53}
}

// fill paints a rounded rectangle given in logical pixels.
func fill(buf []byte, w, h int, r rect, radius float64, c rgb, a float64) {
	s := float64(scale)
	box(buf, w, h, (r.x+r.w/2)*s, (r.y+r.h/2)*s, r.w*s/2, r.h*s/2, radius*s, c, a)
}

// repasteIcon is a round arrow going back around.
func repasteIcon(buf []byte, w, h int, cx, cy, s float64, c rgb, a float64) {
	r := 5.6 * s
	eachPixel(w, h, cx, cy, r+2*s, r+2*s, func(x, y int, px, py float64) {
		dx, dy := px-cx, py-cy
		ang := math.Atan2(dy, dx) // -π..π, 0 to the right
		if ang > -0.35 && ang < 0.95 {
			return // the gap where the arrow head is
		}
		d := math.Abs(math.Hypot(dx, dy)-r) - 0.8*s
		put(buf, w, x, y, c, a*clamp(0.5-d))
	})
	// The head, at the end of the arc above the gap.
	ex, ey := cx+r*math.Cos(-0.35), cy+r*math.Sin(-0.35)
	line(buf, w, h, ex, ey, ex-3.2*s, ey-0.6*s, 1.6*s, c, a)
	line(buf, w, h, ex, ey, ex+0.6*s, ey-3.2*s, 1.6*s, c, a)
}

// switchIcon is an on/off switch, centred on cx, cy.
func switchIcon(buf []byte, w, h int, cx, cy, s float64, on bool, th theme, dark bool, a float64) {
	tw, tr := 15*s, 9*s
	track := blend(th.body, inkFor(dark), 0.18)
	if on {
		eachPixel(w, h, cx, cy, tw+1, tr+1, func(x, y int, px, py float64) {
			c := blend(th.from, th.to, clamp((px-(cx-tw))/(2*tw)))
			put(buf, w, x, y, c, a*clamp(0.5-roundBox(px-cx, py-cy, tw, tr, tr)))
		})
	} else {
		box(buf, w, h, cx, cy, tw, tr, tr, track, a)
	}
	kx := cx - tw + tr
	if on {
		kx = cx + tw - tr
	}
	box(buf, w, h, kx, cy, tr-2*s, tr-2*s, tr-2*s, rgb{1, 1, 1}, a)
}

// styleIcon is the little picture of a pill style in the segmented control.
func styleIcon(buf []byte, w, h int, st Style, cx, cy, s float64, c rgb, th theme, a float64) {
	switch st {
	case StyleBars:
		for i, bh := range []float64{2.4, 4.4, 6, 4.4, 2.4} {
			box(buf, w, h, cx+(float64(i)-2)*2.9*s, cy, 0.85*s, bh*s, 0.85*s, c, a)
		}
	case StyleGlow:
		var prev pt
		for i := 0; i <= 16; i++ {
			x := cx + (float64(i)/16-0.5)*12*s
			y := cy + 1.5*s - 2.4*s*math.Sin(float64(i)/16*2*math.Pi)
			if i > 0 {
				line(buf, w, h, prev.x, prev.y, x, y, 1.6*s, c, a)
			}
			prev = pt{x, y}
		}
	case StyleMascot:
		face := []pt{{cx - 6*s, cy - 3*s}, {cx + 6*s, cy - 3*s}, {cx, cy + 6*s}}
		for i := range face {
			j := (i + 1) % len(face)
			line(buf, w, h, face[i].x, face[i].y, face[j].x, face[j].y, 1.5*s, c, a)
		}
		box(buf, w, h, cx-2*s, cy-0.5*s, 0.9*s, 0.9*s, 0.9*s, c, a)
		box(buf, w, h, cx+2*s, cy-0.5*s, 0.9*s, 0.9*s, 0.9*s, c, a)
		for i := -1; i <= 1; i++ {
			box(buf, w, h, cx+float64(i)*2.6*s, cy-5.2*s, 0.7*s, 1.4*s, 0.7*s, c, a)
		}
	}
}

// runIsland holds the island on one screen for as long as the pointer is on
// it, or for good once pinned, and does what is clicked. It reports whether a
// pinned island lost its window and should come back.
func (p *pill) runIsland(output uint32) (reopen bool) {
	defer func() { p.islandShut.Store(time.Now().UnixMilli()) }()
	if err := faces(); err != nil {
		p.log.Debug("no island", "err", err)
		return false
	}
	st := islandState{dark: p.dark(), view: viewMain, prev: viewMain, fade: 1}
	read := func() {
		c := p.d.Config()
		st.cleanup, st.enter, st.live = c.Cleanup.Enabled, c.Injection.AppendEnter, c.UI.PillLiveEnabled()
		st.stats = c.UI.IslandStatsEnabled()
		st.partials = stt.HasPartials(c.STT)
		st.style = StyleOf(c.UI.PillStyle)
		st.rules = st.rules[:0]
		for _, b := range cleanup.Builtins() {
			st.rules = append(st.rules, ruleSet{b.ID, b.Name})
		}
		for _, r := range c.Cleanup.Prompts {
			st.rules = append(st.rules, ruleSet{r.ID, r.Name})
		}
		st.active = c.Cleanup.ActivePrompt
		if st.active == "" || st.activeName() == "" || !st.known(st.active) {
			st.active = cleanup.BuiltinPrefix + "default"
		}
		st.dark = p.dark()
	}
	day := func() {
		if p.src.Today != nil {
			st.day = p.src.Today()
		}
		if p.src.Last != nil {
			st.last = p.src.Last()
		}
	}
	read()
	day()
	st.ph = st.height(viewMain)
	pin := p.d.Config().UI.IslandPin
	st.pinned = pin != nil

	// shift: pinned near an edge, the picture is drawn this many logical
	// pixels higher in the window than laid out, so the panel can reach the
	// top of the screen, which the window itself may not cross.
	var shift int
	panelBox := func(height float64) [4]int {
		return [4]int{int((islandWinW - panelW) / 2), int(panelFoot-height) - shift, int(panelW), int(height + 2)}
	}
	// region: the panel, and under it only the microphone's room (or the
	// embedded pill's), so the free space beside it lets the pointer through.
	region := func(height float64) [][4]int {
		return [][4]int{panelBox(height), {int(islandWinW/2 - 60), int(panelFoot) - shift, 120, int(footH)}}
	}
	// foldRegion: the folded island's resting pill, where its foot is.
	foldRegion := func() [4]int {
		x, y, iw, ih := idleInputBox(st.style)
		return [4]int{x + foldDX, y + foldDY - shift, iw, ih}
	}
	// A pinned island opens on the screen it was pinned to (after a restart,
	// or when its window came back), not on the one in use; only when that
	// screen is gone does it fall back to the one in use.
	if output == 0 && pin != nil {
		output = outputNamed(pin.Screen)
	}
	w, err := openIslandWindow(output, islandWinW, islandWinH, inputArea(region(st.ph)).union())
	if err != nil {
		p.log.Debug("no island", "err", err)
		return false
	}
	w.setInput(region(st.ph)...)
	defer w.close()
	mv, _ := w.(mover)
	var offX, offUp int   // where a pinned island stands
	var wantX, wantUp int // and where it was put, which a taller view may not allow
	// keepOn holds the panel on its screen, when the desktop tells how big
	// that is: a fling of the mouse must not lose it.
	var sw, sh int
	var sized bool
	// A window opened on "the screen in use" is not told which one that is;
	// the desktop says. Without a screen size the drag cannot place the
	// window from its left edge, and on niri the fallback moved it twice as
	// far as the pointer.
	inUse := ""
	if w.connector() == "" {
		inUse = focusedScreen()
	}
	measure := func() {
		c := w.connector()
		if c == "" {
			c = inUse
		}
		sw, sh, sized = screenSize(c)
	}
	keepOn := func() {
		offUp = max(offUp, -(islandMargin + int(islandWinH-panelBottom)))
		if !sized {
			return
		}
		maxX := max(0, sw/2-int(panelW)/2)
		offX = min(max(offX, -maxX), maxX)
		// The panel as tall as it is now may reach the top edge; a taller
		// view moves it down again (goTo).
		offUp = min(offUp, max(0, sh-islandMargin-int(islandWinH-panelBottom)-int(st.height(st.view)+footH)-panelGap))
	}
	// place puts a pinned window where the panel's foot is dragged to: the
	// window as close over the panel as the screen lets it, and the picture
	// shifted in it to match.
	place := func() {
		if !sized {
			mv.setOffset(offX, offUp, sw)
			return
		}
		foot := sh - islandMargin - int(islandWinH-panelBottom) - offUp
		top := foot - int(math.Round(st.ph+footH))
		win := min(max(top-panelGap, 0), sh-islandWinH)
		mv.setPlace(sw/2-islandWinW/2+offX, win, sh)
		if now := int(math.Round(st.top())) - (top - win); now != shift {
			shift = now
			w.setInput(region(max(st.ph, st.height(st.view)))...)
		}
	}
	if st.pinned && mv != nil {
		offX, offUp = pin.X, pin.Up
		wantX, wantUp = offX, offUp
		measure()
		keepOn()
		place()
	}
	p.pinnedUp.Store(st.pinned)
	defer p.pinnedUp.Store(false)
	ip := newIslandPainter(uitext.ResolveLang(p.d.Config().UI.Lang))

	buf := make([]byte, islandWinW*scale*islandWinH*scale*4)
	frame := make([]byte, surfaceW*scale*surfaceH*scale*4)
	out := make([]byte, len(buf))
	tick := time.NewTicker(frameTime)
	defer tick.Stop()
	var (
		inside    bool
		left      = time.Now().Add(1500 * time.Millisecond) // time to reach it
		closing   bool
		px, py    float64 // where the pointer is on the picture
		rx, ry    float64 // and in the window, as the desktop says it
		dim       float64 // 0 bright .. 1 faded
		busyAt    = time.Now()
		dirty     = true
		overSince time.Time // when the pointer came onto st.over
		copiedAt  time.Time // when the last dictation was copied
		actor     = newRestActor(time.Now())
		dragging  bool
		// pressed: the button went down on the free space of an island not
		// yet pinned, at pressX, pressY. Moved from there, it pins itself and
		// is dragged, with no need to pin it first.
		pressed        bool
		pressX, pressY float64
		gx, gy         float64 // where the drag took hold
		rel            bool    // the drag follows the pointer's own motion
		ax, ay         float64 // of which this much is not applied yet
		startX         int     // where the window was when the drag took hold
		startUp        int
		deltas         int  // pointer motions of its own during this drag
		moves          int  // and positions
		inPill         bool // the pointer is over the embedded pill
		embedded       *embedWindow
		frames         int
	)
	change := func(mut func(*config.Config)) {
		c := p.d.Config()
		mut(&c)
		if err := p.d.SetConfig(c); err != nil {
			p.log.Warn("island: setting not applied", "err", err)
		}
		read()
	}
	savePin := func() {
		screen := w.connector()
		change(func(c *config.Config) { c.UI.IslandPin = &config.IslandPin{Screen: screen, X: offX, Up: offUp} })
	}
	goTo := func(v view) {
		st.prev, st.view, st.fade = st.view, v, 0
		// While it changes, the region covers both views' panels.
		w.setInput(region(max(st.height(st.prev), st.height(v)))...)
		// Pinned near the top, a taller view moves down to fit.
		if st.pinned && mv != nil {
			offX, offUp = wantX, wantUp
			keepOn()
			place()
		}
	}
	// overPill says whether a point is on the embedded pill's part of the panel.
	overPill := func(x, y float64) bool {
		l := (islandWinW - panelW) / 2
		return embedded != nil && st.embed > 0.5 && x >= l && x < l+panelW && y >= islandMarkY-embedLiveUp-blockLine-6 && y < panelBottom
	}
	// toPill turns a point on the island into one on the pill's picture.
	toPill := func(e pointerEvent) pointerEvent {
		e.x += (surfaceW - islandWinW) / 2
		e.y += pillY - islandMarkY
		return e
	}
	// handle takes one pointer event. A motion only moves the pointer; the
	// hover follows from it once per frame.
	startDrag := func(x, y float64) {
		dragging, gx, gy = true, x, y
		rel, ax, ay, startX, startUp = !dragFromPress() && mv.relative(), 0, 0, offX, offUp
		deltas, moves = 0, 0
		measure()
	}
	handle := func(e pointerEvent) {
		switch e.kind {
		case pointerIn, pointerMove:
			if pressed && !dragging && math.Hypot(e.x-pressX, e.y-pressY) > 4 {
				pressed = false
				st.pinned = true
				offX, offUp, wantX, wantUp = 0, 0, 0, 0
				savePin()
				p.pinnedUp.Store(true)
				st.tip, overSince = 0, time.Now().Add(time.Hour)
				dirty = true
				startDrag(pressX, pressY) // the way since the press counts
			}
			if dragging {
				// A desktop may offer relative motion and not send it (a
				// nested compositor): then the positions it is.
				if moves++; rel && deltas == 0 && moves > 3 {
					rel = false
				}
			}
			inside, rx, ry = true, e.x, e.y
			px, py = rx, ry+float64(shift)
			busyAt = time.Now()
			if now := overPill(px, py); now || inPill {
				if now && !inPill {
					embedded.forward(toPill(pointerEvent{kind: pointerIn, x: px, y: py}))
				} else if now {
					embedded.forward(toPill(pointerEvent{kind: pointerMove, x: px, y: py}))
				} else {
					embedded.forward(pointerEvent{kind: pointerOut})
				}
				inPill = now
			}
		case pointerOut:
			inside, left = false, time.Now()
			if inPill {
				embedded.forward(pointerEvent{kind: pointerOut})
				inPill = false
			}
		case pointerDelta:
			if dragging {
				ax, ay = ax+e.x, ay+e.y
				deltas++
			}
		case pointerRelease:
			pressed = false
			if dragging && e.button == buttonLeft {
				dragging = false
				wantX, wantUp = offX, offUp
				savePin()
			}
		case pointerClick:
			busyAt = time.Now()
			if inPill {
				embedded.forward(toPill(pointerEvent{kind: pointerClick, x: px, y: py, button: e.button}))
				return
			}
			if e.button != buttonLeft {
				return
			}
			dirty = true
			// A click answers the tooltip's question: it goes until the
			// pointer moves on to something else.
			st.tip, overSince = 0, time.Now().Add(time.Hour)
			tg := ip.hit(px, py)
			switch {
			case (tg == tNone || !clickable(tg)) && st.pinned && mv != nil:
				startDrag(rx, ry)
			case (tg == tNone || !clickable(tg)) && mv != nil:
				pressed, pressX, pressY = true, rx, ry
			case tg == tMic:
				closing = !st.pinned
				p.clickIdle(output)
			case tg == tOpenVito:
				closing = !st.pinned
				if err := webapp.Open(fmt.Sprintf("http://127.0.0.1:%d", p.d.Config().Server.Port)); err != nil {
					p.log.Warn("island: open Vito failed", "err", err)
				}
			case tg == tPin:
				st.pinned = !st.pinned
				if st.pinned {
					offX, offUp, wantX, wantUp = 0, 0, 0, 0
					savePin()
				} else {
					change(func(c *config.Config) { c.UI.IslandPin = nil })
					if mv != nil {
						mv.setOffset(0, 0, 0)
						shift = 0
						w.setInput(region(st.height(st.view))...)
					}
				}
				p.pinnedUp.Store(st.pinned)
				overSince = time.Now() // the other tooltip, after a fresh wait
				st.tip = 0
			case tg == tMenu:
				goTo(map[view]view{viewMain: viewSettings, viewSettings: viewMain, viewRules: viewSettings}[st.view])
			case tg == tRepaste:
				closing = !st.pinned
				text := st.last
				go func() {
					time.Sleep(islandOpen) // let the island get out of the way first
					if err := p.d.InjectText(text); err != nil {
						p.log.Warn("island: paste again failed", "err", err)
					}
				}()
			case tg == tRestore:
				closing = !st.pinned
				go func() {
					time.Sleep(islandOpen) // let the island get out of the way first
					if err := p.d.RestoreCancelled(); err != nil {
						p.log.Warn("island: restore failed", "err", err)
					}
				}()
			case tg == tCopy:
				text := st.last
				st.copied, copiedAt = true, time.Now()
				go func() {
					if _, err := inject.Inject(config.Injection{Mode: string(inject.ModeClipboardOnly)}, text); err != nil {
						p.log.Warn("island: copy failed", "err", err)
					}
				}()
			case tg == tCleanup:
				change(func(c *config.Config) { c.Cleanup.Enabled = !c.Cleanup.Enabled })
			case tg == tRules:
				goTo(viewRules)
			case tg == tEnter:
				change(func(c *config.Config) { c.Injection.AppendEnter = !c.Injection.AppendEnter })
			case tg == tLive:
				change(func(c *config.Config) { on := !c.UI.PillLiveEnabled(); c.UI.PillLive = &on })
			case tg == tStats:
				change(func(c *config.Config) { on := !c.UI.IslandStatsEnabled(); c.UI.IslandStats = &on })
			case tg == tStyleBars, tg == tStyleGlow, tg == tStyleVito:
				name := map[target]string{tStyleBars: "bars", tStyleGlow: "", tStyleVito: "vito"}[tg]
				change(func(c *config.Config) { c.UI.PillStyle = name })
			case tg >= tRule0 && int(tg-tRule0) < len(st.rules):
				id := st.rules[tg-tRule0].id
				change(func(c *config.Config) { c.Cleanup.ActivePrompt = id })
				goTo(viewSettings)
			}
		}
	}
	for {
		select {
		case e := <-w.pointer():
			// Take every event waiting before drawing: a mouse moves many
			// times per frame, and a picture per motion fell behind.
			handle(e)
			for drained := false; !drained; {
				select {
				case e := <-w.pointer():
					handle(e)
				default:
					drained = true
				}
			}
		case <-w.gone():
			return st.pinned
		case <-tick.C:
		}
		frames++
		if st.copied && time.Since(copiedAt) > copiedFor {
			st.copied, dirty = false, true
		}
		if !st.pinned {
			if !inside && time.Since(left) > islandGrace {
				closing = true
			}
			if p.d.Status().State != daemon.StateIdle {
				closing = true
			}
		} else {
			if frames%60 == 0 { // a window that stays follows the settings
				read()
				dirty = true
			}
			// Options left open go back to the main view once the user has
			// gone elsewhere: the pointer away for a while, or a dictation.
			if st.view != viewMain && st.fade >= 1 && !dragging &&
				(!inside && time.Since(left) > optionsGrace || p.d.Status().State != daemon.StateIdle) {
				goTo(viewMain)
				dirty = true
			}
		}
		// Moved by its free space: the pointer's way from where it took hold.
		if dragging && mv != nil {
			var dx, dy int
			if rel {
				dx, dy = int(math.Round(ax)), int(math.Round(ay))
				ax, ay = ax-float64(dx), ay-float64(dy)
			} else {
				// No motion of its own: the position. During a drag niri
				// measures it from where the window was when the button
				// went down, so it is the whole way since then.
				dx = startX + int(math.Round(rx-gx)) - offX
				dy = offUp - (startUp - int(math.Round(ry-gy)))
			}
			if dx != 0 || dy != 0 {
				offX, offUp = offX+dx, offUp-dy
				keepOn()
				place()
				dirty = true
			}
		}
		if heard, ok := p.d.Cancelled(); ok != st.kept || heard != st.heard {
			st.kept, st.heard = ok, heard
			dirty = true
		}
		// The pill, while it shows in the pinned island.
		if now := p.embed.Load(); now != embedded {
			if now == nil {
				day() // a dictation ended: the figures and the last text moved on
				if inPill {
					inPill = false
				}
			} else if st.view != viewMain {
				goTo(viewMain)
			}
			embedded = now
		}
		moving := false
		if st.style == StyleMascot && st.embed < 1 && actor.update(time.Now()) {
			actor.frame(time.Now(), &st.rest)
			moving = true
		} else if st.style == StyleMascot && (st.rest.Blink != 1 || st.rest.Pose != "") {
			actor.frame(time.Now(), &st.rest) // the gesture's end: back at rest
			dirty = true
		}
		if embedded != nil {
			if embedded.take(frame) {
				dirty = true
			}
			ip.frame = frame
			if st.embed < 1 {
				st.embed = min(1, st.embed+step(islandView))
				moving = true
			}
		} else if st.embed > 0 {
			st.embed = max(0, st.embed-step(islandView))
			moving = true
		}
		// Step the animations.
		if closing {
			st.open = max(0, st.open-step(islandOpen))
			moving = true
		} else if st.open < 1 {
			st.open = min(1, st.open+step(islandOpen))
			moving = true
		}
		if st.fade < 1 {
			st.fade = min(1, st.fade+step(islandView))
			if st.fade == 1 {
				w.setInput(region(st.height(st.view))...)
			}
			moving = true
		}
		if want := st.height(st.view); st.ph != want {
			st.ph += (want - st.ph) * 0.28
			if math.Abs(want-st.ph) < 0.5 {
				st.ph = want
			}
			if st.pinned && sized && mv != nil {
				place() // the panel's foot stays; the window follows its top
			}
			moving = true
		}
		py = ry + float64(shift)
		// Left alone, a pinned island fades back; the pointer or a dictation
		// brings it back.
		if embedded != nil || inside || dragging || !st.pinned {
			busyAt = time.Now()
		}
		want := 0.0
		if time.Since(busyAt) > dimAfter {
			want = 1
		}
		if fold := st.pinned && time.Since(busyAt) > dimAfter+compactAfter; fold && st.compact < 1 {
			if st.compact = min(1, st.compact+step(compactTime)); st.compact == 1 {
				w.setInput(foldRegion())
			}
			moving = true
		} else if !fold && st.compact > 0 {
			if st.compact == 1 {
				w.setInput(region(st.height(st.view))...)
			}
			st.compact = max(0, st.compact-step(compactTime/2))
			moving = true
		}
		if dim < want { // fading out slowly, back in quickly
			dim = min(want, dim+step(dimTime*2))
			moving = true
		} else if dim > want {
			dim = max(want, dim-step(dimTime/2))
			moving = true
		}
		over := tNone
		if inside && !dragging && !inPill {
			over = ip.hit(px, py)
		}
		if over != st.over {
			st.over, dirty = over, true
			overSince, st.tip = time.Now(), 0
		}
		// A tooltip for every button: what an icon does is not always plain.
		if tipped(st.over) && time.Since(overSince) > tipDelay && st.tip < 1 {
			st.tip = min(1, st.tip+step(hoverTime))
			moving = true
		}
		if mv != nil && inside {
			shape := uint32(cursorShapePointer)
			switch {
			case dragging:
				shape = cursorShapeGrabbing
			case st.pinned && (over == tNone || !clickable(over)) && !inPill:
				shape = cursorShapeGrab
			}
			mv.setCursor(shape)
		}
		if moving || dirty {
			ip.draw(buf, st)
			if err := w.present(fitPicture(out, buf, shift*scale, 1-(1-dimTo)*smooth(dim))); err != nil {
				return st.pinned
			}
			dirty = false
		}
		if closing && st.open == 0 {
			return false
		}
	}
}

// fitPicture is the picture as the window shows it: rows moved up by shift
// (down when negative) and everything faded to alpha. Premultiplied, so a fade
// scales every channel. It hands back pic itself when there is nothing to do.
func fitPicture(out, pic []byte, shift int, alpha float64) []byte {
	if shift == 0 && alpha >= 1 {
		return pic
	}
	stride := islandWinW * scale * 4
	rows := len(pic) / stride
	clear(out)
	k := uint32(alpha * 256)
	for y := 0; y < rows; y++ {
		sy := y + shift
		if sy < 0 || sy >= rows {
			continue
		}
		src, dst := pic[sy*stride:(sy+1)*stride], out[y*stride:(y+1)*stride]
		if alpha >= 1 {
			copy(dst, src)
			continue
		}
		for i, v := range src {
			dst[i] = byte(uint32(v) * k >> 8)
		}
	}
	return out
}

// known says whether id is one of the rule sets.
func (st *islandState) known(id string) bool {
	for _, r := range st.rules {
		if r.id == id {
			return true
		}
	}
	return false
}
