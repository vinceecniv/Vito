package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"vito/internal/config"
)

// fakeGitHub serves a latest release "2026.11" with one asset and its
// checksum; sum overrides the published checksum.
func fakeGitHub(t *testing.T, body, sum string) *httptest.Server {
	t.Helper()
	if sum == "" {
		h := sha256.Sum256([]byte(body))
		sum = hex.EncodeToString(h[:])
	}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name": "v2026.11",
				"assets": []map[string]any{
					{"name": "Vito-Setup-2026.11.exe", "browser_download_url": srv.URL + "/a", "size": len(body)},
					{"name": "Vito-Setup-2026.11.exe.sha256", "browser_download_url": srv.URL + "/a.sha256", "size": 90},
					// Another platform's checksum, listed last: it must not be
					// mistaken for the installer's.
					{"name": "Vito-2026.11-x86_64.AppImage.sha256", "browser_download_url": srv.URL + "/other.sha256", "size": 90},
				},
			})
		case "/a":
			_, _ = io.WriteString(w, body)
		case "/a.sha256":
			fmt.Fprintf(w, "%s  Vito-Setup-2026.11.exe\n", sum)
		case "/other.sha256":
			fmt.Fprintf(w, "%s  other\n", strings.Repeat("0", 64))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func checker(srv *httptest.Server, current string) *Checker {
	c := NewChecker(current)
	c.API = srv.URL + "/latest"
	return c
}

func TestDownloadAssetVerifies(t *testing.T) {
	ctx := context.Background()
	c := checker(fakeGitHub(t, "installer bytes", ""), "2026.10")
	rel, err := c.Check(ctx, true)
	if err != nil || !rel.Available {
		t.Fatalf("check: %v %+v", err, rel)
	}
	p, err := c.Download(ctx, rel, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "installer bytes" {
		t.Fatalf("got %q", b)
	}

	bad := checker(fakeGitHub(t, "installer bytes", strings.Repeat("a", 64)), "2026.10")
	rel, _ = bad.Check(ctx, true)
	if _, err := bad.Download(ctx, rel, nil); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected a checksum mismatch, got %v", err)
	}
	if _, err := bad.DownloadAsset(ctx, rel, "missing.dmg", nil); err == nil {
		t.Fatal("expected an error for a missing asset")
	}
}

type fakeInstaller struct {
	installed []string
	fail      error
}

func (f *fakeInstaller) asset(rel *Release) string { return rel.Installer }
func (f *fakeInstaller) install(path string) error {
	f.installed = append(f.installed, path)
	return f.fail
}

func TestAutoWaitsForQuiet(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	idle := false
	clock := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	prepared := 0
	a := NewAuto(checker(fakeGitHub(t, "x", ""), "2026.10"), slog.New(slog.NewTextHandler(io.Discard, nil)),
		func() bool { return true }, func() bool { return idle }, func() { prepared++ })
	inst := &fakeInstaller{}
	a.inst, a.now = inst, func() time.Time { return clock }

	a.step(ctx) // downloads
	if st := a.Status(); st.State != AutoReady || st.Version != "2026.11" {
		t.Fatalf("after download: %+v", st)
	}
	a.step(ctx) // busy: nothing
	idle = true
	a.step(ctx) // idle, but only just
	clock = clock.Add(2 * time.Minute)
	a.step(ctx)
	if len(inst.installed) != 0 {
		t.Fatal("installed before Vito had been quiet long enough")
	}
	idle = false
	clock = clock.Add(2 * time.Minute)
	a.step(ctx) // a dictation resets the quiet period
	idle = true
	a.step(ctx)
	clock = clock.Add(2 * time.Minute)
	a.step(ctx)
	if len(inst.installed) != 0 {
		t.Fatal("a dictation in between must restart the quiet period")
	}
	clock = clock.Add(2 * time.Minute)
	a.step(ctx)
	if len(inst.installed) != 1 || prepared != 1 {
		t.Fatalf("installed %d times, prepared %d", len(inst.installed), prepared)
	}
	if got := TakeUpdatedFrom("2026.11"); got != "2026.10" {
		t.Fatalf("updated-from marker: %q", got)
	}
	if got := TakeUpdatedFrom("2026.11"); got != "" {
		t.Fatal("the marker is reported once")
	}
}

func TestAutoFailureBacksOff(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	clock := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	a := NewAuto(checker(fakeGitHub(t, "x", ""), "2026.10"), slog.New(slog.NewTextHandler(io.Discard, nil)),
		func() bool { return true }, func() bool { return true }, nil)
	inst := &fakeInstaller{fail: errors.New("disk full")}
	a.inst, a.now, a.QuietFor = inst, func() time.Time { return clock }, 0

	a.step(ctx) // download
	a.step(ctx) // install fails
	if st := a.Status(); st.State != AutoError || len(inst.installed) != 1 {
		t.Fatalf("%+v, %d installs", st, len(inst.installed))
	}
	if TakeUpdatedFrom("2026.11") != "" {
		t.Fatal("a failed install must not leave the marker")
	}
	a.step(ctx)
	a.step(ctx)
	if len(inst.installed) != 1 {
		t.Fatal("retried during the back-off")
	}
}

func TestAutoOffForDevAndSetting(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	on := func() bool { return true }
	dev := NewAuto(NewChecker("dev"), log, on, on, nil)
	dev.inst = &fakeInstaller{}
	if dev.Status().State != AutoOff {
		t.Fatal("a dev build never updates itself")
	}
	off := NewAuto(NewChecker("2026.10"), log, func() bool { return false }, on, nil)
	off.inst = &fakeInstaller{}
	if off.Status().State != AutoOff {
		t.Fatal("setting off")
	}
}

func TestAutoEnabledDefault(t *testing.T) {
	f, tr := false, true
	cases := []struct {
		u    config.Update
		want bool
	}{
		{config.Update{}, true},
		{config.Update{Auto: &f}, false},
		{config.Update{Check: &f}, false},
		{config.Update{Check: &f, Auto: &tr}, false},
	}
	for _, c := range cases {
		if got := c.u.AutoEnabled(); got != c.want {
			t.Errorf("%+v: got %v", c.u, got)
		}
	}
}
