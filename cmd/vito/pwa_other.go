//go:build !windows

package main

import "errors"

// installPWA is Windows-only: elsewhere the browser's own install button is the
// only supported route, and there is no notification identity to gain by
// scripting it.
func installPWA() error {
	return errors.New("installing the web app from the command line is only supported on Windows; use the install button in your browser")
}
