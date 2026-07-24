//go:build windows

package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// hideOwnConsole detaches the process from its console when Windows created that
// console just for us — i.e. we were launched from Explorer, a Start-menu
// shortcut, the autostart entry or the installer, and are the only process on
// it. Detaching makes the console window close.
//
// We use FreeConsole rather than ShowWindow(SW_HIDE): on Windows 11 the default
// console host is Windows Terminal, whose window ShowWindow can't hide (it isn't
// a classic conhost window). FreeConsole works regardless of the host — the
// window goes away once its last process detaches.
//
// Started from a terminal the console is shared with the shell (process count
// > 1), so we leave it alone: `vito status`, `vito version` and the error
// messages all still print when you run them by hand. The binary stays a
// console application on purpose so those keep their output; only the
// long-running daemon (launched from a shortcut) sheds its window here.
func hideOwnConsole() {
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")

	var pids [8]uint32
	n, _, _ := kernel32.NewProc("GetConsoleProcessList").Call(
		uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids)))
	if n != 1 {
		return // a shell (or anything else) shares this console — leave it be
	}
	kernel32.NewProc("FreeConsole").Call()
}
