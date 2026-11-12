//go:build linux && webview

package main

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"syscall"
	"time"

	webview "github.com/webview/webview_go"

	"vito/internal/config"
	"vito/internal/selfexe"
)

// vito window on Linux: WebKitGTK through webview. Built only with the
// `webview` tag, so the release builds keep their libc-only binary. A first
// trial: no single-window hand-over yet.
func openWindow(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	port := cfg.Server.Port
	target := fmt.Sprintf("http://127.0.0.1:%d/", port)
	if len(args) > 0 {
		if target, err = ownPage(args[0], port); err != nil {
			return err
		}
	}
	if !daemonRunning(port) {
		exe, err := selfexe.Path()
		if err != nil {
			return err
		}
		cmd := exec.Command(exe, "serve")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			return err
		}
		for i := 0; i < 150 && !daemonRunning(port); i++ {
			time.Sleep(100 * time.Millisecond)
		}
	}
	w := webview.New(os.Getenv("VITO_DEBUG") != "")
	if w == nil {
		return fmt.Errorf("could not start WebKitGTK")
	}
	defer w.Destroy()
	w.SetTitle("Vito")
	w.SetSize(1280, 860, webview.HintNone)
	w.SetSize(760, 560, webview.HintMin)
	_ = w.Bind("vitoOpenExternal", func(raw string) error {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "mailto") {
			return fmt.Errorf("not an external link")
		}
		return exec.Command("xdg-open", u.String()).Start()
	})
	w.Init(externalLinksJS)
	w.Navigate(target)
	w.Run()
	return nil
}

func launchWindow(u string) error {
	exe, err := selfexe.Path()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "window", u)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Start()
}
