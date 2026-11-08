package webapp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstalledDarwin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	id := "bjplljdlilgfpfpomdmoaolikaeemhfl"
	store := filepath.Join(home, "Library", "Application Support", "Google", "Chrome", "Default", "Sync Data", "LevelDB")
	app := filepath.Join(home, "Applications", "Chrome Apps.localized", "Vito.app", "Contents")
	for _, d := range []string{store, app} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(store, "000003.log"), []byte("\x01,web_apps-dt-"+id+"\xc1\x06\n\xac\x02\n\x16http://127.0.0.1:4573/\x12\x1bVito"), 0o644)
	os.WriteFile(filepath.Join(app, "Info.plist"), []byte("<key>CrAppModeShortcutID</key><string>"+id+"</string>"), 0o644)

	if installed("http://127.0.0.1:4573") == nil {
		t.Error("the app installed from this address was not found")
	}
	if installed("http://127.0.0.1:4599") != nil {
		t.Error("an app from another port was taken for this one")
	}
}
