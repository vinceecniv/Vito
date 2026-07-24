//go:build windows

// Package notify shows desktop notifications for dictation state feedback.
//
// Windows has no notify-send equivalent, and the WinRT toast API isn't reachable
// from pure Go, so each notification is handed to a hidden PowerShell process
// that talks to Windows.UI.Notifications. That costs a few hundred milliseconds,
// which is why sending is fire-and-forget: the dictation path must never wait on
// a toast.
//
// Unpackaged apps can't own an AppUserModelID without a Start Menu shortcut
// registering one, so toasts are shown under PowerShell's AUMID and Windows
// labels them "Windows PowerShell". Installing a shortcut with a Vito AUMID
// later is all that's needed to fix the sender name.
//
// Routine status (Send) reuses one tag, so successive messages replace each
// other in place — the same effect as notify-send's replace-id on Linux —
// leaving at most one entry behind in the Action Center. Errors (SendSticky)
// each get their own tag so they stack up and stay reviewable.
package notify

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// powershellAUMID is the shell:appsfolder id of Windows PowerShell, used only as
// a fallback. CreateToastNotifier needs an AUMID that exists in the Start Menu;
// this one always does.
const powershellAUMID = `{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe`

var (
	mu      sync.Mutex // serialises the spawns so toasts keep their order
	errSeq  atomic.Int64
	pwshOne sync.Once
	pwshBin string

	aumidOne sync.Once
	aumid    string
)

// appAUMID returns the identity toasts are shown under. Installing Vito as a PWA
// registers it in the Start Menu with its own AppUserModelID, and borrowing that
// makes Windows label the toast "Vito" with the app's icon. Without the PWA there
// is nothing to borrow and toasts fall back to PowerShell's identity.
//
// Resolved once per run: the id embeds a hash of the origin, so it differs per
// install and can't be hardcoded.
func appAUMID() string {
	aumidOne.Do(func() {
		aumid = powershellAUMID
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, powershell(), "-NoProfile", "-NonInteractive", "-Command",
			`(Get-StartApps | Where-Object { $_.Name -like 'Vito*' } | Select-Object -First 1).AppID`)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		out, err := cmd.Output()
		if err != nil {
			return
		}
		// Reject anything with a quote or newline: it goes straight into a
		// single-quoted PowerShell literal.
		if id := strings.TrimSpace(string(out)); id != "" && !strings.ContainsAny(id, "'\"\r\n") {
			aumid = id
		}
	})
	return aumid
}

// Send shows (or replaces) a routine vito notification.
// Best effort and asynchronous: a failure must never break dictation.
func Send(summary, body string) { go show(summary, body, "vito-status") }

// SendSticky shows a notification that stays in the Action Center — for errors
// worth reviewing after the popup is gone.
func SendSticky(summary, body string) {
	go show(summary, body, fmt.Sprintf("vito-err-%d", errSeq.Add(1)))
}

func show(summary, body, tag string) {
	mu.Lock()
	defer mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, powershell(), "-NoProfile", "-NonInteractive", "-Command", toastScript(summary, body, tag, appAUMID()))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} // no console flash
	_ = cmd.Run()
}

// toastScript renders the PowerShell that hands one toast to WinRT. Both type
// accelerators have to be loaded: without the XmlDocument one the document
// can't be constructed and Show() fails with an RPC server fault.
func toastScript(summary, body, tag, aumid string) string {
	xml := "<toast><visual><binding template=\"ToastGeneric\"><text>" + escapeXML(summary) + "</text>"
	if body != "" {
		xml += "<text>" + escapeXML(body) + "</text>"
	}
	xml += "</binding></visual></toast>"

	// The XML travels inside a single-quoted PowerShell string, where '' is a
	// literal quote. escapeXML has already neutralised anything XML-significant.
	return `$ErrorActionPreference='Stop'
[void][Windows.UI.Notifications.ToastNotificationManager,Windows.UI.Notifications,ContentType=WindowsRuntime]
[void][Windows.Data.Xml.Dom.XmlDocument,Windows.Data.Xml.Dom,ContentType=WindowsRuntime]
$d=New-Object Windows.Data.Xml.Dom.XmlDocument
$d.LoadXml('` + strings.ReplaceAll(xml, "'", "''") + `')
$t=New-Object Windows.UI.Notifications.ToastNotification $d
$t.Tag='` + tag + `'
$t.Group='vito'
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('` + aumid + `').Show($t)`
}

// powershell prefers Windows PowerShell: the WinRT type accelerators used above
// work there out of the box, while PowerShell 7 needs extra assemblies.
func powershell() string {
	pwshOne.Do(func() {
		pwshBin = "powershell.exe"
		if p, err := exec.LookPath("powershell.exe"); err == nil {
			pwshBin = p
		}
	})
	return pwshBin
}

func escapeXML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return r.Replace(s)
}
