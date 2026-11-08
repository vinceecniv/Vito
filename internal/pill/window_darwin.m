//go:build darwin

// The pill's panels. Built without ARC, like every .m file in Vito: whatever
// is alloc'ed or copied here is released here.
//
// AppKit may only be touched on the main thread, which the tray's NSApplication
// owns. Calls from Go arrive on other threads and are sent to the main queue:
// asynchronously where Go needs no answer, synchronously where it does.

#import <AppKit/AppKit.h>
#import <QuartzCore/QuartzCore.h>

#include "window_darwin.h"
#include "_cgo_export.h"

// onMain runs b on the main thread and waits for it.
static void onMain(dispatch_block_t b) {
    if ([NSThread isMainThread]) {
        b();
    } else {
        dispatch_sync(dispatch_get_main_queue(), b);
    }
}

// A panel that never becomes key or main: the paste the pill announces must
// land in the app the user is in.
@interface VitoPillPanel : NSPanel
@end

@implementation VitoPillPanel
- (BOOL)canBecomeKeyWindow { return NO; }
- (BOOL)canBecomeMainWindow { return NO; }
@end

// The view shows the picture as its layer's contents and passes clicks and
// drags to Go. Hover is not its business: the panel ignores the mouse except
// over the input box, which Go watches.
@interface VitoPillView : NSView {
@public
    int pillID;
    NSCursor *cursor; // not retained: the NSCursor class methods keep them
    int button;       // the one held, for its release
}
@end

@implementation VitoPillView
- (BOOL)acceptsFirstMouse:(NSEvent *)e { return YES; }
- (BOOL)mouseDownCanMoveWindow { return NO; }

- (void)updateTrackingAreas {
    [super updateTrackingAreas];
    for (NSTrackingArea *a in [[self.trackingAreas copy] autorelease]) {
        [self removeTrackingArea:a];
    }
    NSTrackingArea *a = [[NSTrackingArea alloc] initWithRect:NSZeroRect
        options:NSTrackingCursorUpdate | NSTrackingActiveAlways | NSTrackingInVisibleRect
        owner:self userInfo:nil];
    [self addTrackingArea:a];
    [a release];
}

- (void)cursorUpdate:(NSEvent *)e {
    [(cursor ? cursor : [NSCursor arrowCursor]) set];
}

// local gives the event's place from the top left, as the pill measures. The
// view is not flipped instead, which would also turn its layer's picture.
- (NSPoint)local:(NSEvent *)e {
    NSPoint p = [self convertPoint:[e locationInWindow] fromView:nil];
    return NSMakePoint(p.x, self.bounds.size.height - p.y);
}

- (void)press:(NSEvent *)e button:(int)b {
    button = b;
    NSPoint p = [self local:e];
    vitoPillPointer(pillID, 3, p.x, p.y, b); // pointerClick
}

- (void)lift:(NSEvent *)e {
    NSPoint p = [self local:e];
    vitoPillPointer(pillID, 4, p.x, p.y, button); // pointerRelease
}

- (void)drag:(NSEvent *)e {
    // deltaY grows downwards, the way the pill measures.
    vitoPillPointer(pillID, 5, [e deltaX], [e deltaY], 0); // pointerDelta
    NSPoint p = [self local:e];
    vitoPillPointer(pillID, 2, p.x, p.y, 0); // pointerMove
}

- (void)mouseDown:(NSEvent *)e { [self press:e button:0x110]; }
- (void)rightMouseDown:(NSEvent *)e { [self press:e button:0x111]; }
- (void)mouseUp:(NSEvent *)e { [self lift:e]; }
- (void)rightMouseUp:(NSEvent *)e { [self lift:e]; }
- (void)mouseDragged:(NSEvent *)e { [self drag:e]; }
- (void)rightMouseDragged:(NSEvent *)e { [self drag:e]; }
@end

static NSMutableDictionary *panels; // id -> VitoPillPanel, main thread only

static VitoPillPanel *panelFor(int wid) {
    return panels ? [panels objectForKey:@(wid)] : nil;
}

bool vitoPillAppRunning(void) {
    return NSApp != nil && [NSApp isRunning];
}

int vitoPillScreens(VitoScreen *out, int max) {
    __block int n = 0;
    onMain(^{
        for (NSScreen *s in [NSScreen screens]) {
            if (n >= max) break;
            NSRect f = [s frame], v = [s visibleFrame];
            NSNumber *num = [[s deviceDescription] objectForKey:@"NSScreenNumber"];
            out[n] = (VitoScreen){
                .display = [num unsignedIntValue],
                .x = f.origin.x, .y = f.origin.y, .w = f.size.width, .h = f.size.height,
                .vx = v.origin.x, .vy = v.origin.y, .vw = v.size.width, .vh = v.size.height,
                .scale = [s backingScaleFactor],
            };
            n++;
        }
    });
    return n;
}

// The main display's height turns CoreGraphics' coordinates (top left, y
// down) into Cocoa's (bottom left, y up); both are thread-safe to read.
static double mainHeight(void) {
    return CGDisplayBounds(CGMainDisplayID()).size.height;
}

void vitoPillMouse(double *x, double *y) {
    CGEventRef e = CGEventCreate(NULL);
    CGPoint p = e ? CGEventGetLocation(e) : CGPointZero;
    if (e) CFRelease(e);
    *x = p.x;
    *y = mainHeight() - p.y;
}

uint32_t vitoPillFront(bool *found) {
    *found = false;
    uint32_t display = 0;
    @autoreleasepool {
        NSRunningApplication *app = [[NSWorkspace sharedWorkspace] frontmostApplication];
        if (app == nil) return 0;
        pid_t pid = [app processIdentifier];
        // Bounds and owners need no Screen Recording right; only titles would.
        CFArrayRef list = CGWindowListCopyWindowInfo(
            kCGWindowListOptionOnScreenOnly | kCGWindowListExcludeDesktopElements, kCGNullWindowID);
        if (list == NULL) return 0;
        for (CFIndex i = 0; i < CFArrayGetCount(list); i++) { // front to back
            NSDictionary *w = (NSDictionary *)CFArrayGetValueAtIndex(list, i);
            if ([[w objectForKey:(id)kCGWindowOwnerPID] intValue] != pid) continue;
            if ([[w objectForKey:(id)kCGWindowLayer] intValue] != 0) continue; // menus, panels
            CGRect r;
            if (!CGRectMakeWithDictionaryRepresentation((CFDictionaryRef)[w objectForKey:(id)kCGWindowBounds], &r)) continue;
            if (r.size.width < 50 || r.size.height < 50) continue;
            *found = true;
            CGPoint c = CGPointMake(CGRectGetMidX(r), CGRectGetMidY(r));
            uint32_t n = 0;
            CGGetDisplaysWithPoint(c, 1, &display, &n);
            if (n == 0) display = 0;
            break;
        }
        CFRelease(list);
    }
    return display;
}

bool vitoPillDark(void) {
    // Written by System Settings, also by Auto when it turns dark.
    CFPreferencesAppSynchronize(kCFPreferencesAnyApplication);
    CFStringRef v = CFPreferencesCopyAppValue(CFSTR("AppleInterfaceStyle"), kCFPreferencesAnyApplication);
    if (v == NULL) return false;
    bool dark = CFGetTypeID(v) == CFStringGetTypeID() &&
                CFStringCompare(v, CFSTR("Dark"), kCFCompareCaseInsensitive) == kCFCompareEqualTo;
    CFRelease(v);
    return dark;
}

void vitoPillOpen(int wid, double x, double y, double w, double h, bool front) {
    onMain(^{
        if (panels == nil) panels = [[NSMutableDictionary alloc] init];
        VitoPillPanel *p = [[VitoPillPanel alloc] initWithContentRect:NSMakeRect(x, y, w, h)
            styleMask:NSWindowStyleMaskBorderless | NSWindowStyleMaskNonactivatingPanel
            backing:NSBackingStoreBuffered defer:NO];
        [p setReleasedWhenClosed:NO];
        [p setOpaque:NO];
        [p setBackgroundColor:[NSColor clearColor]];
        [p setHasShadow:NO];
        // A panel hides when its app deactivates, and Vito is never active.
        [p setHidesOnDeactivate:NO];
        [p setBecomesKeyOnlyIfNeeded:YES];
        [p setIgnoresMouseEvents:YES];
        [p setAnimationBehavior:NSWindowAnimationBehaviorNone];
        NSWindowCollectionBehavior cb = NSWindowCollectionBehaviorCanJoinAllSpaces |
            NSWindowCollectionBehaviorStationary | NSWindowCollectionBehaviorIgnoresCycle;
        if (front) {
            [p setLevel:NSStatusWindowLevel];
            cb |= NSWindowCollectionBehaviorFullScreenAuxiliary;
        } else {
            [p setLevel:NSFloatingWindowLevel];
        }
        [p setCollectionBehavior:cb];

        VitoPillView *v = [[VitoPillView alloc] initWithFrame:NSMakeRect(0, 0, w, h)];
        v->pillID = wid;
        [v setWantsLayer:YES];
        v.layer.contentsGravity = kCAGravityResize;
        v.layer.minificationFilter = kCAFilterTrilinear;
        [p setContentView:v];
        [v release];

        [panels setObject:p forKey:@(wid)];
        [p release];
    });
}

void vitoPillPresent(int wid, const void *pixels, int pw, int ph) {
    // Copied here, on Go's thread: the slice is Go's again once this returns.
    CFDataRef data = CFDataCreate(NULL, pixels, (CFIndex)pw * ph * 4);
    CGDataProviderRef prov = CGDataProviderCreateWithCFData(data);
    CGColorSpaceRef cs = CGColorSpaceCreateWithName(kCGColorSpaceSRGB);
    CGImageRef img = CGImageCreate(pw, ph, 8, 32, (size_t)pw * 4, cs,
        kCGImageAlphaPremultipliedFirst | kCGBitmapByteOrder32Little, prov, NULL, false,
        kCGRenderingIntentDefault);
    CGColorSpaceRelease(cs);
    CGDataProviderRelease(prov);
    CFRelease(data);
    if (img == NULL) return;

    // Go sends the next picture only once this one is up (vitoPillShown),
    // so a busy main thread is never handed a queue of stale ones.
    dispatch_async(dispatch_get_main_queue(), ^{
        VitoPillPanel *p = panelFor(wid);
        if (p == nil) {
            CGImageRelease(img);
            return;
        }
        VitoPillView *v = (VitoPillView *)[p contentView];
        [CATransaction begin];
        [CATransaction setDisableActions:YES]; // no cross-fade between frames
        v.layer.contents = (id)img;
        [CATransaction commit];
        CGImageRelease(img);
        if (![p isVisible]) [p orderFrontRegardless];
        vitoPillShown(wid);
    });
}

void vitoPillMove(int wid, double x, double y, double w, double h) {
    dispatch_async(dispatch_get_main_queue(), ^{
        VitoPillPanel *p = panelFor(wid);
        if (p) [p setFrame:NSMakeRect(x, y, w, h) display:NO];
    });
}

void vitoPillIgnoreMouse(int wid, bool ignore) {
    dispatch_async(dispatch_get_main_queue(), ^{
        VitoPillPanel *p = panelFor(wid);
        if (p) [p setIgnoresMouseEvents:ignore];
    });
}

void vitoPillCursor(int wid, int kind) {
    dispatch_async(dispatch_get_main_queue(), ^{
        VitoPillPanel *p = panelFor(wid);
        if (p == nil) return;
        NSCursor *c = [NSCursor arrowCursor];
        switch (kind) {
        case 1: c = [NSCursor pointingHandCursor]; break;
        case 2: c = [NSCursor openHandCursor]; break;
        case 3: c = [NSCursor closedHandCursor]; break;
        }
        VitoPillView *v = (VitoPillView *)[p contentView];
        v->cursor = c;
        // A window of an inactive app gets no cursor updates of its own
        // everywhere; setting it while the pointer is over it covers that.
        NSPoint m = [NSEvent mouseLocation];
        if (NSPointInRect(m, [p frame])) [c set];
    });
}

void vitoPillClose(int wid) {
    onMain(^{
        VitoPillPanel *p = panelFor(wid);
        if (p == nil) return;
        [p orderOut:nil];
        [p close];
        [panels removeObjectForKey:@(wid)];
    });
}
