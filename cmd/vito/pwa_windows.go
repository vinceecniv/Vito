//go:build windows

package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"vito/internal/config"
)

// installPWA asks a Chromium browser to install Vito's web interface as an app.
//
// This is worth doing beyond the nicer window: an installed PWA registers an
// AppUserModelID with Windows, and that is what Vito's desktop notifications
// attach themselves to. Without it, notifications fall back to a generic
// shell identity.
//
// It is best-effort by design. The switch is not contractual browser API, so a
// failure here is reported and shrugged off — the web UI still offers the usual
// install button, which is the supported route.
func installPWA() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/", cfg.Server.Port)

	// The browser has to be able to fetch the manifest, so the daemon must be up.
	if !waitForDaemon(cfg.Server.Port, 15*time.Second) {
		return fmt.Errorf("vito daemon is not answering on port %d — start it first (vito serve)", cfg.Server.Port)
	}

	browser, err := findChromium()
	if err != nil {
		return err
	}
	// --silent installs without opening a window or asking; if the browser is
	// already running it hands the request to the existing process.
	cmd := exec.Command(browser, "--install-app="+url, "--silent")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %w: %s", filepath.Base(browser), err, out)
	}
	fmt.Printf("asked %s to install the Vito web app (%s)\n", filepath.Base(browser), url)
	fmt.Println("if it doesn't appear, open the address above and use the install button in the browser's address bar")
	return nil
}

// findChromium prefers Edge — it ships with Windows and is what most people
// have — then falls back to Chrome.
func findChromium() (string, error) {
	var candidates []string
	for _, base := range []string{os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramFiles"), os.Getenv("LOCALAPPDATA")} {
		if base == "" {
			continue
		}
		candidates = append(candidates,
			filepath.Join(base, "Microsoft", "Edge", "Application", "msedge.exe"),
			filepath.Join(base, "Google", "Chrome", "Application", "chrome.exe"),
		)
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c, nil
		}
	}
	return "", fmt.Errorf("no Chromium-based browser found (Edge or Chrome) — install the web app from the browser instead")
}

func waitForDaemon(port int, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false
}
