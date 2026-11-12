// Package webapp opens Vito's page: in the installed web app when there is
// one, in the browser otherwise.
package webapp

import (
	"errors"
	"os/exec"
	"runtime"
)

// Native, when set, opens the page in Vito's own window instead; it returns
// ErrNoNative when that is switched off, and Open carries on as before.
var Native func(url string) error

// NativeOn, when set, says whether Open will use Native: Vito's own window
// shows at once, so callers can skip their "opening…" feedback.
var NativeOn func() bool

// ErrNoNative says Native is not in use.
var ErrNoNative = errors.New("no native window")

// Open opens url (Vito's own page, http://127.0.0.1:<port>) in the web app
// the user installed from it, and in an ordinary browser tab when there is
// none — where the browser and Vito itself can offer to install it.
func Open(url string) error {
	if Native != nil {
		if err := Native(url); err == nil {
			return nil
		}
	}
	if launch := installed(url); launch != nil {
		if err := launch(); err == nil {
			return nil
		}
	}
	return Browser(url)
}

// Browser opens url in the user's default browser as an ordinary tab.
//
// It deliberately does NOT open a Chromium --app window. An --app window shows
// no tab UI and — because it already behaves like a standalone app — fires no
// PWA install prompt, so neither the browser's install banner nor Vito's own
// in-app "install as app" notification can appear there.
func Browser(url string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}
