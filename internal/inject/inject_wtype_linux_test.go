//go:build linux

package inject

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"vito/internal/config"
	"vito/internal/xdgportal"
)

// The direct Wayland connection is found only inside a Flatpak, only when the
// link is a socket, and a remade link changes the probe key so it is probed again.
func TestHostWaylandLink(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("FLATPAK_ID", "")
	link := filepath.Join(dir, "app", "io.github.vinceecniv.vito", "wayland")
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	listen := func(p string) {
		l, err := net.Listen("unix", p)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { l.Close() })
	}
	listen(filepath.Join(dir, "wayland-1"))
	if err := os.Link(filepath.Join(dir, "wayland-1"), link); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat("/.flatpak-info"); err != nil && hostWayland() != "" {
		t.Fatal("link used outside a Flatpak")
	}
	t.Setenv("FLATPAK_ID", "io.github.vinceecniv.vito")
	if got := hostWayland(); got != link {
		t.Fatalf("hostWayland() = %q, want %q", got, link)
	}
	key := wtypeProbeKey()

	// A new session's socket: same name, new inode.
	listen(filepath.Join(dir, "wayland-2"))
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(dir, "wayland-2"), link); err != nil {
		t.Fatal(err)
	}
	if wtypeProbeKey() == key {
		t.Fatal("remade link kept the old probe key")
	}

	// Not a socket: ignored.
	os.Remove(link)
	if err := os.WriteFile(link, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if hostWayland() != "" || HasHostWaylandLink() {
		t.Fatal("a plain file was taken for the link")
	}
}

// TestFlatpakDirectWayland checks the whole route from inside a real sandbox:
// with the link made on the host, a Flatpak on a portal-less compositor resolves
// to wtype instead of clipboard-only. It types nothing — the probe binds the
// virtual keyboard and sends an empty string. Run it in the installed app's
// sandbox, after the link command from docs/flatpak.md:
//
//	go test -c -o /tmp/inject.test ./internal/inject/
//	flatpak run --filesystem=/tmp/inject.test:ro \
//	  --env=VITO_FLATPAK_LINK_TEST=1 --command=/tmp/inject.test \
//	  io.github.vinceecniv.vito -test.run TestFlatpakDirectWayland -test.v
func TestFlatpakDirectWayland(t *testing.T) {
	if os.Getenv("VITO_FLATPAK_LINK_TEST") == "" {
		t.Skip("manual test: set VITO_FLATPAK_LINK_TEST=1 and run inside the Flatpak")
	}
	if !xdgportal.InFlatpak() {
		t.Fatal("not running inside a Flatpak")
	}
	if !HasHostWaylandLink() {
		t.Fatal("no link at $XDG_RUNTIME_DIR/" + HostWaylandLink())
	}
	if !wtypeUsable() {
		t.Fatal("wtype refused over the direct connection")
	}
	cfg := config.Injection{Backend: backendAuto}
	if got := ActiveBackend(cfg); got != backendWayland {
		t.Fatalf("ActiveBackend = %q, want %q", got, backendWayland)
	}
	t.Logf("delivering through %s via %s", ActiveBackend(cfg), hostWayland())
}
