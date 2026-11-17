//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOtherLauncher(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_DIRS", t.TempDir())
	apps := filepath.Join(data, "applications")
	if err := os.MkdirAll(apps, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := "/home/u/My Apps/Vito.AppImage"
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(apps, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("vito.desktop", "[Desktop Entry]\nExec=\""+exe+"\" serve %u\n")
	if otherLauncher(exe, data) {
		t.Fatal("Vito's own entry counted as another launcher")
	}
	write("hidden.desktop", "[Desktop Entry]\nExec=\""+exe+"\" %U\nNoDisplay=true\n")
	write("other.desktop", "[Desktop Entry]\nExec=/usr/bin/other %U\n")
	if otherLauncher(exe, data) {
		t.Fatal("a hidden entry or another program counted")
	}
	write("dsd-appimage-vito.desktop", "[Desktop Entry]\nType=Application\nName=Vito\nExec=\""+exe+"\" %U\n")
	if !otherLauncher(exe, data) {
		t.Fatal("the AppImage manager's entry was not found")
	}
}

func TestSetManagerIcon(t *testing.T) {
	f := filepath.Join(t.TempDir(), "dsd-appimage-vito.desktop")
	orig := "[Desktop Entry]\nType=Application\nName=Vito\nExec=\"/a/Vito.AppImage\" %U\nIcon=/icons/dsd-appimage-vito.png\nCategories=Utility;\n"
	if err := os.WriteFile(f, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	read := func() string { b, _ := os.ReadFile(f); return string(b) }
	setManagerIcon(f, "/icons/vito-abc.png")
	setManagerIcon(f, "/icons/vito-def.png")
	got := read()
	want := "[Desktop Entry]\nType=Application\nName=Vito\nExec=\"/a/Vito.AppImage\" %U\nIcon=/icons/vito-def.png\nCategories=Utility;\nX-Vito-Icon-Orig=/icons/dsd-appimage-vito.png\n"
	if got != want {
		t.Fatalf("themed:\n%s\nwant:\n%s", got, want)
	}
	setManagerIcon(f, "vito")
	if got := read(); got != orig {
		t.Fatalf("default look did not restore the entry:\n%s", got)
	}
}
