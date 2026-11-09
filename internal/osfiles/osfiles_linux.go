package osfiles

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Documents is XDG_DOCUMENTS_DIR from user-dirs.dirs (localized, e.g.
// ~/Documenten), else ~/Documents.
func Documents() string {
	home, _ := os.UserHomeDir()
	// In a Flatpak XDG_CONFIG_HOME is the app's own; the user's file is
	// still at ~/.config (shared read-only by the manifest).
	for _, cfg := range []string{os.Getenv("XDG_CONFIG_HOME"), filepath.Join(home, ".config")} {
		if cfg == "" {
			continue
		}
		if v := userDirsDocuments(filepath.Join(cfg, "user-dirs.dirs"), home); v != "" {
			return v
		}
	}
	return homeDocuments()
}

func userDirsDocuments(path, home string) string {
	if f, err := os.Open(path); err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			v, ok := strings.CutPrefix(line, "XDG_DOCUMENTS_DIR=")
			if !ok {
				continue
			}
			v = strings.Trim(v, `"`)
			v = strings.Replace(v, "$HOME", home, 1)
			if v != "" && v != home {
				return v
			}
		}
	}
	return ""
}

// OpenFolder shows dir in the file manager.
func OpenFolder(dir string) error { return exec.Command("xdg-open", dir).Start() }

// Trash moves path to the home trash as the freedesktop.org spec has it
// (files/ plus an .trashinfo in info/), so the file manager can restore it.
// Only within the home's filesystem; elsewhere ErrNoTrash.
func Trash(path string) error {
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		data = filepath.Join(home, ".local", "share")
	}
	trash := filepath.Join(data, "Trash")
	files, info := filepath.Join(trash, "files"), filepath.Join(trash, "info")
	if err := os.MkdirAll(files, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(info, 0o700); err != nil {
		return err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	base := filepath.Base(abs)
	name := base
	for i := 2; ; i++ {
		f, err := os.OpenFile(filepath.Join(info, name+".trashinfo"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			u := (&url.URL{Path: abs}).EscapedPath()
			_, werr := fmt.Fprintf(f, "[Trash Info]\nPath=%s\nDeletionDate=%s\n", u, time.Now().Format("2006-01-02T15:04:05"))
			f.Close()
			if werr != nil {
				os.Remove(filepath.Join(info, name+".trashinfo"))
				return werr
			}
			break
		}
		if !os.IsExist(err) {
			return err
		}
		name = base + "." + strconv.Itoa(i)
	}
	if err := os.Rename(abs, filepath.Join(files, name)); err != nil {
		os.Remove(filepath.Join(info, name+".trashinfo"))
		return ErrNoTrash
	}
	return nil
}
