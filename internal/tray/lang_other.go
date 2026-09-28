//go:build !windows && !darwin

package tray

func osLang() string { return envLang() }
