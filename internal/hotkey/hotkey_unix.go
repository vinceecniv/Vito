//go:build !windows

// Package hotkey registers global hotkeys where the OS allows it.
package hotkey

import (
	"log/slog"

	"vito/internal/daemon"
)

// BindInfo mirrors the Windows type so callers are platform-agnostic.
type BindInfo struct {
	Spec       string
	Registered bool
	ErrCode    string
}

// Manager is a no-op on Linux: Wayland has no global hotkeys by design — bind
// `vito toggle` / `vito cancel` in the compositor instead (see the Activation
// settings). It keeps the same shape as the Windows Manager.
type Manager struct{}

// New creates a no-op Manager.
func New(d *daemon.Daemon, log *slog.Logger) *Manager { return &Manager{} }

// Start does nothing; there are no global hotkeys to register.
func (m *Manager) Start(toggle, cancel string) {}

// Rebind does nothing.
func (m *Manager) Rebind(toggle, cancel string) {}

// Status reports that global hotkeys are unsupported on this platform.
func (m *Manager) Status() (toggle, cancel BindInfo, supported bool) {
	return BindInfo{}, BindInfo{}, false
}
