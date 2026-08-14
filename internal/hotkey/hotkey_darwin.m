//go:build darwin

#import <CoreFoundation/CoreFoundation.h>
#import <CoreGraphics/CoreGraphics.h>

#include "hotkey_darwin.h"
#include "_cgo_export.h"

static CFMachPortRef gTap = NULL;
static CFRunLoopSourceRef gSource = NULL;
static CFRunLoopRef gLoop = NULL;

// tapCallback must stay fast: it only forwards the key to Go, which classifies
// it and hands any real work to a worker goroutine.
static CGEventRef tapCallback(CGEventTapProxy proxy, CGEventType type,
                              CGEventRef event, void *userInfo) {
    (void)proxy;
    (void)userInfo;

    // macOS disables a tap that takes too long in its callback, and again after
    // certain user input. Re-enabling is the documented recovery; without it
    // the hotkey silently stops working until Vito restarts.
    if (type == kCGEventTapDisabledByTimeout || type == kCGEventTapDisabledByUserInput) {
        if (gTap != NULL) CGEventTapEnable(gTap, true);
        return event;
    }
    if (type != kCGEventKeyDown && type != kCGEventKeyUp) return event;

    int64_t code = CGEventGetIntegerValueField(event, kCGKeyboardEventKeycode);
    int64_t repeat = CGEventGetIntegerValueField(event, kCGKeyboardEventAutorepeat);
    CGEventFlags flags = CGEventGetFlags(event);

    if (vitoHotkeyEvent((long long)code, (unsigned long long)flags,
                        type == kCGEventKeyDown ? 1 : 0,
                        repeat != 0 ? 1 : 0)) {
        return NULL; // swallow: don't let the key reach the focused app
    }
    return event;
}

bool vitoTapCreate(void) {
    CGEventMask mask = CGEventMaskBit(kCGEventKeyDown) | CGEventMaskBit(kCGEventKeyUp);
    gTap = CGEventTapCreate(kCGSessionEventTap, kCGHeadInsertEventTap,
                            kCGEventTapOptionDefault, mask, tapCallback, NULL);
    if (gTap == NULL) return false; // no Accessibility right

    gSource = CFMachPortCreateRunLoopSource(kCFAllocatorDefault, gTap, 0);
    if (gSource == NULL) {
        CFRelease(gTap);
        gTap = NULL;
        return false;
    }
    gLoop = CFRunLoopGetCurrent();
    CFRunLoopAddSource(gLoop, gSource, kCFRunLoopCommonModes);
    CGEventTapEnable(gTap, true);
    return true;
}

void vitoTapRun(void) {
    CFRunLoopRun();
}

void vitoTapStop(void) {
    if (gTap != NULL) CGEventTapEnable(gTap, false);
    if (gLoop != NULL) {
        if (gSource != NULL) CFRunLoopRemoveSource(gLoop, gSource, kCFRunLoopCommonModes);
        CFRunLoopStop(gLoop);
        gLoop = NULL;
    }
    if (gSource != NULL) {
        CFRelease(gSource);
        gSource = NULL;
    }
    if (gTap != NULL) {
        CFRelease(gTap);
        gTap = NULL;
    }
}
