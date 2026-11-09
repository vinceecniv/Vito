package webapp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstalledWindows(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "Local"))
	t.Setenv("APPDATA", filepath.Join(home, "Roaming"))
	t.Setenv("USERPROFILE", home)
	id := "bjplljdlilgfpfpomdmoaolikaeemhfl"
	store := filepath.Join(home, "Local", "Google", "Chrome", "User Data", "Default", "Sync Data", "LevelDB")
	apps := filepath.Join(home, "Roaming", "Microsoft", "Windows", "Start Menu", "Programs", "Chrome Apps")
	for _, d := range []string{store, apps} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(store, "000003.log"), []byte("\x01,web_apps-dt-"+id+"\xc1\x06\n\xac\x02\n\x16http://127.0.0.1:4573/\x12\x1bVito"), 0o644)
	lnk := append([]byte("L\x00\x00\x00junk"), utf16le("--profile-directory=Default --app-id="+id)...)
	os.WriteFile(filepath.Join(apps, "Vito.lnk"), lnk, 0o644)

	if installed("http://127.0.0.1:4573") == nil {
		t.Error("the app installed from this address was not found")
	}
	if installed("http://127.0.0.1:4599") != nil {
		t.Error("an app from another port was taken for this one")
	}
}

// Edge on current Windows installs a web app as an app package instead of a
// shortcut; its manifest names the app id, its family name the AUMID.
func TestPackagedApp(t *testing.T) {
	manifest := []byte(`<Applications><Application Id="App" uap10:HostId="PWA" uap10:Parameters="--app-id=bjplljdlilgfpfpomdmoaolikaeemhfl --ip-edge-aumid=Microsoft.MicrosoftEdge.Stable_8wekyb3d8bbwe!MSEDGE">`)
	m := pwaApplication.FindSubmatch(manifest)
	if m == nil || string(m[1]) != "App" || string(m[2]) != "bjplljdlilgfpfpomdmoaolikaeemhfl" {
		t.Fatalf("manifest not read: %q", m)
	}
	if got := familyName("127.0.0.1-70C77FDB_1.0.0.1_neutral__01gcjt78yvm32"); got != "127.0.0.1-70C77FDB_01gcjt78yvm32" {
		t.Errorf("familyName = %q", got)
	}
}

// TestPackagedAppHere looks the installed Vito up on this machine:
// VITO_PWA_ID=<app id> go test ./internal/webapp/ -run TestPackagedAppHere -v
func TestPackagedAppHere(t *testing.T) {
	id := os.Getenv("VITO_PWA_ID")
	if id == "" {
		t.Skip("set VITO_PWA_ID to an installed web app's id")
	}
	t.Logf("AUMID %q", packagedApp(id))
	if installed("http://127.0.0.1:4573") == nil {
		t.Error("installed web app not found")
	}
}
