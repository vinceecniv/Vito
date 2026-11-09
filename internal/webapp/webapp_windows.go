package webapp

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf16"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// storeGlobs are the browser profiles under %LOCALAPPDATA%:
// Google\Chrome\User Data\Default, BraveSoftware\Brave-Browser\User Data\Default,
// Vivaldi\User Data\Default.
func storeGlobs() []string {
	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		return nil
	}
	return []string{
		filepath.Join(local, "*", "User Data", "*"),
		filepath.Join(local, "*", "*", "User Data", "*"),
	}
}

// launcher starts app id through the Start menu shortcut its browser made
// (Chrome puts them in "Chrome Apps", Edge and Brave straight in Programs), or
// through the app package Edge registers instead of a shortcut.
// A .lnk keeps its arguments in UTF-16, so the shortcut is found by its
// "--app-id=<id>" in that form, and opened the way Explorer would.
func launcher(id string) func() error {
	want := utf16le("--app-id=" + id)
	var found string
	for _, root := range []string{
		filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs"),
		filepath.Join(os.Getenv("USERPROFILE"), "Desktop"),
	} {
		filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || found != "" {
				return nil
			}
			if d.IsDir() || !strings.EqualFold(filepath.Ext(path), ".lnk") {
				return nil
			}
			if b, err := os.ReadFile(path); err == nil && bytes.Contains(b, want) {
				found = path
				return fs.SkipAll
			}
			return nil
		})
		if found != "" {
			return func() error {
				// The browser may take the foreground from Vito: with the
				// manifest's launch_handler it then brings the open window
				// forward instead of opening another, which Windows would
				// otherwise leave flashing in the taskbar.
				procAllowSetForegroundWindow.Call(asfwAny)
				return exec.Command("rundll32", "shell32.dll,ShellExec_RunDLL", found).Start()
			}
		}
	}
	if aumid := packagedApp(id); aumid != "" {
		return func() error {
			procAllowSetForegroundWindow.Call(asfwAny)
			return exec.Command("explorer.exe", `shell:AppsFolder\`+aumid).Start()
		}
	}
	return nil
}

// packagesKey lists the app packages installed for this user, each with the
// folder it lives in.
const packagesKey = `Software\Classes\Local Settings\Software\Microsoft\Windows\CurrentVersion\AppModel\Repository\Packages`

var pwaApplication = regexp.MustCompile(`<Application\s(?:[^>]*\s)?Id="([^"]+)"[^>]*Parameters="[^"]*--app-id=([a-p]{32})`)

// packagedApp returns the AppUserModelID of app id when Edge installed it as
// an app package rather than a shortcut, as it does on current Windows: an
// MSIX package ("127.0.0.1-70C77FDB") whose manifest starts Edge with
// "--app-id=<id>". "" when there is none.
func packagedApp(id string) string {
	k, err := registry.OpenKey(registry.CURRENT_USER, packagesKey, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return ""
	}
	defer k.Close()
	names, _ := k.ReadSubKeyNames(-1)
	want := []byte("--app-id=" + id)
	for _, full := range names {
		if systemPackage(full) {
			continue
		}
		pk, err := registry.OpenKey(k, full, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		root, _, err := pk.GetStringValue("PackageRootFolder")
		pk.Close()
		if err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, "AppxManifest.xml"))
		if err != nil || !bytes.Contains(b, want) {
			continue
		}
		for _, m := range pwaApplication.FindAllSubmatch(b, -1) {
			if string(m[2]) == id {
				return familyName(full) + "!" + string(m[1])
			}
		}
	}
	return ""
}

// systemPackage skips the packages Windows and Microsoft ship (most of the
// list), so a click does not read hundreds of manifests. Edge's web-app
// packages are named after the site and signed by the site's own id.
func systemPackage(full string) bool {
	for _, p := range []string{"Microsoft", "Windows", "windows.", "Clipchamp.", "MicrosoftCorporationII."} {
		if strings.HasPrefix(full, p) {
			return true
		}
	}
	for _, pub := range []string{"_8wekyb3d8bbwe", "_cw5n1h2txyewy"} {
		if strings.HasSuffix(full, pub) {
			return true
		}
	}
	return false
}

// familyName turns a package full name (Name_Version_Arch_ResourceId_PublisherId)
// into its family name (Name_PublisherId).
func familyName(full string) string {
	parts := strings.Split(full, "_")
	if len(parts) < 2 {
		return full
	}
	return parts[0] + "_" + parts[len(parts)-1]
}

func utf16le(s string) []byte {
	var b []byte
	for _, u := range utf16.Encode([]rune(s)) {
		b = append(b, byte(u), byte(u>>8))
	}
	return b
}

var procAllowSetForegroundWindow = windows.NewLazySystemDLL("user32.dll").NewProc("AllowSetForegroundWindow")

const asfwAny = ^uintptr(0) // ASFW_ANY

// focusApp: Windows has no app id on a window short of COM; the browser, let
// into the foreground and told by launch_handler, focuses its own window.
func focusApp(string) bool { return false }
