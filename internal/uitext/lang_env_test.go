//go:build !windows

package tray

func runtimeUsesEnv() bool { return true }
