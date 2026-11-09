//go:build !windows && !darwin && !linux

package osfiles

import "errors"

func Documents() string       { return homeDocuments() }
func OpenFolder(string) error { return errors.New("not supported on this system") }
func Trash(string) error      { return ErrNoTrash }
