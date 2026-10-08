//go:build windows

package cloudsync

import (
	"context"
	"os/exec"
	"strings"
	"syscall"
)

// PickFolder opens Windows' own folder picker and returns the chosen path, or
// "" when the user cancels. The dialog belongs to a hidden, topmost form, so
// it opens in front of the browser window that asked for it.
func PickFolder(ctx context.Context, title, start string) (string, error) {
	script := `[Console]::OutputEncoding = [Text.Encoding]::UTF8
Add-Type -AssemblyName System.Windows.Forms
$d = New-Object System.Windows.Forms.FolderBrowserDialog
$d.Description = $env:VITO_PICK_TITLE
$d.UseDescriptionForTitle = $true
if ($env:VITO_PICK_START) { $d.SelectedPath = $env:VITO_PICK_START }
$o = New-Object System.Windows.Forms.Form -Property @{ TopMost = $true; ShowInTaskbar = $false }
if ($d.ShowDialog($o) -eq [System.Windows.Forms.DialogResult]::OK) { $d.SelectedPath }`
	cmd := exec.CommandContext(ctx, "powershell", "-STA", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.Env = append(cmd.Environ(), "VITO_PICK_TITLE="+title, "VITO_PICK_START="+start)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
