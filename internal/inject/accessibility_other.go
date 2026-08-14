//go:build !darwin

package inject

// Only macOS gates synthetic keystrokes behind a permission. Everywhere else
// the answer is simply yes, so callers — the settings page in particular — can
// ask without first checking which OS they are on.

// Accessible reports whether the OS lets Vito synthesise keystrokes.
func Accessible() bool { return true }

// RequestAccessibility asks the OS for that right, and reports whether it is
// held afterwards. A no-op where no such permission exists.
func RequestAccessibility() bool { return true }
