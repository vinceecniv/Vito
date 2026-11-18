package frontapp

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// A program's icon, for the history and the dashboard. It is taken when the
// program is first seen in front (each system has its own source: the
// executable on Windows, the icon theme on Linux) and kept as a file under
// <UserCacheDir>/vito/app-icons, so a program that is gone still shows it.

var iconTried sync.Map // id → true: looked for in this run

func iconDir() string {
	d, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(d, "vito", "app-icons")
}

// iconFile makes an id safe as a file name.
func iconFile(id string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(id) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return strings.TrimLeft(b.String(), ".")
}

// Icon is the kept icon of a program and its content type; ok is false when
// there is none.
func Icon(id string) (path, contentType string, ok bool) {
	dir, name := iconDir(), iconFile(id)
	if dir == "" || name == "" {
		return "", "", false
	}
	for ext, ct := range map[string]string{".png": "image/png", ".svg": "image/svg+xml"} {
		p := filepath.Join(dir, name+ext)
		if st, err := os.Stat(p); err == nil && st.Size() > 0 {
			return p, ct, true
		}
	}
	return "", "", false
}

// keepIcon stores a program's icon once per run, off the caller's path; src
// is what the system's iconData understands.
func keepIcon(id, src string) {
	if id == "" || src == "" {
		return
	}
	if _, seen := iconTried.LoadOrStore(id, true); seen {
		return
	}
	// Kept less than a week ago: no need to draw it again (a program's icon
	// changes with an update, rarely).
	if p, _, ok := Icon(id); ok {
		if st, err := os.Stat(p); err == nil && time.Since(st.ModTime()) < 7*24*time.Hour {
			return
		}
	}
	go func() {
		data, ext := iconData(src)
		dir, name := iconDir(), iconFile(id)
		if len(data) == 0 || dir == "" || name == "" || os.MkdirAll(dir, 0o755) != nil {
			return
		}
		for _, e := range []string{".png", ".svg"} { // a newer icon may change its kind
			if e != ext {
				_ = os.Remove(filepath.Join(dir, name+e))
			}
		}
		tmp := filepath.Join(dir, name+ext+".tmp")
		if os.WriteFile(tmp, data, 0o644) == nil {
			_ = os.Rename(tmp, filepath.Join(dir, name+ext))
		}
	}()
}
