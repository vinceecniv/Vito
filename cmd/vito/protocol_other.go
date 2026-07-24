//go:build !windows && !linux

package main

// registerLaunchProtocol is a no-op on platforms without a protocol handler.
func registerLaunchProtocol() {}
