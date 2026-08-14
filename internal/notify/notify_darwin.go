//go:build darwin

// Package notify shows desktop notifications for dictation state feedback.
//
// On macOS the notification centre is only reachable from a bundled app, so
// this has two backends. Inside Vito.app it uses UserNotifications, where one
// reused request identifier makes the routine status messages replace each
// other instead of stacking up — the same trick as notify-send's replace-id on
// Linux. Errors (SendSticky) get a fresh identifier each time so they stay
// visible in Notification Centre for review.
//
// Run as a bare binary (go run, a dev build) there is no bundle identifier and
// UserNotifications is unusable; osascript stands in. It cannot replace a
// notification in place and is attributed to Script Editor, which is fine for
// development and never happens in a released build.
package notify

/*
#cgo LDFLAGS: -framework Foundation -framework UserNotifications
#include <stdlib.h>
#include "notify_darwin.h"
*/
import "C"

import (
	"context"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

// statusIdent is reused by every routine status notification, so Notification
// Centre keeps exactly one of them rather than one per dictation.
const statusIdent = "io.github.vinceecniv.vito.status"

var (
	bundled   = bool(C.vitoNotifyAvailable())
	authOnce  sync.Once
	stickySeq atomic.Uint64
)

// Send shows (or replaces) a transient vito notification. Use for routine
// status. Best effort: a refused permission must never break dictation.
func Send(summary, body string) { send(statusIdent, summary, body) }

// SendSticky shows a notification that is kept alongside the others — for
// errors worth reviewing after the popup disappears.
func SendSticky(summary, body string) {
	id := statusIdent + ".error." + strconv.FormatUint(stickySeq.Add(1), 10)
	send(id, summary, body)
}

func send(ident, summary, body string) {
	if summary == "" {
		return
	}
	if !bundled {
		sendViaOsascript(summary, body)
		return
	}
	authOnce.Do(func() { C.vitoNotifyRequestAuth() })

	cIdent := C.CString(ident)
	cTitle := C.CString(summary)
	cBody := C.CString(body)
	defer C.free(unsafe.Pointer(cIdent))
	defer C.free(unsafe.Pointer(cTitle))
	defer C.free(unsafe.Pointer(cBody))
	C.vitoNotifySend(cIdent, cTitle, cBody)
}

// sendViaOsascript is the unbundled fallback. The text is passed as argv
// rather than spliced into the script, so quotes and backslashes in a
// dictation cannot break (or inject into) the AppleScript.
func sendViaOsascript(summary, body string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "osascript",
		"-e", "on run argv",
		"-e", "display notification (item 2 of argv) with title (item 1 of argv)",
		"-e", "end run",
		summary, body)
	_ = cmd.Run() // best effort
}
