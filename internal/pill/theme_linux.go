//go:build linux

package pill

import (
	"log/slog"

	"github.com/godbus/dbus/v5"

	"vito/internal/xdgportal"
)

const (
	ifaceSettings  = "org.freedesktop.portal.Settings"
	appearance     = "org.freedesktop.appearance"
	colorSchemeKey = "color-scheme"
	schemeDark     = 1 // 0 is "no preference" and 2 light; both draw light
)

// watchColorScheme reports the desktop's light or dark mode, now and on every
// change. The portal is the one place GNOME, KDE and shells like DMS all
// publish it, and it reaches a Flatpak. Without it the pill stays light, which
// is what "no preference" means too.
func watchColorScheme(log *slog.Logger, changed func(dark bool)) {
	conn, err := xdgportal.Bus()
	if err != nil {
		log.Debug("no colour scheme for the pill", "err", err)
		return
	}
	var v dbus.Variant
	obj := conn.Object(xdgportal.Dest, dbus.ObjectPath(xdgportal.Path))
	if err := obj.Call(ifaceSettings+".ReadOne", 0, appearance, colorSchemeKey).Store(&v); err == nil {
		changed(isDark(v))
	}
	match := []dbus.MatchOption{dbus.WithMatchInterface(ifaceSettings), dbus.WithMatchMember("SettingChanged")}
	if err := conn.AddMatchSignal(match...); err != nil {
		log.Debug("cannot follow the colour scheme", "err", err)
		return
	}
	ch := make(chan *dbus.Signal, 8)
	conn.Signal(ch)
	go func() {
		for sig := range ch {
			if sig.Name != ifaceSettings+".SettingChanged" || len(sig.Body) < 3 {
				continue
			}
			ns, _ := sig.Body[0].(string)
			key, _ := sig.Body[1].(string)
			if v, ok := sig.Body[2].(dbus.Variant); ok && ns == appearance && key == colorSchemeKey {
				changed(isDark(v))
			}
		}
	}()
}

func isDark(v dbus.Variant) bool {
	if inner, ok := v.Value().(dbus.Variant); ok { // the older Read nests it
		v = inner
	}
	n, ok := v.Value().(uint32)
	return ok && n == schemeDark
}
