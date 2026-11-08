package webapp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstalled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "share"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	id := "bjplljdlilgfpfpomdmoaolikaeemhfl"
	apps := filepath.Join(home, "share", "applications")
	store := filepath.Join(home, "config", "BraveSoftware", "Brave-Browser", "Default", "Sync Data", "LevelDB")
	for _, d := range []string{apps, store} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(apps, "brave-"+id+"-Default.desktop"), []byte("[Desktop Entry]\nName=Vito\nExec=/bin/true --profile-directory=Default --app-id="+id+" %U\n"), 0o644)
	os.WriteFile(filepath.Join(store, "000003.log"), []byte("\x01,web_apps-dt-"+id+"\xc1\x06\n\xac\x02\n\x16http://127.0.0.1:4573/\x12\x1bVito"), 0o644)

	if installed("http://127.0.0.1:4573") == nil {
		t.Error("the app installed from this address was not found")
	}
	if installed("http://127.0.0.1:4599") != nil {
		t.Error("an app from another port was taken for this one")
	}
	if err := installed("http://127.0.0.1:4573")(); err != nil {
		t.Error(err)
	}
}
