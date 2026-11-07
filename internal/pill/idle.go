package pill

import (
	"errors"
	"time"
)

// retryTime is the wait before reconnecting after the compositor went away.
const retryTime = 2 * time.Second

// How far a resting pill stands out, on the screen in use and on the others.
// Where the desktop does not say which that is, every pill stays at 1.
const (
	presenceActive   = 1.45
	presenceInactive = 0.45
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
	var last []uint32
	dark := p.dark()
	active := "" // the screen in use, "" as long as the desktop has not said
	presence := func(w window) float64 {
		switch active {
		case "":
			return 1
		case w.connector():
			return presenceActive
		}
		return presenceInactive
	}
	repaint := func() {
		for _, w := range windows {
			if err := w.present(idlePicture(dark, presence(w))); err != nil {
				p.log.Debug("resting pill not repainted", "err", err)
			}
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
			w, err := openIdleWindow(name)
			if err != nil {
				p.log.Debug("no resting pill on a screen", "err", err)
				continue
			}
			windows[name] = w
			if err := w.present(idlePicture(dark, presence(w))); err != nil {
				p.log.Debug("resting pill not painted", "err", err)
			}
			go func() {
				<-w.gone()
				lost <- name
			}()
		}
		for name, w := range windows {
			if !keep[name] {
				delete(windows, name)
				w.close()
			}
		}
	}
	for {
		select {
		case last = <-outputs:
			sync()
		case active = <-focused:
			repaint()
		case name := <-lost:
			delete(windows, name)
		case err := <-ended:
			close(stop)
			return err
		case <-p.settings:
			if !p.idleWanted() {
				close(stop)
				return <-ended
			}
			if now := p.dark(); now != dark {
				dark = now
				repaint()
			}
		}
	}
}

func idlePicture(dark bool, presence float64) []byte {
	buf := make([]byte, surfaceW*scale*surfaceH*scale*4)
	DrawIdle(buf, surfaceW*scale, surfaceH*scale, dark, presence)
	return buf
}

func (p *pill) idleWanted() bool {
	ui := p.d.Config().UI
	return ui.PillEnabled() && ui.PillIdle
}
