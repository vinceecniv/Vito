//go:build windows

package main

import (
	"runtime"

	"golang.org/x/sys/windows"
)

// undoEdgeInit takes back what go-webview2's edge package does in its init()
// for every Vito process: it locks the main goroutine to the main thread and
// makes that thread a COM single-threaded apartment. Only `vito window` wants
// that; in the daemon an STA main thread without a message pump stalled the
// microphone (WASAPI) and the keyboard hook. Runs first thing in main, which
// is still on that thread.
func undoEdgeInit() {
	windows.CoUninitialize()
	runtime.UnlockOSThread()
}
