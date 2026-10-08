// Package uibundle keeps the web interface up to date without a new release of
// Vito itself.
//
// The interface is published at vito.talk/ui/ as a zip of the same tree that
// is embedded in the binary (web.Files), described by ui.json and signed with
// an ed25519 key that only the build machine holds. The daemon fetches it at
// start and every six hours, checks the signature, the hash and that it fits
// this daemon's API, unpacks it into the cache and serves it from the next page
// load on. The embedded copy is always there underneath: when nothing has been
// downloaded, when a download is not trusted, and when ?ui=builtin asks for it.
package uibundle

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"vito/web"
)

// API is the level of the HTTP API this daemon offers the interface. Raise it
// when the UI starts to depend on something new in the daemon (a route, a
// field, an event): a bundle built against a higher level is then left alone
// by older daemons, which keep the interface they shipped with.
const API = 1

// PublicKey verifies ui.json. Its private half lives outside the repository on
// the machine that publishes (packaging/uibundle).
var PublicKey = mustKey("63LlULA2SjsFNdU0MvdJOvzxYoocEjY439M47BPQQwk=")

// DefaultURL is where the bundle is published; VITO_UI_URL overrides it.
const DefaultURL = "https://vito.talk/ui/"

// checkEvery is how often a running daemon looks for a newer interface.
const checkEvery = 6 * time.Hour

// maxBundle caps what is downloaded and unpacked: the interface is ~12 MB.
const maxBundle = 64 << 20

// Manifest is ui.json.
type Manifest struct {
	Version string `json:"version"`
	// API is the level the bundle was built against; MinAPI the lowest it
	// works with. A daemon newer than API carries a newer interface itself.
	API    int    `json:"api"`
	MinAPI int    `json:"min_api"`
	File   string `json:"file"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Built  string `json:"built,omitempty"`
	// App is the release the bundle updates the interface of. A daemon serves
	// a bundle only for its own version: after the next release its built-in
	// interface is newer than any bundle made for the one before, and stays in
	// use until a bundle for the new release is published.
	App string `json:"app"`
}

// fits reports whether a daemon of version app should serve the bundle.
func (m Manifest) fits(app string) bool {
	return m.App != "" && m.App == app && m.MinAPI <= API && m.API >= API
}

// Status is what the daemon reports about the interface it serves.
type Status struct {
	Source  string    `json:"source"`            // "builtin" or "bundle"
	Version string    `json:"version,omitempty"` // the bundle's, when Source is "bundle"
	API     int       `json:"api"`
	Checked time.Time `json:"checked,omitzero"`
	Error   string    `json:"error,omitempty"`
}

var versionRe = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.\-]{0,39}$`)

// Manager owns the downloaded interface.
type Manager struct {
	log     *slog.Logger
	app     string // this daemon's version
	url     string
	dir     string
	enabled func() bool
	changed func(Status)
	client  *http.Client

	mu      sync.Mutex
	bundle  fs.FS
	man     *Manifest
	checked time.Time
	err     string
}

// New loads the interface downloaded earlier, if it is still trusted and fits
// this daemon's version app. enabled says whether looking online is allowed
// (the update-check setting); changed is called when a new interface has been
// installed. With use false the manager never serves a download: a build made
// by hand shows its own web/.
func New(log *slog.Logger, app string, use bool, enabled func() bool, changed func(Status)) *Manager {
	m := &Manager{log: log, app: app, url: DefaultURL, enabled: enabled, changed: changed,
		client: &http.Client{Timeout: 2 * time.Minute}}
	if u := os.Getenv("VITO_UI_URL"); u != "" {
		m.url = u
	}
	if !strings.HasSuffix(m.url, "/") {
		m.url += "/"
	}
	if c, err := os.UserCacheDir(); err == nil {
		m.dir = filepath.Join(c, "vito", "ui")
	}
	if use {
		m.loadInstalled()
	}
	return m
}

// Builtin is the interface embedded in the binary.
func Builtin() fs.FS { return web.Files }

// FS is the interface to serve: the downloaded one when there is one, else the
// embedded one.
func (m *Manager) FS() fs.FS {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.bundle != nil {
		return m.bundle
	}
	return web.Files
}

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := Status{Source: "builtin", API: API, Checked: m.checked, Error: m.err}
	if m.bundle != nil {
		st.Source, st.Version = "bundle", m.man.Version
	}
	return st
}

// Run checks shortly after start and then every six hours, until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	wait := 20 * time.Second // let startup settle first
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = checkEvery
		if !m.enabled() {
			continue
		}
		if err := m.Check(ctx); err != nil {
			m.log.Info("interface update check failed", "err", err)
		}
	}
}

// Check fetches ui.json and installs the bundle it names when it is new,
// trusted and fits this daemon.
func (m *Manager) Check(ctx context.Context) error {
	err := m.check(ctx)
	m.mu.Lock()
	m.checked = time.Now()
	m.err = ""
	if err != nil {
		m.err = err.Error()
	}
	m.mu.Unlock()
	return err
}

func (m *Manager) check(ctx context.Context) error {
	if m.dir == "" {
		return errors.New("no cache directory")
	}
	raw, err := m.get(ctx, "ui.json", 64<<10)
	if err != nil {
		return err
	}
	sig, err := m.get(ctx, "ui.json.sig", 1<<10)
	if err != nil {
		return err
	}
	man, err := verify(raw, sig)
	if err != nil {
		return err
	}
	if !man.fits(m.app) {
		m.log.Debug("interface bundle does not fit this version", "version", man.Version, "for", man.App, "api", man.API, "min_api", man.MinAPI)
		return nil
	}
	m.mu.Lock()
	same := m.man != nil && m.man.Version == man.Version
	m.mu.Unlock()
	if same {
		return nil
	}

	zipData, err := m.get(ctx, man.File, maxBundle)
	if err != nil {
		return err
	}
	if int64(len(zipData)) != man.Size {
		return fmt.Errorf("bundle is %d bytes, ui.json says %d", len(zipData), man.Size)
	}
	if sum := sha256.Sum256(zipData); hex.EncodeToString(sum[:]) != man.SHA256 {
		return errors.New("bundle does not match its checksum")
	}

	dest := filepath.Join(m.dir, man.Version)
	if err := unpack(zipData, dest); err != nil {
		_ = os.RemoveAll(dest)
		return err
	}
	// The manifest and its signature go along, so the next start can check
	// again what it is about to serve.
	if err := os.WriteFile(filepath.Join(dest, "ui.json"), raw, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dest, "ui.json.sig"), sig, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(m.dir, "current"), []byte(man.Version), 0o644); err != nil {
		return err
	}

	m.mu.Lock()
	m.bundle, m.man = os.DirFS(dest), &man
	m.mu.Unlock()
	m.log.Info("interface updated", "version", man.Version)
	m.prune(man.Version)
	if m.changed != nil {
		m.changed(m.Status())
	}
	return nil
}

// loadInstalled picks up the bundle named in <dir>/current.
func (m *Manager) loadInstalled() {
	if m.dir == "" {
		return
	}
	v, err := os.ReadFile(filepath.Join(m.dir, "current"))
	if err != nil {
		return
	}
	ver := strings.TrimSpace(string(v))
	if !versionRe.MatchString(ver) {
		return
	}
	dest := filepath.Join(m.dir, ver)
	raw, err1 := os.ReadFile(filepath.Join(dest, "ui.json"))
	sig, err2 := os.ReadFile(filepath.Join(dest, "ui.json.sig"))
	if err1 != nil || err2 != nil {
		return
	}
	man, err := verify(raw, sig)
	if err != nil {
		m.log.Warn("downloaded interface not trusted; using the built-in one", "err", err)
		return
	}
	if !man.fits(m.app) {
		// Made for another release (most often the one before an update): the
		// interface this daemon shipped with is the right one until a bundle
		// for its version is published.
		m.log.Info("downloaded interface does not fit this version; using the built-in one", "version", man.Version)
		return
	}
	if _, err := os.Stat(filepath.Join(dest, "index.html")); err != nil {
		return
	}
	m.bundle, m.man = os.DirFS(dest), &man
	m.log.Info("serving downloaded interface", "version", man.Version)
}

// prune removes every unpacked bundle except keep.
func (m *Manager) prune(keep string) {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() && e.Name() != keep {
			_ = os.RemoveAll(filepath.Join(m.dir, e.Name()))
		}
	}
}

func (m *Manager) get(ctx context.Context, name string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.url+name, nil)
	if err != nil {
		return nil, err
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", name, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s is larger than expected", name)
	}
	return b, nil
}

// verify checks ui.json against its signature (base64) and parses it.
func verify(raw, sig []byte) (Manifest, error) {
	var man Manifest
	s, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil || !ed25519.Verify(PublicKey, raw, s) {
		return man, errors.New("ui.json is not signed by Vito")
	}
	if err := json.Unmarshal(raw, &man); err != nil {
		return man, err
	}
	if !versionRe.MatchString(man.Version) || path.Base(man.File) != man.File || !strings.HasSuffix(man.File, ".zip") {
		return man, errors.New("ui.json is malformed")
	}
	return man, nil
}

// unpack extracts a bundle into dest, refusing anything that would land
// outside it, and requires the page itself to be there.
func unpack(data []byte, dest string) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	_ = os.RemoveAll(dest)
	var total int64
	hasIndex := false
	for _, f := range zr.File {
		name := f.Name
		if strings.HasSuffix(name, "/") {
			continue
		}
		if !fs.ValidPath(name) || strings.Contains(name, `\`) {
			return fmt.Errorf("bundle holds an unsafe path: %q", name)
		}
		if name == "index.html" {
			hasIndex = true
		}
		total += int64(f.UncompressedSize64)
		if total > maxBundle {
			return errors.New("bundle unpacks larger than allowed")
		}
		target := filepath.Join(dest, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.Create(target)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, io.LimitReader(rc, maxBundle))
		rc.Close()
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	if !hasIndex {
		return errors.New("bundle has no index.html")
	}
	return nil
}

func mustKey(b64 string) ed25519.PublicKey {
	k, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(k) != ed25519.PublicKeySize {
		panic("uibundle: bad public key")
	}
	return ed25519.PublicKey(k)
}
