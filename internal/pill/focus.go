package pill

import "time"

// focusSource tells which screen is in use. No general way exists: a Wayland
// client cannot ask, so each desktop needs its own, added to focusSources. The
// answer must be the screen a dictation's pill will open on, and its name the
// one window.connector gives.
type focusSource struct {
	// available reports whether this desktop is the one running.
	available func() bool
	// watch reports the name of the screen in use, now and on every change,
	// until stop is closed or the desktop stops talking.
	watch func(stop <-chan struct{}, changed func(output string))
	// window asks whether a window has the keyboard now; known is false when
	// the desktop could not say. Optional.
	window func() (focused, known bool)
	// screen gives a screen's size in logical pixels. Optional.
	screen func(connector string) (w, h int, ok bool)
	// fromPress: during a drag the pointer's position is measured from where
	// the window was when the button went down, however it moved since.
	fromPress bool
}

var focusSources = []focusSource{niriFocus}

// windowFocused asks this desktop whether a window has the keyboard: when
// none does, a paste went nowhere.
func windowFocused() (focused, known bool) {
	for _, s := range focusSources {
		if s.available() && s.window != nil {
			return s.window()
		}
	}
	return false, false
}

// watchFocus runs the source for this desktop. With none, it reports nothing
// and every resting pill keeps the same presence.
func watchFocus(stop <-chan struct{}, changed func(output string)) {
	for _, s := range focusSources {
		if s.available() {
			s.watch(stop, changed)
			return
		}
	}
}

// focusedScreen names the screen in use now, "" when the desktop cannot say.
func focusedScreen() string {
	stop := make(chan struct{})
	defer close(stop)
	got := make(chan string, 1)
	go watchFocus(stop, func(output string) {
		select {
		case got <- output:
		default:
		}
	})
	select {
	case o := <-got:
		return o
	case <-time.After(time.Second):
		return ""
	}
}

// screenSize asks this desktop how big a screen is, in logical pixels; a
// Wayland client is not told. The pinned island keeps itself on it with this.
func screenSize(connector string) (w, h int, ok bool) {
	for _, s := range focusSources {
		if s.available() && s.screen != nil {
			return s.screen(connector)
		}
	}
	return 0, 0, false
}

// dragFromPress says whether this desktop measures a drag from the press
// (focusSource.fromPress): then the position alone follows the pointer
// exactly, and its own motion, scaled its own way, need not be asked for.
func dragFromPress() bool {
	for _, s := range focusSources {
		if s.available() {
			return s.fromPress
		}
	}
	return false
}
