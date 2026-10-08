package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"vito/assets"
	"vito/internal/achievements"
	"vito/internal/cleanup"
	"vito/internal/config"
	"vito/web"
)

// The Whistle engine as WebAssembly, pinned like the native one in
// internal/whistle/assets.go (same Cactus-Compute/needle3 revision).
const needleBase = "https://huggingface.co/Cactus-Compute/needle3/resolve/2ae11323dc000f5e70c49f7403efa6af12ba9e67/wasm/"

var needleFiles = []struct{ name, sha256 string }{
	{"needle.js", "964681b2a5ec3c4db2f06e45a5b60c8981a7d4ab4cac16ef6bf5b1988657a5f1"},
	{"needle.wasm", "c43f48e11f302087250d1e406024956781cd2f02595a5e343b3d7ddd5ef707fa"},
}

// app writes the browser version (vito.talk/app/): the interface tree as it
// is, plus what the daemon would otherwise answer from Go — the default
// config, the achievement list — and the WebAssembly engine.
func app(args []string) error {
	fl := flag.NewFlagSet("app", flag.ExitOnError)
	out := fl.String("out", "", "directory to write the site into (replaced)")
	_ = fl.Parse(args)
	if *out == "" {
		usage()
	}
	if err := os.RemoveAll(*out); err != nil {
		return err
	}
	err := fs.WalkDir(web.Files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := web.Files.ReadFile(p)
		if err != nil {
			return err
		}
		return writeFile(filepath.Join(*out, filepath.FromSlash(p)), data)
	})
	if err != nil {
		return err
	}

	sa := filepath.Join(*out, "standalone")
	def, err := json.MarshalIndent(config.Default(), "", " ")
	if err != nil {
		return err
	}
	if err := writeFile(filepath.Join(sa, "defaults.json"), def); err != nil {
		return err
	}
	ach, err := json.Marshal(map[string]any{
		"list":     achievements.List,
		"images":   assetIDs("achievements", ".png"),
		"animated": assetIDs("achievements/lottie", ".json"),
	})
	if err != nil {
		return err
	}
	if err := writeFile(filepath.Join(sa, "achievements.json"), ach); err != nil {
		return err
	}
	// The cleanup rule sets and the contract, for AI cleanup from the browser.
	var builtins []map[string]string
	for _, b := range cleanup.Builtins() {
		builtins = append(builtins, map[string]string{"id": b.ID, "name": b.Name, "description": b.Description, "rules": b.Rules})
	}
	cl, err := json.Marshal(map[string]any{"builtins": builtins, "contract": cleanup.Contract(), "default_rules": cleanup.DefaultRules})
	if err != nil {
		return err
	}
	if err := writeFile(filepath.Join(sa, "cleanup.json"), cl); err != nil {
		return err
	}
	for name, wav := range map[string][]byte{"start": assets.SoundStart, "done": assets.SoundDone, "cancel": assets.SoundCancel,
		"achievement": assets.SoundAchievement, "command": assets.SoundCommand, "warn": assets.SoundWarn} {
		if err := writeFile(filepath.Join(sa, "sounds", name+".wav"), wav); err != nil {
			return err
		}
	}
	for _, f := range needleFiles {
		data, err := fetchPinned(needleBase+f.name, f.sha256)
		if err != nil {
			return fmt.Errorf("%s: %w", f.name, err)
		}
		if err := writeFile(filepath.Join(sa, f.name), data); err != nil {
			return err
		}
	}
	fmt.Println("browser version written to", *out)
	return nil
}

func assetIDs(dir, ext string) []string {
	entries, _ := fs.ReadDir(web.Files, dir)
	var ids []string
	for _, e := range entries {
		if n := e.Name(); strings.HasSuffix(n, ext) {
			ids = append(ids, strings.TrimSuffix(n, ext))
		}
	}
	sort.Strings(ids)
	return ids
}

// fetchPinned downloads url once into the user cache and returns it after
// checking its digest.
func fetchPinned(url, sum string) ([]byte, error) {
	cache, _ := os.UserCacheDir()
	path := filepath.Join(cache, "vito", "build", sum)
	if b, err := os.ReadFile(path); err == nil && digest(b) == sum {
		return b, nil
	}
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download: %s", resp.Status)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if digest(b) != sum {
		return nil, fmt.Errorf("does not match its pinned sha256")
	}
	_ = writeFile(path, b)
	return b, nil
}

func digest(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
