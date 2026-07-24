// vito — personal voice dictation daemon and its control CLI.
//
//	vito serve    run the daemon (bind this to a systemd user service)
//	vito toggle   start/stop a dictation (bind this to a hotkey)
//	vito start | stop | cancel | status
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"vito/internal/audio"
	"vito/internal/autostart"
	"vito/internal/config"
	"vito/internal/daemon"
	"vito/internal/history"
	"vito/internal/hotkey"
	"vito/internal/server"
	"vito/internal/tray"
)

// version is stamped at build time by packaging/build-installer.ps1 with
//
//	-ldflags "-X main.version=2026.7.1"
//
// The scheme is calendar-based: year.month, plus a counter for further releases
// in the same month (2026.7, then 2026.7.1, 2026.7.2). You can always see at a
// glance how old a build is. "dev" means someone built it by hand.
var version = "dev"

func main() {
	cmd := "help"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve()
	case "toggle", "start", "stop", "cancel":
		err = request(http.MethodPost, "/api/"+cmd)
	case "pwa":
		err = installPWA()
	case "autostart":
		// vito autostart on|off — write the login-startup entry through the same
		// code the settings toggle uses, so the installer and the app can never
		// disagree on its exact form. No daemon needed; it's a direct registry
		// (or .desktop) write.
		if len(os.Args) < 3 || (os.Args[2] != "on" && os.Args[2] != "off") {
			err = fmt.Errorf("usage: vito autostart on|off")
			break
		}
		err = autostart.Set(os.Args[2] == "on")
	case "quit":
		// Shuts the daemon down. The installer calls this before replacing the
		// binary: a graceful exit restores ducked media and closes the database,
		// which killing the process would not.
		err = request(http.MethodPost, "/api/quit")
	case "status":
		err = request(http.MethodGet, "/api/status")
	case "version", "--version":
		fmt.Println("vito", version)
	case "help", "--help", "-h":
		usage()
	default:
		usage()
		err = fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "vito:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`Vito — voice dictation

Usage:
  vito serve     run the daemon
  vito toggle    start or stop a dictation (bind to hotkey)
  vito start     start recording
  vito stop      stop recording and inject the text
  vito cancel    discard the current recording (bind to second hotkey)
  vito quit      shut the daemon down
  vito pwa       install the web interface as an app (Windows)
  vito status    show daemon state
  vito version

Environment:
  VITO_CONFIG    config file path override
  VITO_DEBUG=1   verbose logging (includes transcript pipeline)
`)
}

func serve() error {
	// Detach from (and close) a console Windows opened just for us — before any
	// logging, so a shortcut/autostart launch doesn't flash a window at all.
	// When run from a terminal the console is shared and left untouched, so the
	// logs below still appear.
	hideOwnConsole()

	level := slog.LevelInfo
	if os.Getenv("VITO_DEBUG") != "" {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)

	firstRun := !config.Exists() // capture before Load, which creates the file
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfgPath, _ := config.Path()
	log.Info("starting", "version", version, "config", cfgPath)

	// Single-instance guard: if a daemon already answers on the port, don't
	// start a second one. This makes the "vito://start" relaunch (and the tray/
	// autostart) idempotent — firing it while Vito is up is a harmless no-op.
	if daemonRunning(cfg.Server.Port) {
		log.Info("vito daemon already running; not starting a second instance", "port", cfg.Server.Port)
		return nil
	}
	registerLaunchProtocol() // so the web UI can relaunch the daemon when it's down
	// Keep the login entry pointing at this executable. An update that installs
	// Vito somewhere else would otherwise leave it aimed at the old location,
	// and autostart would quietly stop working (or start the previous version).
	if autostart.Supported() {
		if on, err := autostart.Enabled(); err == nil && on {
			if err := autostart.Set(true); err != nil {
				log.Warn("could not refresh the autostart entry", "err", err)
			}
		}
	}
	if cfg.STT.APIKey == "" {
		log.Warn("stt.api_key is empty — set your AssemblyAI key in the config file", "config", cfgPath)
	}

	audioCtx, err := audio.NewContext()
	if err != nil {
		return err
	}
	defer audioCtx.Close()

	if devs, err := audioCtx.CaptureDevices(); err == nil {
		for _, d := range devs {
			log.Debug("capture device", "name", d.Name, "default", d.IsDefault)
		}
	}

	hist, err := history.NewStore(cfg.History.MaxEntries, cfg.History.RetentionDays)
	if err != nil {
		return err
	}
	defer hist.Close()

	d := daemon.New(cfg, log, audioCtx, hist)
	hk := hotkey.New(d, log)
	server.Version = version
	srv := server.New(d, log, audioCtx, hist, hk, cfg.Server.Token, cfg.Server.Port)
	notifyUserSignals(d, log)
	hk.Start(cfg.HotkeyWindows, cfg.HotkeyCancelWindows)

	if cfg.Demo {
		log.Info("demo mode: serving sample data; your own history and dictionary are hidden, not changed")
	}
	// Always running: it idles while demo mode is off, so switching it on from
	// the banner starts the sample transcript immediately, without a restart.
	demoCtx, stopDemo := context.WithCancel(context.Background())
	defer stopDemo()
	go d.RunDemo(demoCtx)

	// Graceful shutdown: cancel any in-flight recording so the spool closes.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Info("shutting down")
		d.Shutdown() // restore any ducked/paused media before exiting
		os.Exit(0)
	}()

	// The server runs in the background so the tray can own the main goroutine
	// (systray.Run must). If the tray is disabled or no tray host is present,
	// we simply block on the server as before.
	srvErr := make(chan error, 1)
	go func() { srvErr <- srv.ListenAndServe() }()

	if firstRun {
		firstRunSetup(cfg, log)
	}

	if cfg.Tray.Enabled {
		url := fmt.Sprintf("http://127.0.0.1:%d", cfg.Server.Port)
		tray.Run(d, url, log, func() {
			log.Info("shutting down (tray quit)")
			d.Shutdown() // restore any ducked/paused media before exiting
			os.Exit(0)
		})
		// systray.Run returned without a Quit: no tray host. Stay alive and
		// keep serving headlessly rather than exiting the daemon.
		log.Debug("tray unavailable; continuing headless")
	}
	return <-srvErr
}

// firstRunSetup runs once, the very first time the daemon starts (no config
// existed yet): it enables launch-at-login by default — so Vito is always ready
// after that — and opens the settings page in the browser once the server is up,
// so a fresh install lands the user on the interface instead of a silent tray.
func firstRunSetup(cfg *config.Config, log *slog.Logger) {
	if autostart.Supported() {
		if err := autostart.Set(true); err != nil {
			log.Warn("could not enable autostart on first run", "err", err)
		}
	}
	url := fmt.Sprintf("http://127.0.0.1:%d", cfg.Server.Port)
	go func() {
		for i := 0; i < 60; i++ { // wait up to ~6s for the server to accept connections
			if daemonRunning(cfg.Server.Port) {
				if err := tray.OpenURL(url); err != nil {
					log.Warn("could not open the browser on first run", "err", err)
				}
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()
}

// daemonRunning reports whether something is already listening on the daemon's
// control port — a cheap cross-platform single-instance check.
func daemonRunning(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 400*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// request sends a control command to the running daemon and prints the reply.
func request(method, path string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	url := fmt.Sprintf("http://127.0.0.1:%d%s", cfg.Server.Port, path)
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Server.Token)

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		var opErr *net.OpError
		if errors.As(err, &opErr) {
			return fmt.Errorf("daemon is not running (start it with 'vito serve')")
		}
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))

	var pretty map[string]any
	if json.Unmarshal(body, &pretty) == nil {
		if errMsg, ok := pretty["error"].(string); ok && errMsg != "" {
			return errors.New(errMsg)
		}
		if state, ok := pretty["state"].(string); ok {
			fmt.Println(state)
			return nil
		}
	}
	fmt.Println(strings.TrimSpace(string(body)))
	return nil
}
