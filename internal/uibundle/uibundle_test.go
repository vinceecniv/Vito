package uibundle

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// publish builds a site holding one bundle with the given files, signed with
// priv, and returns its handler.
// appFor is the release the published test bundles are for.
var appFor = "2026.10"

func publish(t *testing.T, priv ed25519.PrivateKey, version string, api, minAPI int, files map[string]string) http.Handler {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(w, body)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(buf.Bytes())
	man := Manifest{Version: version, API: api, MinAPI: minAPI, File: "vito-ui-" + version + ".zip",
		Size: int64(buf.Len()), SHA256: hex.EncodeToString(sum[:]), App: appFor}
	raw, _ := json.Marshal(man)
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, raw))
	mux := http.NewServeMux()
	mux.HandleFunc("/ui/ui.json", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(raw) })
	mux.HandleFunc("/ui/ui.json.sig", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, sig) })
	mux.HandleFunc("/ui/"+man.File, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(buf.Bytes()) })
	return mux
}

func testManager(t *testing.T, url string) *Manager {
	t.Helper()
	cache := t.TempDir()
	t.Setenv("VITO_UI_URL", url+"/ui/")
	t.Setenv("LocalAppData", cache) // os.UserCacheDir on Windows
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("HOME", cache)
	return New(slog.New(slog.NewTextHandler(io.Discard, nil)), "2026.10", true, func() bool { return true }, nil)
}

func withKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	old := PublicKey
	PublicKey = pub
	t.Cleanup(func() { PublicKey = old })
	return priv
}

func TestCheckInstallsAndSurvivesRestart(t *testing.T) {
	priv := withKey(t)
	srv := httptest.NewServer(publish(t, priv, "2026.10.08-1", API, 1, map[string]string{
		"index.html": "new page", "i18n/de.json": "{}",
	}))
	defer srv.Close()

	m := testManager(t, srv.URL)
	if m.Status().Source != "builtin" {
		t.Fatal("expected the built-in interface before any check")
	}
	if err := m.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b, _ := fs.ReadFile(m.FS(), "index.html"); string(b) != "new page" {
		t.Fatalf("serving %q", b)
	}
	if st := m.Status(); st.Source != "bundle" || st.Version != "2026.10.08-1" {
		t.Fatalf("status %+v", st)
	}

	// A restart picks the same bundle up again from the cache.
	m2 := New(m.log, "2026.10", true, func() bool { return true }, nil)
	if b, _ := fs.ReadFile(m2.FS(), "index.html"); string(b) != "new page" {
		t.Fatalf("after restart serving %q", b)
	}
	// ...but not when downloads are not to be used.
	m3 := New(m.log, "2026.10", false, func() bool { return true }, nil)
	if m3.Status().Source != "builtin" {
		t.Fatal("use=false must serve the built-in interface")
	}
	// After an update to the next release the bundle for the one before is
	// left alone: the new release's own interface is newer.
	m4 := New(m.log, "2026.11", true, func() bool { return true }, nil)
	if m4.Status().Source != "builtin" {
		t.Fatal("a bundle for 2026.10 must not be served by 2026.11")
	}
}

func TestCheckRejects(t *testing.T) {
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	cases := map[string]func(ed25519.PrivateKey) http.Handler{
		"wrong key": func(ed25519.PrivateKey) http.Handler {
			return publish(t, other, "v1", API, 1, map[string]string{"index.html": "x"})
		},
		"no index": func(p ed25519.PrivateKey) http.Handler {
			return publish(t, p, "v1", API, 1, map[string]string{"other.html": "x"})
		},
		"escaping path": func(p ed25519.PrivateKey) http.Handler {
			return publish(t, p, "v1", API, 1, map[string]string{"index.html": "x", "../evil": "x"})
		},
	}
	for name, site := range cases {
		t.Run(name, func(t *testing.T) {
			priv := withKey(t)
			srv := httptest.NewServer(site(priv))
			defer srv.Close()
			m := testManager(t, srv.URL)
			if err := m.Check(context.Background()); err == nil {
				t.Fatal("expected an error")
			}
			if m.Status().Source != "builtin" {
				t.Fatal("a rejected bundle must not be served")
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(m.dir), "evil")); err == nil {
				t.Fatal("wrote outside the bundle directory")
			}
		})
	}
}

func TestCheckSkipsBundleForOtherAPI(t *testing.T) {
	for name, lv := range map[string][2]int{"needs newer daemon": {API + 1, API + 1}, "older than daemon": {API - 1, API - 1}} {
		t.Run(name, func(t *testing.T) {
			priv := withKey(t)
			srv := httptest.NewServer(publish(t, priv, "v1", lv[0], lv[1], map[string]string{"index.html": "x"}))
			defer srv.Close()
			m := testManager(t, srv.URL)
			if err := m.Check(context.Background()); err != nil {
				t.Fatal(err)
			}
			if m.Status().Source != "builtin" {
				t.Fatal("a bundle for another API level must not be served")
			}
		})
	}
}
