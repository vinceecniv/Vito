//go:build !linux

package main

// refreshLauncherIcon: only Linux's launcher shows Vito's look (launchericon_linux.go).
func refreshLauncherIcon(accent, radius string) {}
