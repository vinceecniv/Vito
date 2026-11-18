//go:build darwin

package frontapp

import (
	"os/exec"
	"regexp"
	"strings"
)

// On macOS lsappinfo names the frontmost application and its bundle id
// without asking for any permission (System Events would). Written without a
// Mac, so untested.
var lsField = regexp.MustCompile(`"(CFBundleIdentifier|LSDisplayName)"\s*=\s*"([^"]*)"`)

func current() App {
	front, err := exec.Command("lsappinfo", "front").Output()
	if err != nil {
		return App{}
	}
	out, err := exec.Command("lsappinfo", "info", "-only", "bundleid", "-only", "name", strings.TrimSpace(string(front))).Output()
	if err != nil {
		return App{}
	}
	var a App
	for _, m := range lsField.FindAllStringSubmatch(string(out), -1) {
		if m[1] == "CFBundleIdentifier" {
			a.ID = m[2]
		} else {
			a.Name = m[2]
		}
	}
	return a
}
