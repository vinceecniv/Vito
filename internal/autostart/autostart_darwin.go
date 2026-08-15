//go:build darwin

package autostart

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
)

// launchAgentLabel doubles as the plist's Label and its filename. It matches
// the app bundle identifier so everything macOS shows the user says "Vito".
const launchAgentLabel = "io.github.vinceecniv.vito"

// Supported reports whether autostart can be configured on this OS.
func Supported() bool { return true }

// plistPath is ~/Library/LaunchAgents/io.github.vinceecniv.vito.plist.
func plistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", launchAgentLabel+".plist"), nil
}

// Enabled reports whether the LaunchAgent plist exists.
func Enabled() (bool, error) {
	p, err := plistPath()
	if err != nil {
		return false, err
	}
	_, err = os.Stat(p)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

// Set writes or removes the LaunchAgent that launches `vito serve` at login,
// and asks launchd to pick the change up straight away so enabling autostart
// does not also require a logout.
//
// The path written is the binary inside Vito.app rather than the bundle, since
// launchd runs an executable, not a bundle. Running it from inside Contents/
// still resolves the app bundle, which is what keeps the notification identity
// and the granted Accessibility/microphone permissions attached to Vito.
func Set(enable bool) error {
	p, err := plistPath()
	if err != nil {
		return err
	}
	if !enable {
		_ = bootout()
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}

	exe, err := executablePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p, plistFor(exe), 0o644); err != nil {
		return err
	}
	// Best effort: the plist alone is enough from the next login onwards.
	_ = bootout()
	_ = bootstrap(p)
	return nil
}

func plistFor(exe string) []byte {
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	fmt.Fprintf(&buf, `<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>serve</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>ProcessType</key>
	<string>Interactive</string>
</dict>
</plist>
`, escape(launchAgentLabel), escape(exe))
	return buf.Bytes()
}

// escape XML-escapes a value for the plist. A path can legitimately contain
// & or < (nothing stops a user naming a folder "R&D"), and an unescaped one
// would produce a plist launchd silently refuses to load.
func escape(s string) string {
	var buf bytes.Buffer
	if err := xml.EscapeText(&buf, []byte(s)); err != nil {
		return s
	}
	return buf.String()
}

func guiTarget() string {
	if u, err := user.Current(); err == nil {
		return "gui/" + u.Uid
	}
	return fmt.Sprintf("gui/%d", os.Getuid())
}

func bootstrap(path string) error {
	return exec.Command("launchctl", "bootstrap", guiTarget(), path).Run()
}

func bootout() error {
	return exec.Command("launchctl", "bootout", guiTarget()+"/"+launchAgentLabel).Run()
}
