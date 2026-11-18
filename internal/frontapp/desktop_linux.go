//go:build linux

package frontapp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

func desktopIs(name string) bool {
	return strings.Contains(strings.ToLower(os.Getenv("XDG_CURRENT_DESKTOP")), name)
}

// gnomeApp asks Vito's own Shell extension (internal/pill/gnome, talk.vito.Pill,
// from version 6): GNOME tells an ordinary Wayland client nothing.
func gnomeApp() (id, name string) {
	conn, err := dbus.SessionBus()
	if err != nil {
		return "", ""
	}
	call := conn.Object("talk.vito.Pill", "/talk/vito/Pill").Call("talk.vito.Pill.App", 0)
	if call.Err != nil || call.Store(&id, &name) != nil {
		return "", ""
	}
	return id, name
}

// On KDE KWin answers only its own scripts, so Vito loads one: it reports the
// active window's desktop file and class to Vito over D-Bus, at once and on
// every change (as kdotool does). Vito keeps the last report.
const (
	kdeName   = "talk.vito.Front"
	kdePath   = "/talk/vito/Front"
	kdePlugin = "vito-front"
)

const kdeScript = `
const report = (w) => {
  if (!w) return;
  callDBus("talk.vito.Front", "/talk/vito/Front", "talk.vito.Front", "Active",
    String(w.desktopFileName || ""), String(w.resourceClass || ""));
};
report(workspace.activeWindow || workspace.activeClient);
(workspace.windowActivated || workspace.clientActivated).connect(report);
`

type kdeFront struct {
	mu        sync.Mutex
	desk, cls string
	got       chan struct{}
}

// Active is called by the KWin script.
func (k *kdeFront) Active(desk, cls string) *dbus.Error {
	k.mu.Lock()
	k.desk, k.cls = desk, cls
	k.mu.Unlock()
	select {
	case k.got <- struct{}{}:
	default:
	}
	return nil
}

var (
	kdeOnce  sync.Once
	kdeState *kdeFront
)

func kdeApp() (id string) {
	kdeOnce.Do(func() { kdeState = kdeStart() })
	k := kdeState
	if k == nil {
		return ""
	}
	k.mu.Lock()
	known := k.desk != "" || k.cls != ""
	k.mu.Unlock()
	if !known { // the script's first report is on its way
		select {
		case <-k.got:
		case <-time.After(400 * time.Millisecond):
		}
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.desk != "" {
		return k.desk
	}
	return k.cls
}

func kdeStart() *kdeFront {
	conn, err := dbus.SessionBus()
	if err != nil {
		return nil
	}
	k := &kdeFront{got: make(chan struct{}, 1)}
	if err := conn.Export(k, kdePath, kdeName); err != nil {
		return nil
	}
	if r, err := conn.RequestName(kdeName, dbus.NameFlagDoNotQueue); err != nil || r != dbus.RequestNameReplyPrimaryOwner {
		return nil // another Vito has it
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return nil
	}
	path := filepath.Join(dir, "vito", "kwin-front.js")
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(kdeScript), 0o644); err != nil {
		return nil
	}
	kwin := conn.Object("org.kde.KWin", "/Scripting")
	kwin.Call("org.kde.kwin.Scripting.unloadScript", 0, kdePlugin) // one from an earlier run
	var id int32
	if err := kwin.Call("org.kde.kwin.Scripting.loadScript", 0, path, kdePlugin).Store(&id); err != nil || id < 0 {
		return nil
	}
	// KWin 6 puts the script at /Scripting/Script<id>, KWin 5 at /<id>.
	for _, p := range []string{fmt.Sprintf("/Scripting/Script%d", id), fmt.Sprintf("/%d", id)} {
		if conn.Object("org.kde.KWin", dbus.ObjectPath(p)).Call("org.kde.kwin.Script.run", 0).Err == nil {
			return k
		}
	}
	return nil
}
