package pill

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
}

var focusSources = []focusSource{niriFocus}

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
