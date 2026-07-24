// Package update checks whether a newer Vito has been released, and on Windows
// can install it.
//
// Versions are calendar-based — year.month, plus a counter for further releases
// that month (2026.7, 2026.7.1, 2026.8) — so comparing them is comparing the
// numbers in order. A build that says "dev" was made by hand and never counts
// as out of date.
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Repo is where releases are published. The check is a single unauthenticated
// GET; GitHub allows 60 an hour per address, and Vito asks once a day.
const Repo = "vinceecniv/vito"

const apiURL = "https://api.github.com/repos/" + Repo + "/releases/latest"

// Release is what the UI needs to know about the newest published version.
type Release struct {
	Version     string    `json:"version"`               // e.g. "2026.8"
	Available   bool      `json:"available"`             // newer than what is running
	URL         string    `json:"url,omitempty"`         // the release page
	Notes       string    `json:"notes,omitempty"`       // markdown, as written by the author
	PublishedAt time.Time `json:"published_at,omitzero"` //
	// Installer names the Windows setup asset, when the release has one.
	Installer     string `json:"installer,omitempty"`
	InstallerSize int64  `json:"installer_size,omitempty"`
	installerURL  string
	checksumURL   string
}

// Checker caches the last answer so opening the status page doesn't fire a
// request every time.
type Checker struct {
	Current string // the running version
	Client  *http.Client

	mu      sync.Mutex
	last    *Release
	lastAt  time.Time
	lastErr error
}

// MaxAge is how long a result stays fresh.
const MaxAge = 24 * time.Hour

func NewChecker(current string) *Checker {
	return &Checker{Current: current, Client: &http.Client{Timeout: 15 * time.Second}}
}

// Cached returns the last result without going near the network.
func (c *Checker) Cached() (*Release, time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last, c.lastAt
}

// Check returns the newest release, asking GitHub only when what we have is
// older than MaxAge (or force is set).
func (c *Checker) Check(ctx context.Context, force bool) (*Release, error) {
	c.mu.Lock()
	if !force && c.last != nil && time.Since(c.lastAt) < MaxAge {
		r := c.last
		c.mu.Unlock()
		return r, nil
	}
	c.mu.Unlock()

	rel, err := c.fetch(ctx)

	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.lastErr = err
		// Keep whatever we had: a flaky network shouldn't erase a known update.
		if c.last != nil {
			return c.last, nil
		}
		return nil, err
	}
	c.last, c.lastAt, c.lastErr = rel, time.Now(), nil
	return rel, nil
}

func (c *Checker) fetch(ctx context.Context) (*Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "vito/"+c.Current)
	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// A repository with no published release answers 404 here. That is an
	// answer, not a failure: there is simply nothing newer to offer.
	if resp.StatusCode == http.StatusNotFound {
		return &Release{}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github: HTTP %d", resp.StatusCode)
	}
	var out struct {
		TagName     string    `json:"tag_name"`
		Name        string    `json:"name"`
		Body        string    `json:"body"`
		HTMLURL     string    `json:"html_url"`
		Draft       bool      `json:"draft"`
		Prerelease  bool      `json:"prerelease"`
		PublishedAt time.Time `json:"published_at"`
		Assets      []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
			Size int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, err
	}
	if out.Draft || out.Prerelease {
		return nil, fmt.Errorf("latest release is a draft or pre-release")
	}

	rel := &Release{
		Version:     strings.TrimPrefix(out.TagName, "v"),
		URL:         out.HTMLURL,
		Notes:       out.Body,
		PublishedAt: out.PublishedAt,
	}
	rel.Available = Newer(rel.Version, c.Current)
	for _, a := range out.Assets {
		switch {
		case strings.HasSuffix(a.Name, ".exe"):
			rel.Installer, rel.installerURL, rel.InstallerSize = a.Name, a.URL, a.Size
		case strings.HasSuffix(a.Name, ".sha256"):
			rel.checksumURL = a.URL
		}
	}
	return rel, nil
}

// Newer reports whether version a is later than b. Anything that isn't a
// calendar version — "dev", a hand build — is never newer and never older, so a
// developer is left alone.
func Newer(a, b string) bool {
	pa, oka := parse(a)
	pb, okb := parse(b)
	if !oka || !okb {
		return false
	}
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func parse(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(parts) < 2 || len(parts) > 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// Download fetches the release's installer to a temporary file and checks it
// against the published SHA-256. It returns the path; the caller runs it.
//
// The checksum is the whole point: this downloads an executable and hands it to
// Windows. Without a signature to verify (Vito is not code-signed yet), the
// checksum published beside the asset is the only thing standing between a
// tampered download and running it.
func (c *Checker) Download(ctx context.Context, rel *Release, onProgress func(frac float64)) (string, error) {
	if rel == nil || rel.installerURL == "" {
		return "", fmt.Errorf("this release has no installer to download")
	}
	if rel.checksumURL == "" {
		return "", fmt.Errorf("this release has no checksum published; refusing to run an unverified installer")
	}
	want, err := c.fetchChecksum(ctx, rel.checksumURL)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rel.installerURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}

	dir, err := os.MkdirTemp("", "vito-update-")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, filepath.Base(rel.Installer))
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	sum := sha256.New()
	var got int64
	buf := make([]byte, 64<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				return "", werr
			}
			sum.Write(buf[:n])
			got += int64(n)
			if onProgress != nil && rel.InstallerSize > 0 {
				onProgress(float64(got) / float64(rel.InstallerSize))
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			return "", rerr
		}
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if have := hex.EncodeToString(sum.Sum(nil)); !strings.EqualFold(have, want) {
		os.RemoveAll(dir)
		return "", fmt.Errorf("checksum mismatch: expected %s, got %s", want, have)
	}
	return path, nil
}

func (c *Checker) fetchChecksum(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("checksum: HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if err != nil {
		return "", err
	}
	// "<hex>  <filename>", the shasum format the build script writes.
	fields := strings.Fields(string(b))
	if len(fields) == 0 || len(fields[0]) != 64 {
		return "", fmt.Errorf("checksum file is not in the expected format")
	}
	return fields[0], nil
}
