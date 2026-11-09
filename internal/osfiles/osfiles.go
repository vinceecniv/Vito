// Package osfiles does the few things with files that each system does its
// own way: where the user's documents are, showing a folder in the file
// manager, and moving something to the trash instead of deleting it.
package osfiles

import (
	"errors"
	"os"
	"path/filepath"
)

// ErrNoTrash means the system offers no trash for the path; nothing was moved.
var ErrNoTrash = errors.New("no trash for this location")

// homeDocuments is ~/Documents, the fallback wherever the system names none.
func homeDocuments() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Documents")
}
