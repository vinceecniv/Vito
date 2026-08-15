//go:build !linux && !windows

package inject

import "vito/internal/config"

// ActiveBackend names the delivery route actually in use. Only Linux picks
// between several, so everywhere else there is nothing to choose and nothing to
// report — callers can ask without first checking which OS they are on.
func ActiveBackend(cfg config.Injection) string { return "" }
