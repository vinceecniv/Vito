//go:build linux

package cloudsync

import (
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"

	"vito/internal/xdgportal"
)

// portalPickFolder asks the FileChooser portal for a directory: the
// desktop's own dialog, which also works inside a Flatpak and needs no
// helper programs. errNoPortal when the portal is missing or can't answer.
func portalPickFolder(title, start string) (string, error) {
	conn, err := xdgportal.Bus()
	if err != nil {
		return "", errNoPortal
	}
	if _, ok := xdgportal.Version(conn, "org.freedesktop.portal.FileChooser"); !ok {
		return "", errNoPortal
	}
	obj := conn.Object("org.freedesktop.portal.Desktop", "/org/freedesktop/portal/desktop")
	res, err := xdgportal.Request(conn, 10*time.Minute, func(opts map[string]dbus.Variant) *dbus.Call {
		opts["directory"] = dbus.MakeVariant(true)
		opts["modal"] = dbus.MakeVariant(true)
		if start != "" {
			opts["current_folder"] = dbus.MakeVariant(append([]byte(start), 0))
		}
		return obj.Call("org.freedesktop.portal.FileChooser.OpenFile", 0, "", title, opts)
	})
	if errors.Is(err, xdgportal.ErrCancelled) {
		return "", nil
	}
	if err != nil {
		// A frontend without a working backend answers with an error: fall
		// back to zenity or kdialog rather than give up.
		return "", errNoPortal
	}
	uris, _ := res["uris"].Value().([]string)
	if len(uris) == 0 {
		return "", nil
	}
	u, err := url.Parse(uris[0])
	if err != nil || u.Scheme != "file" {
		return "", errors.New("the folder picker returned something that isn't a folder on this computer")
	}
	return strings.TrimRight(u.Path, "/"), nil
}
