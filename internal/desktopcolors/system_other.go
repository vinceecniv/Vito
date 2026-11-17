//go:build !windows && !darwin

package desktopcolors

// The system accent themes exist only on Windows (windows.go) and macOS
// (macos.go).
func Windows() (*Palette, error) { return nil, ErrNone }
func MacOS() (*Palette, error)   { return nil, ErrNone }

// SystemKey: see windows.go.
func SystemKey() string { return "" }
