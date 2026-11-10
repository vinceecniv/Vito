package osfiles

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

// Documents is ~/Documents.
func Documents() string { return homeDocuments() }

// OpenFolder shows dir in Finder.
func OpenFolder(dir string) error { return exec.Command("open", dir).Start() }

// Trash moves path into ~/.Trash (a rename: no permission prompt, unlike
// asking Finder; only "Put Back" is missing).
func Trash(path string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	trash := filepath.Join(home, ".Trash")
	name := filepath.Base(path)
	dst := filepath.Join(trash, name)
	if _, err := os.Lstat(dst); err == nil {
		dst = filepath.Join(trash, name+" "+strconv.FormatInt(time.Now().Unix(), 10))
	}
	if err := os.Rename(path, dst); err != nil {
		return ErrNoTrash
	}
	return nil
}
