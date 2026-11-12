//go:build !windows && !(linux && webview)

package main

import (
	"fmt"
	"os/exec"
	"syscall"

	"vito/internal/selfexe"
)

// openWindow needs WebView2 (Windows) or a build with -tags webview (Linux);
// elsewhere the page opens in the installed web app or the browser.
func openWindow([]string) error {
	return fmt.Errorf("vito window is not available in this build")
}

func launchWindow(string) error { return fmt.Errorf("no window on this system") }

func windowSupported() bool { return false }

// startDaemon starts `vito serve` from this same binary, in a session of its
// own so it outlives the caller.
func startDaemon() error {
	exe, err := selfexe.Path()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "serve")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Start()
}
