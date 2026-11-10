package osfiles

import (
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Documents is the user's Documents folder (wherever it was moved, OneDrive
// included).
func Documents() string {
	if p, err := windows.KnownFolderPath(windows.FOLDERID_Documents, 0); err == nil && p != "" {
		return p
	}
	return homeDocuments()
}

// OpenFolder shows dir in Explorer.
func OpenFolder(dir string) error {
	// Explorer exits with 1 even when it worked: start it, don't wait.
	return exec.Command("explorer.exe", dir).Start()
}

var procSHFileOperation = windows.NewLazySystemDLL("shell32.dll").NewProc("SHFileOperationW")

// shFileOpStruct is SHFILEOPSTRUCTW (natural alignment on 64-bit Windows).
type shFileOpStruct struct {
	hwnd                  uintptr
	wFunc                 uint32
	pFrom                 *uint16
	pTo                   *uint16
	fFlags                uint16
	fAnyOperationsAborted int32
	hNameMappings         uintptr
	lpszProgressTitle     *uint16
}

const (
	foDelete          = 3
	fofSilent         = 0x0004
	fofNoConfirmation = 0x0010
	fofAllowUndo      = 0x0040
	fofNoErrorUI      = 0x0400
)

// Trash moves path to the Recycle Bin.
func Trash(path string) error {
	from, err := windows.UTF16FromString(path)
	if err != nil {
		return err
	}
	from = append(from, 0) // the list ends with a second NUL
	op := shFileOpStruct{
		wFunc:  foDelete,
		pFrom:  &from[0],
		fFlags: fofAllowUndo | fofNoConfirmation | fofSilent | fofNoErrorUI,
	}
	r, _, _ := procSHFileOperation.Call(uintptr(unsafe.Pointer(&op)))
	if r != 0 {
		return windows.Errno(r)
	}
	if op.fAnyOperationsAborted != 0 {
		return ErrNoTrash
	}
	return nil
}
