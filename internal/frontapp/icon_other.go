//go:build !windows && !linux

package frontapp

// iconData has no source here yet (macOS: the bundle's .icns would do).
func iconData(string) ([]byte, string) { return nil, "" }
