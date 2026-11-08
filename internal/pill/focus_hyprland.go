package pill

import (
	"bufio"
	"encoding/json"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// hyprFocus follows Hyprland's IPC: requests on .socket.sock, events on
// .socket2.sock, both under $XDG_RUNTIME_DIR/hypr/<instance>/.
var hyprFocus = focusSource{
	available: func() bool { return os.Getenv("HYPRLAND_INSTANCE_SIGNATURE") != "" },
	watch:     watchHypr,
	window:    hyprWindow,
	screen:    hyprScreen,
}

func init() { focusSources = append(focusSources, hyprFocus) }

func hyprSocket(name string) string {
	return filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "hypr", os.Getenv("HYPRLAND_INSTANCE_SIGNATURE"), name)
}

// hyprAsk sends one request ("j/monitors") and returns the whole reply.
func hyprAsk(req string) ([]byte, error) {
	conn, err := net.DialTimeout("unix", hyprSocket(".socket.sock"), time.Second)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(time.Second))
	if _, err := conn.Write([]byte(req)); err != nil {
		return nil, err
	}
	return io.ReadAll(conn)
}

type hyprMonitor struct {
	Name          string
	Width, Height int
	Scale         float64
	Transform     int
	Focused       bool
}

func hyprMonitors() []hyprMonitor {
	b, err := hyprAsk("j/monitors")
	if err != nil {
		return nil
	}
	var list []hyprMonitor
	json.Unmarshal(b, &list)
	return list
}

func hyprScreen(connector string) (w, h int, ok bool) {
	list := hyprMonitors()
	for _, m := range list {
		// A window that was not told its screen ("") is on the focused one,
		// where the compositor opened it.
		if connector == "" && m.Focused {
			connector = m.Name
		}
		if m.Name != connector || m.Scale <= 0 {
			continue
		}
		w, h = int(math.Round(float64(m.Width)/m.Scale)), int(math.Round(float64(m.Height)/m.Scale))
		if m.Transform%2 == 1 { // turned a quarter
			w, h = h, w
		}
		return w, h, true
	}
	return 0, 0, false
}

// hyprWindow: Hyprland answers {} when no window has the keyboard.
func hyprWindow() (focused, known bool) {
	b, err := hyprAsk("j/activewindow")
	if err != nil {
		return false, false
	}
	var w struct{ Address string }
	if json.Unmarshal(b, &w) != nil {
		return false, false
	}
	return w.Address != "", true
}

// watchHypr reports the focused screen now and on every focusedmon event.
func watchHypr(stop <-chan struct{}, changed func(output string)) {
	for _, m := range hyprMonitors() {
		if m.Focused {
			changed(m.Name)
		}
	}
	conn, err := net.Dial("unix", hyprSocket(".socket2.sock"))
	if err != nil {
		return
	}
	go func() {
		<-stop
		conn.Close()
	}()
	r := bufio.NewScanner(conn)
	for r.Scan() {
		// focusedmon>>DP-1,2
		if name, ok := strings.CutPrefix(r.Text(), "focusedmon>>"); ok {
			name, _, _ = strings.Cut(name, ",")
			changed(name)
		}
	}
}
