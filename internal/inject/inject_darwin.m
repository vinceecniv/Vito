//go:build darwin

#import <Cocoa/Cocoa.h>
#import <ApplicationServices/ApplicationServices.h>
#include <unistd.h>

#include "inject_darwin.h"

// Virtual key codes (Carbon's kVK_* values, spelled out so this file does not
// have to pull in the deprecated Carbon headers).
enum {
    kVitoKeyV      = 0x09,
    kVitoKeyReturn = 0x24,
};

bool vitoClipboardSet(const char *utf8) {
    if (utf8 == NULL) return false;
    @autoreleasepool {
        NSString *s = [NSString stringWithUTF8String:utf8];
        if (s == nil) return false;
        NSPasteboard *pb = [NSPasteboard generalPasteboard];
        [pb clearContents];
        return [pb setString:s forType:NSPasteboardTypeString] ? true : false;
    }
}

char *vitoClipboardGet(void) {
    @autoreleasepool {
        NSString *s = [[NSPasteboard generalPasteboard] stringForType:NSPasteboardTypeString];
        if (s == nil) return NULL;
        const char *utf8 = [s UTF8String];
        if (utf8 == NULL) return NULL;
        return strdup(utf8);
    }
}

// postKey sends one key down/up pair with exactly the given modifier flags.
// The flags are set explicitly on both events so a modifier the user still
// holds from the hotkey cannot leak into the synthetic combination.
static bool postKey(CGKeyCode key, CGEventFlags flags) {
    CGEventSourceRef src = CGEventSourceCreate(kCGEventSourceStateHIDSystemState);
    CGEventRef down = CGEventCreateKeyboardEvent(src, key, true);
    CGEventRef up = CGEventCreateKeyboardEvent(src, key, false);
    if (down == NULL || up == NULL) {
        if (down) CFRelease(down);
        if (up) CFRelease(up);
        if (src) CFRelease(src);
        return false;
    }
    CGEventSetFlags(down, flags);
    CGEventSetFlags(up, flags);
    CGEventPost(kCGHIDEventTap, down);
    CGEventPost(kCGHIDEventTap, up);
    CFRelease(down);
    CFRelease(up);
    if (src) CFRelease(src);
    return true;
}

bool vitoPasteShortcut(void) {
    return postKey(kVitoKeyV, kCGEventFlagMaskCommand);
}

bool vitoPressReturn(void) {
    return postKey(kVitoKeyReturn, 0);
}

bool vitoTypeUnicode(const char *utf8) {
    if (utf8 == NULL) return false;
    @autoreleasepool {
        NSString *s = [NSString stringWithUTF8String:utf8];
        if (s == nil) return false;

        CGEventSourceRef src = CGEventSourceCreate(kCGEventSourceStateHIDSystemState);
        NSUInteger len = [s length]; // UTF-16 units
        // CGEventKeyboardSetUnicodeString takes a short string per event; a
        // small chunk keeps well inside what the event queue accepts.
        enum { maxChunk = 16 };
        UniChar buf[maxChunk];

        for (NSUInteger i = 0; i < len; ) {
            NSUInteger n = MIN(maxChunk, len - i);
            [s getCharacters:buf range:NSMakeRange(i, n)];
            // Never split a surrogate pair across two events: that would type
            // two replacement characters instead of one emoji.
            if (n > 1 && buf[n - 1] >= 0xD800 && buf[n - 1] <= 0xDBFF) {
                n--;
            }
            CGEventRef down = CGEventCreateKeyboardEvent(src, 0, true);
            CGEventRef up = CGEventCreateKeyboardEvent(src, 0, false);
            if (down == NULL || up == NULL) {
                if (down) CFRelease(down);
                if (up) CFRelease(up);
                if (src) CFRelease(src);
                return false;
            }
            CGEventKeyboardSetUnicodeString(down, n, buf);
            CGEventKeyboardSetUnicodeString(up, n, buf);
            CGEventPost(kCGHIDEventTap, down);
            CGEventPost(kCGHIDEventTap, up);
            CFRelease(down);
            CFRelease(up);
            // Without a small gap the target app drops characters from a fast
            // burst; 1.5ms per chunk is invisible but keeps the text intact.
            usleep(1500);
            i += n;
        }
        if (src) CFRelease(src);
        return true;
    }
}

bool vitoAXTrusted(bool prompt) {
    CFStringRef keys[] = { kAXTrustedCheckOptionPrompt };
    CFBooleanRef values[] = { prompt ? kCFBooleanTrue : kCFBooleanFalse };
    CFDictionaryRef options = CFDictionaryCreate(
        kCFAllocatorDefault,
        (const void **)keys, (const void **)values, 1,
        &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
    bool trusted = AXIsProcessTrustedWithOptions(options) ? true : false;
    CFRelease(options);
    return trusted;
}
