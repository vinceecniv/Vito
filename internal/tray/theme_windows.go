//go:build windows

package tray

import "golang.org/x/sys/windows/registry"

// darkTaskbar reports whether the Windows taskbar/system UI is in dark mode
// (SystemUsesLightTheme == 0), so the tray icon can use its dark variant.
func darkTaskbar() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`SOFTWARE\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("SystemUsesLightTheme")
	if err != nil {
		return false
	}
	return v == 0
}
