//go:build !linux && !windows

package inject

import "vito/internal/config"

// ActiveBackend names the delivery route actually in use. Only Linux picks
// between several, so everywhere else there is nothing to choose and nothing to
// report — callers can ask without first checking which OS they are on.
func ActiveBackend(cfg config.Injection) string { return "" }

// adjustMode is a no-op here. It exists for the Linux case where no injection
// route is present at all and paste has to degrade to the clipboard; macOS
// always has CGEventPost. A missing Accessibility grant is a different thing —
// an error the user can act on, reported as such rather than silently downgraded.
func adjustMode(cfg config.Injection, mode Mode) Mode { return mode }
