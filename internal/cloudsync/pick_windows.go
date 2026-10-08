//go:build windows

package cloudsync

import (
	"context"
	"encoding/base64"
	"os/exec"
	"strings"
	"syscall"
	"unicode/utf16"
)

// PickFolder opens Windows' own folder picker and returns the chosen path, or
// "" when the user cancels.
//
// Vito runs in the background, and Windows won't let a background process put
// a window in front of the browser that asked for it. So the dialog gets an
// owner that is shown topmost (tiny, transparent, in the middle of the screen),
// which keeps the dialog above every other window. The script travels as
// -EncodedCommand, so no quoting or line break can break it on the way.
func PickFolder(ctx context.Context, title, start string) (string, error) {
	script := `[Console]::OutputEncoding = [Text.Encoding]::UTF8
Add-Type -AssemblyName System.Windows.Forms
[System.Windows.Forms.Application]::EnableVisualStyles()
$d = New-Object System.Windows.Forms.FolderBrowserDialog
$d.Description = $env:VITO_PICK_TITLE
$d.UseDescriptionForTitle = $true
$d.ShowNewFolderButton = $true
if ($env:VITO_PICK_START) { $d.SelectedPath = $env:VITO_PICK_START }
$o = New-Object System.Windows.Forms.Form
$o.TopMost = $true; $o.ShowInTaskbar = $false; $o.FormBorderStyle = 'None'; $o.Opacity = 0
$o.StartPosition = 'CenterScreen'; $o.Size = New-Object System.Drawing.Size(1, 1)
$o.Show(); $o.Activate()
$r = $d.ShowDialog($o)
$o.Close()
if ($r -eq [System.Windows.Forms.DialogResult]::OK) { $d.SelectedPath }`
	u := utf16.Encode([]rune(script))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		b[2*i], b[2*i+1] = byte(c), byte(c>>8)
	}
	cmd := exec.CommandContext(ctx, "powershell", "-STA", "-NoProfile", "-NonInteractive", "-EncodedCommand", base64.StdEncoding.EncodeToString(b))
	cmd.Env = append(cmd.Environ(), "VITO_PICK_TITLE="+title, "VITO_PICK_START="+start)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	out, err := cmd.Output()
	if err != nil {
		// PowerShell reports on stderr in CLIXML; only plain text says anything.
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 && !strings.HasPrefix(string(ee.Stderr), "#< CLIXML") {
			return "", &pickError{strings.TrimSpace(string(ee.Stderr))}
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

type pickError struct{ msg string }

func (e *pickError) Error() string { return "folder picker: " + e.msg }
