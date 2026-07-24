//go:build windows

package audio

import (
	"errors"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// This file lets Vito read and set the real Windows input level (the endpoint
// volume of the microphone, i.e. the 0–100% slider in Sound settings) for the
// active capture device, via WASAPI Core Audio. It is pure syscall/COM (no
// cgo). Validated by compilation only, like the rest of the Windows support.

// LevelSupported reports whether the OS input level can be controlled here.
func LevelSupported() bool { return true }

// InputLevel returns the endpoint volume (0..1) of the capture device named
// deviceName (empty = system default).
func InputLevel(deviceName string) (float64, error) {
	return withEndpointVolume(deviceName, func(vol unsafe.Pointer) (float64, error) {
		var level float32
		if lvComCall(vol, 9, uintptr(unsafe.Pointer(&level))) != 0 { // GetMasterVolumeLevelScalar
			return 0, errors.New("GetMasterVolumeLevelScalar failed")
		}
		return float64(level), nil
	})
}

// SetInputLevel sets the endpoint volume (0..1) of the capture device.
func SetInputLevel(deviceName string, level float64) error {
	if level < 0 {
		level = 0
	} else if level > 1 {
		level = 1
	}
	_, err := withEndpointVolume(deviceName, func(vol unsafe.Pointer) (float64, error) {
		if lvComCall(vol, 7, floatArg(float32(level)), 0) != 0 { // SetMasterVolumeLevelScalar(level, nil)
			return 0, errors.New("SetMasterVolumeLevelScalar failed")
		}
		return 0, nil
	})
	return err
}

// --- COM plumbing (self-contained; mirrors the media package's helpers) -----

var (
	lvOle32          = windows.NewLazySystemDLL("ole32.dll")
	lvCoInitialize   = lvOle32.NewProc("CoInitializeEx")
	lvCoUninit       = lvOle32.NewProc("CoUninitialize")
	lvCoCreate       = lvOle32.NewProc("CoCreateInstance")
	lvPropVarClear   = lvOle32.NewProc("PropVariantClear")
	clsidMMDeviceEnu = windows.GUID{Data1: 0xBCDE0395, Data2: 0xE52F, Data3: 0x467C, Data4: [8]byte{0x8E, 0x3D, 0xC4, 0x57, 0x92, 0x91, 0x69, 0x2E}}
	iidIMMDeviceEnu  = windows.GUID{Data1: 0xA95664D2, Data2: 0x9614, Data3: 0x4F35, Data4: [8]byte{0xA7, 0x46, 0xDE, 0x8D, 0xB6, 0x36, 0x17, 0xE6}}
	iidIEndpointVol  = windows.GUID{Data1: 0x5CDF2C82, Data2: 0x841E, Data3: 0x4546, Data4: [8]byte{0x97, 0x22, 0x0C, 0xF7, 0x40, 0x78, 0x22, 0x9A}}
	// PKEY_Device_FriendlyName = fmtid a45c254e-df1c-4efd-8020-67d146a850e0, pid 14
	pkeyFriendlyName = propertyKey{fmtid: windows.GUID{Data1: 0xA45C254E, Data2: 0xDF1C, Data3: 0x4EFD, Data4: [8]byte{0x80, 0x20, 0x67, 0xD1, 0x46, 0xA8, 0x50, 0xE0}}, pid: 14}
)

const (
	eCapture          = 1
	eConsole          = 0
	deviceStateActive = 0x1
	clsctxAll         = 0x17
	stgmRead          = 0
)

type propertyKey struct {
	fmtid windows.GUID
	pid   uint32
}

func lvComCall(this unsafe.Pointer, idx uintptr, args ...uintptr) uintptr {
	vtbl := *(*unsafe.Pointer)(this)
	fn := *(*uintptr)(unsafe.Add(vtbl, idx*unsafe.Sizeof(uintptr(0))))
	ret, _, _ := syscall.SyscallN(fn, append([]uintptr{uintptr(this)}, args...)...)
	return ret
}

func lvRelease(p unsafe.Pointer) {
	if p != nil {
		lvComCall(p, 2)
	}
}

// floatArg packs a float32 into a syscall slot (asmstdcall copies it to XMM).
func floatArg(f float32) uintptr { return uintptr(mathFloat32bits(f)) }

// mathFloat32bits avoids importing math just for one call.
func mathFloat32bits(f float32) uint32 { return *(*uint32)(unsafe.Pointer(&f)) }

// withEndpointVolume finds the capture device (by friendly name, or default
// when name is empty), activates its IAudioEndpointVolume and runs fn.
func withEndpointVolume(deviceName string, fn func(vol unsafe.Pointer) (float64, error)) (float64, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	hr, _, _ := lvCoInitialize.Call(0, 0) // COINIT_MULTITHREADED
	if hr == 0 || hr == 1 {
		defer lvCoUninit.Call()
	} else if hr != 0x80010106 { // RPC_E_CHANGED_MODE is fine
		return 0, errors.New("CoInitializeEx failed")
	}

	var enum unsafe.Pointer
	if r, _, _ := lvCoCreate.Call(uintptr(unsafe.Pointer(&clsidMMDeviceEnu)), 0, clsctxAll,
		uintptr(unsafe.Pointer(&iidIMMDeviceEnu)), uintptr(unsafe.Pointer(&enum))); r != 0 || enum == nil {
		return 0, errors.New("create MMDeviceEnumerator failed")
	}
	defer lvRelease(enum)

	dev := findCaptureDevice(enum, deviceName)
	if dev == nil {
		return 0, errors.New("capture device not found")
	}
	defer lvRelease(dev)

	var vol unsafe.Pointer
	// IMMDevice::Activate(IID_IAudioEndpointVolume, CLSCTX_ALL, nil, &vol) — vtbl 3
	if lvComCall(dev, 3, uintptr(unsafe.Pointer(&iidIEndpointVol)), clsctxAll, 0, uintptr(unsafe.Pointer(&vol))) != 0 || vol == nil {
		return 0, errors.New("activate IAudioEndpointVolume failed")
	}
	defer lvRelease(vol)
	return fn(vol)
}

// findCaptureDevice returns the default capture endpoint when name is empty,
// else the active capture endpoint whose friendly name matches name.
func findCaptureDevice(enum unsafe.Pointer, name string) unsafe.Pointer {
	if name == "" {
		var dev unsafe.Pointer
		// GetDefaultAudioEndpoint(eCapture, eConsole, &dev) — vtbl 4
		if lvComCall(enum, 4, eCapture, eConsole, uintptr(unsafe.Pointer(&dev))) != 0 {
			return nil
		}
		return dev
	}
	var coll unsafe.Pointer
	// EnumAudioEndpoints(eCapture, DEVICE_STATE_ACTIVE, &coll) — vtbl 3
	if lvComCall(enum, 3, eCapture, deviceStateActive, uintptr(unsafe.Pointer(&coll))) != 0 || coll == nil {
		return nil
	}
	defer lvRelease(coll)
	var count uint32
	lvComCall(coll, 3, uintptr(unsafe.Pointer(&count))) // GetCount
	for i := uint32(0); i < count; i++ {
		var dev unsafe.Pointer
		if lvComCall(coll, 4, uintptr(i), uintptr(unsafe.Pointer(&dev))) != 0 || dev == nil { // Item
			continue
		}
		if friendlyName(dev) == name {
			return dev
		}
		lvRelease(dev)
	}
	return nil
}

// propVariant is the leading part of a PROPVARIANT (16 bytes on amd64): the
// type tag plus the union, from which we read the LPWSTR pointer for a string.
type propVariant struct {
	vt  uint16
	_   [6]byte
	ptr unsafe.Pointer // union value; VT_LPWSTR → *uint16
}

// friendlyName reads PKEY_Device_FriendlyName from a device's property store.
func friendlyName(dev unsafe.Pointer) string {
	var store unsafe.Pointer
	// OpenPropertyStore(STGM_READ, &store) — vtbl 4
	if lvComCall(dev, 4, stgmRead, uintptr(unsafe.Pointer(&store))) != 0 || store == nil {
		return ""
	}
	defer lvRelease(store)
	var pv propVariant
	// GetValue(&PKEY, &PROPVARIANT) — vtbl 5
	if lvComCall(store, 5, uintptr(unsafe.Pointer(&pkeyFriendlyName)), uintptr(unsafe.Pointer(&pv))) != 0 {
		return ""
	}
	defer lvPropVarClear.Call(uintptr(unsafe.Pointer(&pv)))
	if pv.ptr == nil {
		return ""
	}
	return windows.UTF16PtrToString((*uint16)(pv.ptr))
}
