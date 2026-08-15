//go:build darwin

#import <CoreAudio/CoreAudio.h>
#import <CoreFoundation/CoreFoundation.h>
#include <stdlib.h>
#include <string.h>

#include "level_darwin.h"

// Element 0 is "main". Spelling it out avoids having to choose between
// kAudioObjectPropertyElementMaster (deprecated in macOS 12) and
// kAudioObjectPropertyElementMain (absent before it) for a build that has to
// work on both.
#define kVitoElementMain 0

static AudioDeviceID defaultInputDevice(void) {
    AudioObjectPropertyAddress addr = {
        kAudioHardwarePropertyDefaultInputDevice,
        kAudioObjectPropertyScopeGlobal,
        kVitoElementMain
    };
    AudioDeviceID dev = kAudioObjectUnknown;
    UInt32 size = sizeof(dev);
    if (AudioObjectGetPropertyData(kAudioObjectSystemObject, &addr, 0, NULL, &size, &dev) != noErr) {
        return kAudioObjectUnknown;
    }
    return dev;
}

// hasInputChannels reports whether a device can capture at all, so a search by
// name cannot land on an output-only device that shares its name — which is
// the normal case, since most hardware registers one device object per
// direction under the same product name.
static bool hasInputChannels(AudioDeviceID dev) {
    AudioObjectPropertyAddress addr = {
        kAudioDevicePropertyStreamConfiguration,
        kAudioDevicePropertyScopeInput,
        kVitoElementMain
    };
    UInt32 size = 0;
    if (AudioObjectGetPropertyDataSize(dev, &addr, 0, NULL, &size) != noErr || size == 0) return false;

    AudioBufferList *list = (AudioBufferList *)malloc(size);
    if (list == NULL) return false;
    bool ok = false;
    if (AudioObjectGetPropertyData(dev, &addr, 0, NULL, &size, list) == noErr) {
        for (UInt32 i = 0; i < list->mNumberBuffers; i++) {
            if (list->mBuffers[i].mNumberChannels > 0) { ok = true; break; }
        }
    }
    free(list);
    return ok;
}

static bool deviceNameMatches(AudioDeviceID dev, const char *want) {
    AudioObjectPropertyAddress addr = {
        kAudioObjectPropertyName,
        kAudioObjectPropertyScopeGlobal,
        kVitoElementMain
    };
    CFStringRef name = NULL;
    UInt32 size = sizeof(name);
    if (AudioObjectGetPropertyData(dev, &addr, 0, NULL, &size, &name) != noErr || name == NULL) {
        return false;
    }
    char buf[512];
    bool ok = CFStringGetCString(name, buf, sizeof(buf), kCFStringEncodingUTF8) &&
              strcmp(buf, want) == 0;
    CFRelease(name);
    return ok;
}

static AudioDeviceID findDevice(const char *deviceName) {
    if (deviceName == NULL || deviceName[0] == '\0') return defaultInputDevice();

    AudioObjectPropertyAddress addr = {
        kAudioHardwarePropertyDevices,
        kAudioObjectPropertyScopeGlobal,
        kVitoElementMain
    };
    UInt32 size = 0;
    if (AudioObjectGetPropertyDataSize(kAudioObjectSystemObject, &addr, 0, NULL, &size) != noErr) {
        return kAudioObjectUnknown;
    }
    UInt32 count = size / (UInt32)sizeof(AudioDeviceID);
    AudioDeviceID *devs = (AudioDeviceID *)malloc(size);
    if (devs == NULL) return kAudioObjectUnknown;

    AudioDeviceID found = kAudioObjectUnknown;
    if (AudioObjectGetPropertyData(kAudioObjectSystemObject, &addr, 0, NULL, &size, devs) == noErr) {
        for (UInt32 i = 0; i < count; i++) {
            if (hasInputChannels(devs[i]) && deviceNameMatches(devs[i], deviceName)) {
                found = devs[i];
                break;
            }
        }
    }
    free(devs);
    return found;
}

// volumeAddress fills addr for one element and reports whether the device
// actually carries that property.
static bool volumeAddress(AudioDeviceID dev, UInt32 element, AudioObjectPropertyAddress *addr) {
    addr->mSelector = kAudioDevicePropertyVolumeScalar;
    addr->mScope = kAudioDevicePropertyScopeInput;
    addr->mElement = element;
    return AudioObjectHasProperty(dev, addr);
}

bool vitoInputVolumeGet(const char *deviceName, double *out) {
    if (out == NULL) return false;
    AudioDeviceID dev = findDevice(deviceName);
    if (dev == kAudioObjectUnknown) return false;

    AudioObjectPropertyAddress addr;
    Float32 vol = 0;
    UInt32 size = sizeof(vol);

    if (volumeAddress(dev, kVitoElementMain, &addr)) {
        if (AudioObjectGetPropertyData(dev, &addr, 0, NULL, &size, &vol) != noErr) return false;
        *out = vol;
        return true;
    }
    // No main control: average the channels that do have one. Stereo inputs
    // routinely expose per-channel volume and nothing else.
    double sum = 0;
    int n = 0;
    for (UInt32 ch = 1; ch <= 2; ch++) {
        if (!volumeAddress(dev, ch, &addr)) continue;
        size = sizeof(vol);
        if (AudioObjectGetPropertyData(dev, &addr, 0, NULL, &size, &vol) == noErr) {
            sum += vol;
            n++;
        }
    }
    if (n == 0) return false;
    *out = sum / n;
    return true;
}

bool vitoInputVolumeSet(const char *deviceName, double level) {
    AudioDeviceID dev = findDevice(deviceName);
    if (dev == kAudioObjectUnknown) return false;
    if (level < 0) level = 0;
    if (level > 1) level = 1;
    Float32 vol = (Float32)level;

    AudioObjectPropertyAddress addr;
    Boolean settable = false;

    if (volumeAddress(dev, kVitoElementMain, &addr) &&
        AudioObjectIsPropertySettable(dev, &addr, &settable) == noErr && settable) {
        return AudioObjectSetPropertyData(dev, &addr, 0, NULL, sizeof(vol), &vol) == noErr;
    }
    bool any = false;
    for (UInt32 ch = 1; ch <= 2; ch++) {
        if (!volumeAddress(dev, ch, &addr)) continue;
        settable = false;
        if (AudioObjectIsPropertySettable(dev, &addr, &settable) != noErr || !settable) continue;
        if (AudioObjectSetPropertyData(dev, &addr, 0, NULL, sizeof(vol), &vol) == noErr) any = true;
    }
    return any;
}
