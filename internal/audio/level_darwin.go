//go:build darwin

package audio

/*
#cgo LDFLAGS: -framework CoreAudio -framework CoreFoundation
#include <stdlib.h>
#include "level_darwin.h"
*/
import "C"

import (
	"errors"
	"unsafe"
)

// This file lets Vito read and set the real macOS input level — the microphone
// slider in System Settings → Sound → Input — for the active capture device,
// through the CoreAudio HAL.
//
// Not every device has one. Aggregate devices, and many USB interfaces that do
// their gain in hardware, expose no volume property at all; there the calls
// report an error and the UI hides the slider rather than pretending.

// LevelSupported reports whether the OS input level can be controlled here.
func LevelSupported() bool { return true }

// InputLevel returns the input volume (0..1) of the capture device named
// deviceName (empty = system default).
func InputLevel(deviceName string) (float64, error) {
	cname := C.CString(deviceName)
	defer C.free(unsafe.Pointer(cname))

	var level C.double
	if !bool(C.vitoInputVolumeGet(cname, &level)) {
		return 0, errors.New("this input device has no adjustable level")
	}
	return float64(level), nil
}

// SetInputLevel sets the input volume (0..1) of the capture device.
func SetInputLevel(deviceName string, level float64) error {
	cname := C.CString(deviceName)
	defer C.free(unsafe.Pointer(cname))

	if !bool(C.vitoInputVolumeSet(cname, C.double(level))) {
		return errors.New("this input device has no adjustable level")
	}
	return nil
}
