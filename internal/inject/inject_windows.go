//go:build windows

package inject

import (
	"fmt"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"

	"vito/internal/config"
)

// injectPlatform implements clipboard + SendInput Ctrl+V on Windows, with
// Unicode SendInput typing and clipboard-only as alternative modes.
// NOTE: written per Win32 docs; validated on the Windows machine, not here.
func injectPlatform(cfg config.Injection, mode Mode, text string) error {
	switch mode {
	case ModeClipboardOnly:
		return setClipboardText(text)

	case ModeType:
		return sendUnicodeText(text)

	case ModePaste:
		prev, prevOK := getClipboardText()
		if err := setClipboardText(text); err != nil {
			return err
		}
		time.Sleep(time.Duration(cfg.PasteDelayMS) * time.Millisecond)
		if err := sendCtrlV(); err != nil {
			return err
		}
		if cfg.RestoreClipboard && prevOK {
			time.Sleep(time.Duration(cfg.RestoreDelayMS) * time.Millisecond)
			_ = setClipboardText(prev) // best effort
		}
		return nil
	}
	return fmt.Errorf("unknown injection mode %q", mode)
}

var (
	user32                     = windows.NewLazySystemDLL("user32.dll")
	kernel32                   = windows.NewLazySystemDLL("kernel32.dll")
	procOpenClipboard          = user32.NewProc("OpenClipboard")
	procCloseClipboard         = user32.NewProc("CloseClipboard")
	procEmptyClipboard         = user32.NewProc("EmptyClipboard")
	procSetClipboardData       = user32.NewProc("SetClipboardData")
	procGetClipboardData       = user32.NewProc("GetClipboardData")
	procIsClipboardFormatAvail = user32.NewProc("IsClipboardFormatAvailable")
	procSendInput              = user32.NewProc("SendInput")
	procGlobalAlloc            = kernel32.NewProc("GlobalAlloc")
	procGlobalLock             = kernel32.NewProc("GlobalLock")
	procGlobalUnlock           = kernel32.NewProc("GlobalUnlock")
)

const (
	cfUnicodeText    = 13
	gmemMoveable     = 0x0002
	inputKeyboard    = 1
	keyeventfKeyUp   = 0x0002
	keyeventfUnicode = 0x0004
	vkControl        = 0x11
	vkV              = 0x56
)

// keyboardInput is KEYBDINPUT; input is the 64-bit INPUT struct (40 bytes).
type keyboardInput struct {
	Vk        uint16
	Scan      uint16
	Flags     uint32
	Time      uint32
	ExtraInfo uintptr
}

type input struct {
	Type uint32
	_    uint32 // alignment padding on amd64
	Ki   keyboardInput
	_    [8]byte // pad union to MOUSEINPUT size
}

func sendInputs(inputs []input) error {
	n, _, err := procSendInput.Call(
		uintptr(len(inputs)),
		uintptr(unsafe.Pointer(&inputs[0])),
		unsafe.Sizeof(inputs[0]),
	)
	if int(n) != len(inputs) {
		return fmt.Errorf("SendInput sent %d of %d events: %v", n, len(inputs), err)
	}
	return nil
}

func sendCtrlV() error {
	key := func(vk uint16, up bool) input {
		in := input{Type: inputKeyboard}
		in.Ki.Vk = vk
		if up {
			in.Ki.Flags = keyeventfKeyUp
		}
		return in
	}
	return sendInputs([]input{
		key(vkControl, false), key(vkV, false),
		key(vkV, true), key(vkControl, true),
	})
}

// pressEnter presses Return, used to auto-submit after injection. A short delay
// lets the just-pasted text settle in the target app before it's submitted.
func pressEnter() error {
	const vkReturn = 0x0D
	time.Sleep(40 * time.Millisecond)
	key := func(vk uint16, up bool) input {
		in := input{Type: inputKeyboard}
		in.Ki.Vk = vk
		if up {
			in.Ki.Flags = keyeventfKeyUp
		}
		return in
	}
	return sendInputs([]input{key(vkReturn, false), key(vkReturn, true)})
}

// sendUnicodeText types the text via KEYEVENTF_UNICODE — layout-independent.
func sendUnicodeText(text string) error {
	units := utf16.Encode([]rune(text))
	inputs := make([]input, 0, len(units)*2)
	for _, u := range units {
		down := input{Type: inputKeyboard}
		down.Ki.Scan = u
		down.Ki.Flags = keyeventfUnicode
		up := down
		up.Ki.Flags = keyeventfUnicode | keyeventfKeyUp
		inputs = append(inputs, down, up)
	}
	// Send in batches so long texts don't overflow the input queue.
	const batch = 200
	for i := 0; i < len(inputs); i += batch {
		end := min(i+batch, len(inputs))
		if err := sendInputs(inputs[i:end]); err != nil {
			return err
		}
	}
	return nil
}

func withClipboard(fn func() error) error {
	var opened bool
	for range 10 { // clipboard can be briefly held by another process
		if r, _, _ := procOpenClipboard.Call(0); r != 0 {
			opened = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !opened {
		return fmt.Errorf("could not open clipboard")
	}
	defer procCloseClipboard.Call()
	return fn()
}

func setClipboardText(text string) error {
	units := utf16.Encode([]rune(text))
	units = append(units, 0) // NUL terminator
	return withClipboard(func() error {
		procEmptyClipboard.Call()
		size := uintptr(len(units) * 2)
		h, _, _ := procGlobalAlloc.Call(gmemMoveable, size)
		if h == 0 {
			return fmt.Errorf("GlobalAlloc failed")
		}
		p, _, _ := procGlobalLock.Call(h)
		if p == 0 {
			return fmt.Errorf("GlobalLock failed")
		}
		dst := unsafe.Slice((*uint16)(unsafe.Pointer(p)), len(units))
		copy(dst, units)
		procGlobalUnlock.Call(h)
		if r, _, _ := procSetClipboardData.Call(cfUnicodeText, h); r == 0 {
			return fmt.Errorf("SetClipboardData failed")
		}
		return nil // ownership of h transferred to the clipboard
	})
}

// ReadClipboard returns the current clipboard text (best-effort), for voice
// commands that operate on already-copied text.
func ReadClipboard() (string, bool) { return getClipboardText() }

func getClipboardText() (string, bool) {
	if r, _, _ := procIsClipboardFormatAvail.Call(cfUnicodeText); r == 0 {
		return "", false
	}
	var out string
	ok := false
	_ = withClipboard(func() error {
		h, _, _ := procGetClipboardData.Call(cfUnicodeText)
		if h == 0 {
			return nil
		}
		p, _, _ := procGlobalLock.Call(h)
		if p == 0 {
			return nil
		}
		defer procGlobalUnlock.Call(h)
		var units []uint16
		for i := 0; ; i++ {
			u := *(*uint16)(unsafe.Pointer(p + uintptr(i*2)))
			if u == 0 {
				break
			}
			units = append(units, u)
		}
		out = string(utf16.Decode(units))
		ok = true
		return nil
	})
	return out, ok
}

// adjustMode is a no-op on Windows: SendInput is always available.
func adjustMode(cfg config.Injection, mode Mode) Mode { return mode }

// ActiveBackend names the backend an injection would use. Windows has exactly
// one way to do this, so there is nothing to choose.
func ActiveBackend(cfg config.Injection) string { return "sendinput" }
