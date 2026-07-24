//go:build windows

package media

import (
	"log/slog"
	"math"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows backends, both pure syscall/COM (no cgo):
//
//   - duck (default): lower each active audio session's own volume via WASAPI
//     (IAudioSessionManager2 → ISimpleAudioVolume), remembering the original so
//     Restore puts it back. Precise and unambiguous — no play/pause guessing.
//   - pause: press the system media transport key VK_MEDIA_PLAY_PAUSE. That key
//     is a toggle, so to avoid *starting* playback when nothing plays we first
//     check the default render endpoint's peak meter and only toggle when audio
//     is actually coming out. Restore toggles the same key back.
//
// Validated by compilation only, not on a running Windows machine (matching the
// rest of the untested-but-compiled Windows support in this project).

const (
	peakThreshold = 0.0001 // master peak (0..1) above which we call it "playing"
	duckFactor    = 0.2    // fraction of original volume to duck sessions down to
)

type duckedSession struct {
	pid uint32
	vol float32
}

func suppressPlatform(a Action, log *slog.Logger) any {
	switch a {
	case ActionPause:
		peak, ok := defaultRenderPeak()
		if !ok {
			log.Debug("media: could not read audio peak meter")
			return nil
		}
		if peak <= peakThreshold {
			return nil // nothing audible: leave media alone
		}
		sendMediaPlayPause()
		log.Info("paused media for dictation")
		return true
	case ActionDuck:
		if ducked := duckSessions(log); len(ducked) > 0 {
			log.Info("ducked media for dictation", "sessions", len(ducked))
			return ducked
		}
	}
	return nil
}

func restorePlatform(a Action, token any, log *slog.Logger) {
	switch a {
	case ActionPause:
		if paused, _ := token.(bool); paused {
			sendMediaPlayPause()
			log.Info("resumed media after dictation")
		}
	case ActionDuck:
		ducked, _ := token.([]duckedSession)
		restoreDuck(log, ducked)
		if len(ducked) > 0 {
			log.Info("restored media volume after dictation", "sessions", len(ducked))
		}
	}
}

// --- media transport key -------------------------------------------------

var (
	modUser32         = windows.NewLazySystemDLL("user32.dll")
	procKeybdEvent    = modUser32.NewProc("keybd_event")
	modKernel32       = windows.NewLazySystemDLL("kernel32.dll")
	procGetCurrentPID = modKernel32.NewProc("GetCurrentProcessId")
	modOle32          = windows.NewLazySystemDLL("ole32.dll")
	procCoInitialize  = modOle32.NewProc("CoInitializeEx")
	procCoUninit      = modOle32.NewProc("CoUninitialize")
	procCoCreate      = modOle32.NewProc("CoCreateInstance")
)

const (
	vkMediaPlayPause  = 0xB3
	keyeventfExtended = 0x0001
	keyeventfKeyUp    = 0x0002
	coinitMultithread = 0x0
	clsctxAll         = 0x17 // CLSCTX_ALL
	rpcEChangedMode   = 0x80010106
	sOK               = 0x0
	sFALSE            = 0x1
)

func sendMediaPlayPause() {
	procKeybdEvent.Call(vkMediaPlayPause, 0, keyeventfExtended, 0)
	procKeybdEvent.Call(vkMediaPlayPause, 0, keyeventfExtended|keyeventfKeyUp, 0)
}

// --- COM / WASAPI --------------------------------------------------------

var (
	clsidMMDeviceEnumerator = windows.GUID{Data1: 0xBCDE0395, Data2: 0xE52F, Data3: 0x467C,
		Data4: [8]byte{0x8E, 0x3D, 0xC4, 0x57, 0x92, 0x91, 0x69, 0x2E}}
	iidIMMDeviceEnumerator = windows.GUID{Data1: 0xA95664D2, Data2: 0x9614, Data3: 0x4F35,
		Data4: [8]byte{0xA7, 0x46, 0xDE, 0x8D, 0xB6, 0x36, 0x17, 0xE6}}
	iidIAudioMeterInformation = windows.GUID{Data1: 0xC02216F6, Data2: 0x8C67, Data3: 0x4B5B,
		Data4: [8]byte{0x9D, 0x00, 0xD0, 0x08, 0xE7, 0x3E, 0x00, 0x64}}
	iidIAudioSessionManager2 = windows.GUID{Data1: 0x77AA99A0, Data2: 0x1BD6, Data3: 0x484F,
		Data4: [8]byte{0x8B, 0xC7, 0x2C, 0x65, 0x4C, 0x9A, 0x9B, 0x6F}}
	iidIAudioSessionControl2 = windows.GUID{Data1: 0xBFB7FF88, Data2: 0x7239, Data3: 0x4FC9,
		Data4: [8]byte{0x8F, 0xA2, 0x07, 0xC9, 0x50, 0xBE, 0x9C, 0x6D}}
	iidISimpleAudioVolume = windows.GUID{Data1: 0x87CE5498, Data2: 0x68D6, Data3: 0x44E5,
		Data4: [8]byte{0x92, 0x15, 0x6D, 0xA4, 0x7E, 0xF8, 0x83, 0xD8}}
)

// comCall invokes vtable method idx on a COM object and returns the HRESULT.
// The object pointer is passed as the implicit first ("this") argument. COM
// interfaces are backed by system-owned memory the Go GC never moves, so
// carrying them as unsafe.Pointer (and using unsafe.Add for the vtable offset)
// keeps this vet-clean.
func comCall(this unsafe.Pointer, idx uintptr, args ...uintptr) uintptr {
	vtbl := *(*unsafe.Pointer)(this)
	fn := *(*uintptr)(unsafe.Add(vtbl, idx*unsafe.Sizeof(uintptr(0))))
	ret, _, _ := syscall.SyscallN(fn, append([]uintptr{uintptr(this)}, args...)...)
	return ret
}

func comRelease(this unsafe.Pointer) {
	if this != nil {
		comCall(this, 2) // IUnknown::Release
	}
}

// floatArg packs a float32 into a syscall argument slot. Go's Windows
// asmstdcall copies the first four integer-register args into XMM0..3, so a
// float passed this way lands in the correct XMM register for the callee.
func floatArg(f float32) uintptr { return uintptr(math.Float32bits(f)) }

// comInit initialises COM on the current (locked) OS thread and returns a
// cleanup func. ok is false if COM is unusable.
func comInit() (cleanup func(), ok bool) {
	hr, _, _ := procCoInitialize.Call(0, coinitMultithread)
	switch hr {
	case sOK, sFALSE:
		return func() { procCoUninit.Call() }, true
	case rpcEChangedMode:
		return func() {}, true // already up in another mode; don't uninit
	default:
		return func() {}, false
	}
}

// defaultRenderPeak reads the peak sample value of the default playback device.
func defaultRenderPeak() (peak float32, ok bool) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	cleanup, ok := comInit()
	if !ok {
		return 0, false
	}
	defer cleanup()

	enum := createDeviceEnumerator()
	if enum == nil {
		return 0, false
	}
	defer comRelease(enum)
	dev := defaultRenderEndpoint(enum)
	if dev == nil {
		return 0, false
	}
	defer comRelease(dev)

	// IMMDevice::Activate(IID_IAudioMeterInformation, CLSCTX_ALL, nil, &meter)
	var meter unsafe.Pointer
	if comCall(dev, 3, uintptr(unsafe.Pointer(&iidIAudioMeterInformation)),
		clsctxAll, 0, uintptr(unsafe.Pointer(&meter))) != sOK || meter == nil {
		return 0, false
	}
	defer comRelease(meter)
	// IAudioMeterInformation::GetPeakValue(&peak)
	if comCall(meter, 3, uintptr(unsafe.Pointer(&peak))) != sOK {
		return 0, false
	}
	return peak, true
}

func createDeviceEnumerator() unsafe.Pointer {
	var enum unsafe.Pointer
	hr, _, _ := procCoCreate.Call(
		uintptr(unsafe.Pointer(&clsidMMDeviceEnumerator)),
		0, clsctxAll,
		uintptr(unsafe.Pointer(&iidIMMDeviceEnumerator)),
		uintptr(unsafe.Pointer(&enum)))
	if hr != sOK {
		return nil
	}
	return enum
}

func defaultRenderEndpoint(enum unsafe.Pointer) unsafe.Pointer {
	// IMMDeviceEnumerator::GetDefaultAudioEndpoint(eRender=0, eConsole=0, &dev)
	var dev unsafe.Pointer
	if comCall(enum, 4, 0, 0, uintptr(unsafe.Pointer(&dev))) != sOK {
		return nil
	}
	return dev
}

// forEachSession walks the render sessions of the default endpoint, calling fn
// with each session's process id, whether it is actively playing, and its
// ISimpleAudioVolume (which may be nil). All COM lifetime is handled here.
func forEachSession(log *slog.Logger, fn func(pid uint32, active bool, vol unsafe.Pointer)) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	cleanup, ok := comInit()
	if !ok {
		return
	}
	defer cleanup()

	enum := createDeviceEnumerator()
	if enum == nil {
		return
	}
	defer comRelease(enum)
	dev := defaultRenderEndpoint(enum)
	if dev == nil {
		return
	}
	defer comRelease(dev)

	// IMMDevice::Activate(IID_IAudioSessionManager2, CLSCTX_ALL, nil, &mgr)
	var mgr unsafe.Pointer
	if comCall(dev, 3, uintptr(unsafe.Pointer(&iidIAudioSessionManager2)),
		clsctxAll, 0, uintptr(unsafe.Pointer(&mgr))) != sOK || mgr == nil {
		return
	}
	defer comRelease(mgr)

	// IAudioSessionManager2::GetSessionEnumerator(&senum)
	var senum unsafe.Pointer
	if comCall(mgr, 5, uintptr(unsafe.Pointer(&senum))) != sOK || senum == nil {
		return
	}
	defer comRelease(senum)

	var count int32
	if comCall(senum, 3, uintptr(unsafe.Pointer(&count))) != sOK { // GetCount
		return
	}
	for i := int32(0); i < count; i++ {
		var ctrl unsafe.Pointer
		if comCall(senum, 4, uintptr(i), uintptr(unsafe.Pointer(&ctrl))) != sOK || ctrl == nil {
			continue // GetSession
		}
		pid, systemSounds := sessionIdentity(ctrl)
		var state int32
		comCall(ctrl, 3, uintptr(unsafe.Pointer(&state))) // IAudioSessionControl::GetState
		var vol unsafe.Pointer
		comCall(ctrl, 0, uintptr(unsafe.Pointer(&iidISimpleAudioVolume)),
			uintptr(unsafe.Pointer(&vol))) // QueryInterface(ISimpleAudioVolume)

		fn(pid, state == 1 && !systemSounds, vol)

		comRelease(vol)
		comRelease(ctrl)
	}
}

// sessionIdentity returns the process id of a session and whether it is the
// system-sounds session (which we never touch).
func sessionIdentity(ctrl unsafe.Pointer) (pid uint32, systemSounds bool) {
	var ctrl2 unsafe.Pointer
	if comCall(ctrl, 0, uintptr(unsafe.Pointer(&iidIAudioSessionControl2)),
		uintptr(unsafe.Pointer(&ctrl2))) != sOK || ctrl2 == nil {
		return 0, false
	}
	defer comRelease(ctrl2)
	comCall(ctrl2, 14, uintptr(unsafe.Pointer(&pid))) // GetProcessId
	systemSounds = comCall(ctrl2, 15) == sOK          // IsSystemSoundsSession
	return pid, systemSounds
}

func duckSessions(log *slog.Logger) []duckedSession {
	ownPID, _, _ := procGetCurrentPID.Call()
	var ducked []duckedSession
	forEachSession(log, func(pid uint32, active bool, vol unsafe.Pointer) {
		if !active || vol == nil || uintptr(pid) == ownPID {
			return
		}
		var level float32
		if comCall(vol, 4, uintptr(unsafe.Pointer(&level))) != sOK || level <= 0 { // GetMasterVolume
			return
		}
		comCall(vol, 3, floatArg(level*duckFactor), 0) // SetMasterVolume(level, nil)
		ducked = append(ducked, duckedSession{pid: pid, vol: level})
	})
	return ducked
}

func restoreDuck(log *slog.Logger, ducked []duckedSession) {
	if len(ducked) == 0 {
		return
	}
	targets := make(map[uint32]float32, len(ducked))
	for _, d := range ducked {
		targets[d.pid] = d.vol
	}
	forEachSession(log, func(pid uint32, active bool, vol unsafe.Pointer) {
		if vol == nil {
			return
		}
		if level, ok := targets[pid]; ok {
			comCall(vol, 3, floatArg(level), 0) // SetMasterVolume(level, nil)
		}
	})
}
