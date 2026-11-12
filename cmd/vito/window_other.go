//go:build !windows && !(linux && webview)

package main

import "fmt"

// openWindow is Windows-only for now (WebView2); elsewhere the page opens in
// the installed web app or the browser.
func openWindow([]string) error {
	return fmt.Errorf("vito window is only available on Windows for now")
}

func launchWindow(string) error { return fmt.Errorf("no window on this system") }
