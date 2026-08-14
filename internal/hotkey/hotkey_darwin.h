// Bridge to the CoreGraphics event tap used for global hotkeys.
// Implemented in hotkey_darwin.m; declared here so the cgo preamble stays C
// (the Go side exports a callback, which forbids definitions in its preamble).
#ifndef VITO_HOTKEY_DARWIN_H
#define VITO_HOTKEY_DARWIN_H

#include <stdbool.h>

// vitoTapCreate installs the keyboard event tap on the calling thread's run
// loop. Returns false when macOS refuses, which in practice always means the
// Accessibility right is missing. Call from the thread that will run the loop.
bool vitoTapCreate(void);

// vitoTapRun runs the calling thread's run loop and returns only after
// vitoTapStop. Call it right after a successful vitoTapCreate.
void vitoTapRun(void);

// vitoTapStop stops the run loop and tears the tap down.
void vitoTapStop(void);

#endif
