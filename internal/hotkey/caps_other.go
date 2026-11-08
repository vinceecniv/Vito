//go:build !linux

package hotkey

// CapsAsMenu is a Linux matter: elsewhere Vito reads Caps Lock itself.
func CapsAsMenu(on bool) error { return nil }

// NiriCapsStatus: niri is a Linux compositor.
func NiriCapsStatus() NiriCaps { return NiriCaps{} }

// SetNiriCaps: niri is a Linux compositor.
func SetNiriCaps(on bool) error { return ErrNiriManual }
