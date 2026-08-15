//go:build darwin

package inject

/*
#cgo LDFLAGS: -framework Cocoa -framework ApplicationServices
#include <stdlib.h>
#include "inject_darwin.h"
*/
import "C"

import (
	"fmt"
	"time"
	"unsafe"

	"vito/internal/config"
)

// injectPlatform implements clipboard + Command+V on macOS, with Unicode
// synthetic typing and clipboard-only as the alternative modes.
//
// Everything that puts a keystroke into another app goes through CGEventPost,
// which macOS gates behind the Accessibility right. Without it the calls
// succeed but do nothing at all, so the modes that need it check first and say
// so rather than reporting a silent success.
// injectPlatform delivers, and reports the mode it actually used. Only Linux
// can end up somewhere other than where it aimed — macOS has one way to press
// a key and it is either available or an error — so the mode passes straight
// through here.
func injectPlatform(cfg config.Injection, mode Mode, text string) (Mode, error) {
	return mode, injectHere(cfg, mode, text)
}

func injectHere(cfg config.Injection, mode Mode, text string) error {
	switch mode {
	case ModeClipboardOnly:
		return setClipboardText(text)

	case ModeType:
		if err := requireAccessibility(); err != nil {
			return err
		}
		ctext := C.CString(text)
		defer C.free(unsafe.Pointer(ctext))
		if !bool(C.vitoTypeUnicode(ctext)) {
			return fmt.Errorf("typing the text failed")
		}
		return nil

	case ModePaste:
		if err := requireAccessibility(); err != nil {
			return err
		}
		prev, prevOK := getClipboardText()
		if err := setClipboardText(text); err != nil {
			return err
		}
		time.Sleep(time.Duration(cfg.PasteDelayMS) * time.Millisecond)
		if !bool(C.vitoPasteShortcut()) {
			return fmt.Errorf("sending Command+V failed")
		}
		if cfg.RestoreClipboard && prevOK {
			time.Sleep(time.Duration(cfg.RestoreDelayMS) * time.Millisecond)
			_ = setClipboardText(prev) // best effort
		}
		return nil
	}
	return fmt.Errorf("unknown injection mode %q", mode)
}

// pressEnter presses Return, used to auto-submit after injection. A short delay
// lets the just-pasted text settle in the target app before it's submitted.
func pressEnter() error {
	if err := requireAccessibility(); err != nil {
		return err
	}
	time.Sleep(40 * time.Millisecond)
	if !bool(C.vitoPressReturn()) {
		return fmt.Errorf("sending Return failed")
	}
	return nil
}

// Accessible reports whether macOS lets this process synthesise keystrokes.
// Paste and type need it; clipboard-only does not.
func Accessible() bool { return bool(C.vitoAXTrusted(C.bool(false))) }

// RequestAccessibility asks macOS to show its "open System Settings" prompt
// when the right is missing. Call it from a user action only — the prompt
// appears once per app per login, so spending it on a background check wastes
// the one moment the user is actually looking.
func RequestAccessibility() bool { return bool(C.vitoAXTrusted(C.bool(true))) }

func requireAccessibility() error {
	if Accessible() {
		return nil
	}
	return fmt.Errorf("Vito needs Accessibility permission to type into other apps — " +
		"grant it under System Settings → Privacy & Security → Accessibility, then restart Vito")
}

func setClipboardText(text string) error {
	ctext := C.CString(text)
	defer C.free(unsafe.Pointer(ctext))
	if !bool(C.vitoClipboardSet(ctext)) {
		return fmt.Errorf("writing to the clipboard failed")
	}
	return nil
}

// ReadClipboard returns the current clipboard text (best-effort), for voice
// commands that operate on already-copied text.
func ReadClipboard() (string, bool) { return getClipboardText() }

func getClipboardText() (string, bool) {
	cstr := C.vitoClipboardGet()
	if cstr == nil {
		return "", false
	}
	defer C.free(unsafe.Pointer(cstr))
	return C.GoString(cstr), true
}
