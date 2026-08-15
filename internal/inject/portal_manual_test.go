//go:build linux

package inject

import (
	"os"
	"testing"

	"vito/internal/xdgportal"
)

// TestPortalSession establishes a real RemoteDesktop portal session — the
// permission dialog appears — without sending a single keystroke, so it is safe
// to run while other windows are focused. It is the quickest way to answer the
// two questions that decide the portal backend on a given desktop:
//
//	does this compositor implement the portal at all, and
//	is the grant remembered (a restore token comes back) or re-asked every time?
//
// Gated behind an environment variable: it is interactive, so it must never run
// in CI or as part of a plain `go test ./...`.
//
//	VITO_PORTAL_TEST=1 go test ./internal/inject/ -run TestPortalSession -v
func TestPortalSession(t *testing.T) {
	if os.Getenv("VITO_PORTAL_TEST") == "" {
		t.Skip("interactive: set VITO_PORTAL_TEST=1 to run")
	}
	t.Logf("portal advertised: %v (version %d)", portalPresent(), PortalVersion())
	t.Logf("restore token before: %q", loadRestoreToken())

	conn, session, err := ensureSession()
	if err != nil {
		t.Fatalf("no usable portal on this desktop: %v", err)
	}
	t.Logf("session established: %s (sender %s)", session, xdgportal.SenderPart(conn))

	tok := loadRestoreToken()
	t.Logf("restore token after: %q", tok)
	if tok == "" {
		t.Log("NOTE: no restore token — this compositor will re-ask on every restart")
	}
}
