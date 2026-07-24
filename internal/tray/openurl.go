package tray

import (
	"os/exec"
	"runtime"
)

// OpenURL opens url in the user's default browser as an ordinary tab.
//
// It deliberately does NOT open a Chromium --app window. An --app window shows
// no tab UI and — because it already behaves like a standalone app — fires no
// PWA install prompt, so neither the browser's install banner nor Vito's own
// in-app "install as app" notification can appear there. Opening a normal tab
// keeps the tray's "open settings" identical to the Start-menu shortcut (which
// shell-executes the same URL) and lets the user install the interface as a PWA
// when they choose to. Once installed, they launch that app from its own icon.
func OpenURL(url string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}
