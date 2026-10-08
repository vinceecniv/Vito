//go:build linux

package update

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
)

// appImageInstaller swaps the user's .AppImage file for the new one and
// re-executes it. The running copy is a squashfs mounted from the old file, so
// replacing the file underneath it is safe: the mount keeps the old inode.
type appImageInstaller struct{ path string }

func appImageArch() string {
	if runtime.GOARCH == "arm64" {
		return "aarch64"
	}
	return "x86_64"
}

func (appImageInstaller) asset(rel *Release) string {
	name := "Vito-" + rel.Version + "-" + appImageArch() + ".AppImage"
	if !rel.HasAsset(name) {
		return ""
	}
	return name
}

func (i appImageInstaller) install(path string) error {
	// Copy next to the target first (the download sits in /tmp, often another
	// filesystem), then rename over it: never a half-written AppImage.
	tmp := filepath.Join(filepath.Dir(i.path), "."+filepath.Base(i.path)+".new")
	if err := copyFile(path, tmp, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp, i.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	_ = os.RemoveAll(filepath.Dir(path))
	args := append([]string{i.path}, os.Args[1:]...)
	// The listening socket and other descriptors are close-on-exec, so the new
	// version binds the port afresh.
	return syscall.Exec(i.path, args, os.Environ())
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(dst)
		return err
	}
	return out.Close()
}

// platformInstaller updates an AppImage the user can write to. Everything else
// on Linux has its own updater: Flatpak, the distribution's package manager.
func platformInstaller() (installer, string) {
	if os.Getenv("FLATPAK_ID") != "" {
		return nil, "updated by Flatpak"
	}
	ai := os.Getenv("APPIMAGE")
	if ai == "" {
		return nil, "updated by your package manager"
	}
	dir := filepath.Dir(ai)
	if f, err := os.CreateTemp(dir, ".vito-write-test-"); err != nil {
		return nil, fmt.Sprintf("cannot write to %s", dir)
	} else {
		f.Close()
		_ = os.Remove(f.Name())
	}
	return appImageInstaller{path: ai}, ""
}
