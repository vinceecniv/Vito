package cloudsync

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// ErrNotFound is what Get returns for a file that isn't there.
var ErrNotFound = errors.New("not found")

// ErrNoPicker means this system has no folder picker Vito can open; the
// settings page then takes the path as text.
var ErrNoPicker = errors.New("no folder picker available")

// Remote is the little sync needs from shared storage: paths are
// slash-separated and relative to Vito's sync folder.
type Remote interface {
	Get(ctx context.Context, path string) ([]byte, error)
	Put(ctx context.Context, path string, data []byte) error
	// List returns the names of the files directly in dir.
	List(ctx context.Context, dir string) ([]string, error)
}

// SubFolder is what Vito creates inside the folder the user picks.
const SubFolder = "Vito Sync"

// folderRemote is a folder on this computer that a sync app — Dropbox,
// OneDrive, Nextcloud, iCloud Drive, Syncthing, a network share — keeps the
// same everywhere. Vito only reads and writes files; moving them between
// computers is the sync app's job. Every computer writes only its own files,
// so the sync app never sees two computers change the same one.
type folderRemote struct{ root string }

func openFolder(base string) (folderRemote, error) {
	if strings.TrimSpace(base) == "" {
		return folderRemote{}, errors.New("no sync folder chosen")
	}
	if fi, err := os.Stat(base); err != nil || !fi.IsDir() {
		return folderRemote{}, fmt.Errorf("the sync folder %s isn't there — is its sync app installed and signed in?", base)
	}
	root := filepath.Join(base, SubFolder)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return folderRemote{}, err
	}
	return folderRemote{root}, nil
}

func (f folderRemote) path(p string) string { return filepath.Join(f.root, filepath.FromSlash(p)) }

func (f folderRemote) Get(_ context.Context, p string) ([]byte, error) {
	b, err := os.ReadFile(f.path(p))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	return b, err
}

// Put writes beside the target and renames it into place, so a sync app
// watching the folder never picks up half a file.
func (f folderRemote) Put(_ context.Context, p string, data []byte) error {
	dst := f.path(p)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	tmp := filepath.Join(filepath.Dir(dst), "."+filepath.Base(dst)+"."+hex.EncodeToString(b)+".tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (f folderRemote) List(_ context.Context, dir string) ([]string, error) {
	entries, err := os.ReadDir(f.path(dir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		// Skip our own half-written files and sync apps' conflict copies.
		if n := e.Name(); !e.IsDir() && !strings.HasPrefix(n, ".") && strings.HasSuffix(n, ".json") {
			names = append(names, n)
		}
	}
	return names, nil
}

// Candidate is a sync folder found on this computer.
type Candidate struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// Candidates lists the folders of the sync apps installed here, where they
// keep them by default, for the settings page to offer.
func Candidates() []Candidate {
	home, _ := os.UserHomeDir()
	var out []Candidate
	seen := map[string]bool{}
	add := func(name, p string) {
		if p == "" {
			return
		}
		p = filepath.Clean(p)
		key := strings.ToLower(p)
		if seen[key] {
			return
		}
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			seen[key] = true
			out = append(out, Candidate{name, p})
		}
	}
	// Dropbox says where it keeps its folders (personal and business).
	for _, base := range []string{os.Getenv("APPDATA"), os.Getenv("LOCALAPPDATA"), filepath.Join(home, ".dropbox")} {
		if base == "" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(base, "Dropbox", "info.json"))
		if err != nil {
			b, err = os.ReadFile(filepath.Join(base, "info.json"))
		}
		if err != nil {
			continue
		}
		var info map[string]struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(b, &info) == nil {
			add("Dropbox", info["personal"].Path)
			add("Dropbox (business)", info["business"].Path)
		}
	}
	add("Dropbox", filepath.Join(home, "Dropbox"))
	switch runtime.GOOS {
	case "windows":
		add("OneDrive", os.Getenv("OneDriveConsumer"))
		add("OneDrive (work or school)", os.Getenv("OneDriveCommercial"))
		add("OneDrive", os.Getenv("OneDrive"))
		add("iCloud Drive", filepath.Join(home, "iCloudDrive"))
		for _, d := range []string{"G", "H", "I"} {
			add("Google Drive", d+`:\My Drive`)
			add("Google Drive", d+`:\Mijn Drive`)
		}
	case "darwin":
		add("iCloud Drive", filepath.Join(home, "Library", "Mobile Documents", "com~apple~CloudDocs"))
		// The File Provider apps all live here now: Dropbox, OneDrive-*, GoogleDrive-*.
		if entries, err := os.ReadDir(filepath.Join(home, "Library", "CloudStorage")); err == nil {
			for _, e := range entries {
				name := e.Name()
				label := strings.SplitN(name, "-", 2)[0]
				if label == "GoogleDrive" {
					label = "Google Drive"
				}
				add(label, filepath.Join(home, "Library", "CloudStorage", name))
			}
		}
	default:
		add("OneDrive", filepath.Join(home, "OneDrive"))
		add("pCloud", filepath.Join(home, "pCloudDrive"))
	}
	add("Google Drive", filepath.Join(home, "Google Drive"))
	add("Nextcloud", filepath.Join(home, "Nextcloud"))
	add("ownCloud", filepath.Join(home, "ownCloud"))
	add("Syncthing", filepath.Join(home, "Sync"))
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
