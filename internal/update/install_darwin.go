//go:build darwin

package update

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// appBundleInstaller replaces Vito.app with the one in the release's disk
// image and opens the new copy. Every release is signed with the same
// certificate (packaging/make-signing-cert.sh), so the Accessibility grant
// survives the swap.
type appBundleInstaller struct{ bundle string }

func (appBundleInstaller) asset(rel *Release) string {
	name := "Vito-" + rel.Version + ".dmg"
	if !rel.HasAsset(name) {
		return ""
	}
	return name
}

func (i appBundleInstaller) install(dmg string) error {
	mnt, err := os.MkdirTemp("", "vito-dmg-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(mnt)
	if out, err := exec.Command("hdiutil", "attach", "-nobrowse", "-readonly", "-noautoopen", "-mountpoint", mnt, dmg).CombinedOutput(); err != nil {
		return fmt.Errorf("hdiutil attach: %v: %s", err, strings.TrimSpace(string(out)))
	}
	detach := func() { _ = exec.Command("hdiutil", "detach", "-force", mnt).Run() }

	parent := filepath.Dir(i.bundle)
	fresh := filepath.Join(parent, ".Vito.app.new")
	old := filepath.Join(parent, ".Vito.app.old")
	_ = os.RemoveAll(fresh)
	_ = os.RemoveAll(old)
	// ditto keeps the signature, extended attributes and symlinks intact.
	if out, err := exec.Command("ditto", filepath.Join(mnt, "Vito.app"), fresh).CombinedOutput(); err != nil {
		detach()
		_ = os.RemoveAll(fresh)
		return fmt.Errorf("ditto: %v: %s", err, strings.TrimSpace(string(out)))
	}
	detach()
	_ = os.RemoveAll(filepath.Dir(dmg))

	if err := os.Rename(i.bundle, old); err != nil {
		_ = os.RemoveAll(fresh)
		return err
	}
	if err := os.Rename(fresh, i.bundle); err != nil {
		_ = os.Rename(old, i.bundle) // put the running version back
		return err
	}
	_ = os.RemoveAll(old)
	// -n: a new instance even though this one is still running; it waits for
	// the port, which is free as soon as we exit below.
	if err := exec.Command("open", "-n", i.bundle).Start(); err != nil {
		return err
	}
	os.Exit(0)
	return nil
}

// platformInstaller updates the app bundle Vito runs from, when the user can
// write next to it (/Applications for an admin, ~/Applications always).
func platformInstaller() (installer, string) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err.Error()
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	// .../Vito.app/Contents/MacOS/vito
	bundle := filepath.Dir(filepath.Dir(filepath.Dir(exe)))
	if !strings.HasSuffix(bundle, ".app") {
		return nil, "not running from an app bundle"
	}
	if strings.HasPrefix(bundle, "/Volumes/") {
		return nil, "running from the disk image; move Vito to Applications"
	}
	parent := filepath.Dir(bundle)
	f, err := os.CreateTemp(parent, ".vito-write-test-")
	if err != nil {
		return nil, fmt.Sprintf("cannot write to %s", parent)
	}
	f.Close()
	_ = os.Remove(f.Name())
	return appBundleInstaller{bundle: bundle}, ""
}
