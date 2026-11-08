// Bridge to the AppKit panels that show the pill on macOS. Implemented in
// window_darwin.m; declared here so the cgo preamble stays C (the Go side
// exports a callback, which forbids definitions in its preamble).
#ifndef VITO_PILL_DARWIN_H
#define VITO_PILL_DARWIN_H

#include <stdbool.h>
#include <stdint.h>

// A screen in Cocoa's global coordinates: points, origin at the bottom left of
// the main screen, y up. visible leaves out the menu bar and the Dock.
typedef struct {
    uint32_t display; // the CGDirectDisplayID
    double x, y, w, h;
    double vx, vy, vw, vh;
    double scale; // backing pixels per point
} VitoScreen;

// vitoPillAppRunning: an NSApplication runs its loop on the main thread (the
// tray's). Without one nothing sent to the main queue would ever run.
bool vitoPillAppRunning(void);

// vitoPillScreens fills out with up to max screens and returns how many.
int vitoPillScreens(VitoScreen *out, int max);

// vitoPillMouse gives the pointer in Cocoa's global coordinates.
void vitoPillMouse(double *x, double *y);

// vitoPillFront tells the screen of the frontmost app's frontmost window, and
// whether it has a window at all (*found). Returns 0 when it has none.
uint32_t vitoPillFront(bool *found);

// vitoPillDark reads whether macOS is in dark mode.
bool vitoPillDark(void);

// The panels, by the id Go gives them. front: above everything, full screen
// apps included; else one level lower, under a full screen app.
void vitoPillOpen(int wid, double x, double y, double w, double h, bool front);
// vitoPillPresent copies a premultiplied BGRA picture of pw × ph pixels and
// shows it at the panel's size.
void vitoPillPresent(int wid, const void *pixels, int pw, int ph);
void vitoPillMove(int wid, double x, double y, double w, double h);
void vitoPillIgnoreMouse(int wid, bool ignore);
// vitoPillCursor: 0 arrow, 1 pointing hand, 2 open hand, 3 closed hand.
void vitoPillCursor(int wid, int kind);
void vitoPillClose(int wid);

#endif
