//go:build linux

// Package notify shows desktop notifications for dictation state feedback.
//
// One notification ID is reused across calls (notify-send's replace-id), so the
// status messages update a single notification in place instead of stacking up.
//
// Routine status (Send) is marked transient — the freedesktop "transient" hint
// (notify-send -e) — so servers like DMS/GNOME still show the popup briefly but
// do NOT keep it in the notification history, which otherwise fills up with a
// "klaar" entry per dictation. Errors (SendSticky) are kept in history so the
// user can review them after the popup is gone.
package notify

import (
	"bytes"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

var (
	mu     sync.Mutex
	lastID int
)

// Send shows (or replaces) a transient vito notification: shown briefly, not
// stored in the server's notification history. Use for routine status.
// Best effort: a missing notification daemon must never break dictation.
func Send(summary, body string) { send(summary, body, true) }

// SendSticky shows a notification that IS kept in the notification history —
// for errors worth reviewing after the popup disappears.
func SendSticky(summary, body string) { send(summary, body, false) }

func send(summary, body string, transient bool) {
	mu.Lock()
	defer mu.Unlock()

	args := []string{"-a", "vito", "-p", "-t", "2500"}
	if transient {
		args = append(args, "-e") // "transient" hint: show but skip history
	}
	if lastID > 0 {
		args = append(args, "-r", strconv.Itoa(lastID))
	}
	args = append(args, summary)
	if body != "" {
		args = append(args, body)
	}
	var out bytes.Buffer
	cmd := exec.Command("notify-send", args...)
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return
	}
	if id, err := strconv.Atoi(strings.TrimSpace(out.String())); err == nil {
		lastID = id
	}
}
