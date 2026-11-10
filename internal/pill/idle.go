package pill

import (
	"errors"
	"image"
	"time"

	"vito/internal/daemon"
)

// retryTime is the wait before reconnecting after the compositor went away.
const retryTime = 2 * time.Second

// How far a resting pill stands out, on the screen in use and on the others.
// Where the desktop does not say which that is, every pill stays at 1.
const (
	presenceActive   = 1.45
	presenceInactive = 0.45

	// Left alone this long, the resting pills fade by restDim; the pointer on
	// one or a dictation brings them back.
	restAfter = time.Minute
	restDim   = 0.6
	restTime  = time.Second
)

// runIdle keeps the resting pill on every screen while the setting is on.
func (p *pill) runIdle() {
	for {
		if !p.idleWanted() {
			<-p.settings
			continue
		}
		stop := make(chan struct{})
		outputs := make(chan []uint32, 1)
		ended := make(chan error, 1)
		focused := make(chan string, 1)
		go watchFocus(stop, func(name string) {
			select {
			case <-focused:
			default:
			}
			focused <- name
		})
		go func() {
			ended <- watchOutputs(stop, func(list []uint32) {
				select { // only the latest list matters
				case <-outputs:
				default:
				}
				outputs <- list
			})
		}()
		err := p.keepIdle(outputs, focused, ended, stop)
		switch {
		case errors.Is(err, errUnsupported):
			<-p.settings // nothing changes here until the setting does
		case err != nil:
			p.log.Debug("resting pill restarts", "err", err)
			time.Sleep(retryTime)
		}
	}
}

// keepIdle holds the windows until the setting goes off or the watcher ends.
func (p *pill) keepIdle(outputs <-chan []uint32, focused <-chan string, ended <-chan error, stop chan struct{}) error {
	windows := map[uint32]window{}
	lost := make(chan uint32, 8)
	pointers := make(chan idlePointer, 16)
	// The pointer over a resting pill: where its growing is, and where it goes.
	hover, hoverTo := map[uint32]float64{}, map[uint32]float64{}
	var tick <-chan time.Time
	var ticker *time.Ticker
	// The pointer resting on a pill opens the island there.
	dwelled := make(chan uint32, 4)
	dwell := map[uint32]*time.Timer{}
	defer func() {
		for _, t := range dwell {
			t.Stop()
		}
	}()
	defer func() {
		if ticker != nil {
			ticker.Stop()
		}
	}()
	var last []uint32
	dark := p.dark()
	shown, shownFont, shownWorn := pal(), fcs(), wornNow() // the colour theme and typeface the pictures are in
	// The character, when it is the style, lives a little between dictations;
	// wake brings the frames back for its next blink or gesture.
	style := StyleOf(p.d.Config().UI.PillStyle)
	actor := newRestActor(time.Now())
	var wake <-chan time.Time
	sleep := func() {
		if style == StyleMascot && ticker == nil {
			wake = time.After(actor.wait(time.Now()))
		}
	}
	rest, restAt := 0.0, time.Now() // how far they have faded, and since when nothing happened
	// A long recording turns the pills into its chip; clock is its time drawn.
	lv := p.longNow()
	var clock *image.Alpha
	drawClock := func() {
		if lv.on && faces() == nil {
			clock, _ = textMask(fcs().main, longClock(lv.secs))
		}
	}
	drawClock()
	inputFor := func() (int, int, int, int) {
		if lv.on {
			return longInputBox()
		}
		return idleInputBox(style)
	}
	slow := time.NewTicker(500 * time.Millisecond)
	defer slow.Stop()
	animate := func() {
		if ticker == nil {
			ticker = time.NewTicker(frameTime)
			tick = ticker.C
		}
	}
	active := "" // the screen in use, "" as long as the desktop has not said
	presence := func(w window) float64 {
		k := 1 - restDim*smooth(rest)
		switch active {
		case "":
			return k
		case w.connector():
			return presenceActive * k
		}
		return presenceInactive * k
	}
	paint := func(name uint32) {
		w := windows[name]
		if w == nil {
			return
		}
		if err := w.present(p.restPicture(dark, presence(w), hover[name], style, actor, w.connector(), lv, clock)); err != nil {
			p.log.Debug("resting pill not repainted", "err", err)
		}
	}
	repaint := func() {
		for name := range windows {
			paint(name)
		}
	}
	defer func() {
		for _, w := range windows {
			w.close()
		}
	}()
	sync := func() {
		keep := map[uint32]bool{}
		for _, name := range last {
			keep[name] = true
			if windows[name] != nil {
				continue
			}
			x, y, iw, ih := inputFor()
			w, err := openIdleWindow(name, [4]int{x, y, iw, ih})
			if err != nil {
				p.log.Debug("no resting pill on a screen", "err", err)
				continue
			}
			windows[name] = w
			p.works.Store(true)
			if err := w.present(p.restPicture(dark, presence(w), 0, style, actor, w.connector(), lv, clock)); err != nil {
				p.log.Debug("resting pill not painted", "err", err)
			}
			go func() {
				for {
					select {
					case e := <-w.pointer():
						pointers <- idlePointer{name, e}
					case <-w.gone():
						lost <- name
						return
					}
				}
			}()
		}
		for name, w := range windows {
			if !keep[name] {
				delete(windows, name)
				w.close()
			}
		}
	}
	sleep()
	for {
		select {
		case <-wake:
			wake = nil
			animate()
		case <-p.restNudge:
			// The character steps aside for the dictation's, which grows out
			// of it, and is back when that one has settled into its place.
			if style == StyleMascot {
				repaint()
			}
		case last = <-outputs:
			sync()
		case active = <-focused:
			repaint()
		case name := <-lost:
			delete(windows, name)
			delete(hover, name)
			delete(hoverTo, name)
		case ip := <-pointers:
			switch ip.e.kind {
			case pointerIn, pointerMove:
				// The pill does not grow under the pointer: the island opens
				// in its place.
				restAt = time.Now()
				if dwell[ip.name] == nil {
					name := ip.name
					dwell[name] = time.AfterFunc(islandDwell, func() { dwelled <- name })
				}
			case pointerOut:
				hoverTo[ip.name] = 0
				if t := dwell[ip.name]; t != nil {
					t.Stop()
					delete(dwell, ip.name)
				}
			case pointerClick:
				p.log.Debug("resting pill clicked", "button", ip.e.button)
				switch {
				case ip.e.button == buttonLeft && lv.on:
					// The recording's chip opens its controls.
					p.openIsland(ip.name)
				case ip.e.button == buttonLeft:
					p.clickIdle(ip.name)
				case ip.e.button == buttonRight:
					p.openIsland(ip.name)
				}
			}
			if hover[ip.name] != hoverTo[ip.name] || rest > 0 {
				animate()
			}
		case <-slow.C:
			if state, _, _ := p.snapshot(); state != daemon.StateIdle {
				restAt = time.Now()
			}
			// The long recording's chip: its time, and whether it is there.
			if now := p.longNow(); now != lv {
				was := lv.on
				lv = now
				drawClock()
				if lv.on != was {
					x, y, iw, ih := inputFor()
					for _, w := range windows {
						w.setInput([4]int{x, y, iw, ih})
					}
				}
				repaint()
			}
			if lv.on {
				restAt = time.Now() // a running recording does not fade
			}
			if want := restWant(restAt); rest != want {
				animate()
			}
		case name := <-dwelled:
			if dwell[name] != nil && time.Now().UnixMilli()-p.islandShut.Load() > 1500 {
				delete(dwell, name)
				p.openIsland(name)
			}
		case <-tick:
			moving := false
			if style == StyleMascot && actor.update(time.Now()) {
				moving = true
				repaint()
			}
			if want := restWant(restAt); rest != want {
				if rest < want {
					rest = min(want, rest+step(restTime))
				} else {
					rest = max(want, rest-step(restTime/4))
				}
				moving = true
				repaint()
			}
			for name, to := range hoverTo {
				at := hover[name]
				if at == to {
					continue
				}
				if at < to {
					at = min(to, at+step(hoverTime))
				} else {
					at = max(to, at-step(hoverTime))
				}
				hover[name] = at
				moving = moving || at != to
				paint(name)
			}
			if !moving {
				ticker.Stop()
				ticker, tick = nil, nil
				if style == StyleMascot {
					repaint() // the gesture's last frame: back at rest
					sleep()
				}
			}
		case err := <-ended:
			close(stop)
			return err
		case <-p.settings:
			if !p.idleWanted() {
				close(stop)
				return <-ended
			}
			if now := p.dark(); now != dark || pal() != shown || fcs() != shownFont || wornNow() != shownWorn {
				dark, shown, shownFont, shownWorn = now, pal(), fcs(), wornNow()
				repaint()
			}
			if now := StyleOf(p.d.Config().UI.PillStyle); now != style {
				style = now
				x, y, iw, ih := inputFor()
				for _, w := range windows {
					w.setInput([4]int{x, y, iw, ih})
				}
				repaint()
				wake = nil
				sleep()
			}
		}
	}
}

// restWant is how far the resting pills should have faded.
func restWant(since time.Time) float64 {
	if time.Since(since) > restAfter {
		return 1
	}
	return 0
}

// restPicture is a resting pill's picture: the long recording's chip while
// one runs, else the pill or the character.
func (p *pill) restPicture(dark bool, presence, hover float64, style Style, actor *restActor, connector string, lv longView, clock *image.Alpha) []byte {
	if lv.on {
		buf := make([]byte, surfaceW*scale*surfaceH*scale*4)
		DrawLongChip(buf, surfaceW*scale, surfaceH*scale, dark, presence, hover, lv.paused, clock)
		return buf
	}
	return idlePicture(dark, presence, hover, style, actor, p.asideFor(connector))
}

func idlePicture(dark bool, presence, hover float64, style Style, actor *restActor, aside bool) []byte {
	buf := make([]byte, surfaceW*scale*surfaceH*scale*4)
	if style == StyleMascot {
		if aside {
			return buf
		}
		var f Frame
		actor.frame(time.Now(), &f)
		DrawIdleMascot(buf, surfaceW*scale, surfaceH*scale, dark, presence, hover, f)
		return buf
	}
	DrawIdle(buf, surfaceW*scale, surfaceH*scale, dark, presence, hover)
	return buf
}

// openIsland opens the island over one screen's pill, when nothing is being
// dictated and no island is open yet.
func (p *pill) openIsland(output uint32) {
	if p.d.Status().State != daemon.StateIdle {
		return
	}
	select {
	case p.island <- output:
	default:
	}
}

type idlePointer struct {
	name uint32
	e    pointerEvent
}

// clickIdle: a click on a resting pill starts a hands-free dictation on that
// screen, and stops one that is running.
func (p *pill) clickIdle(output uint32) {
	go func() {
		var err error
		if p.d.Status().State == daemon.StateRecording {
			err = p.d.Stop()
		} else {
			p.clickedOn.Store(output)
			err = p.d.Start()
			if err != nil {
				p.clickedOn.Store(0)
			}
		}
		if err != nil {
			p.log.Debug("resting pill click rejected", "err", err)
		}
	}()
}

func (p *pill) idleWanted() bool {
	ui := p.d.Config().UI
	// A pinned island stands in for the resting pills. A long recording
	// shows on them even when they are off: it must not be forgotten.
	return ui.PillEnabled() && ui.IslandPin == nil && (ui.PillIdleEnabled() || p.longNow().on)
}
