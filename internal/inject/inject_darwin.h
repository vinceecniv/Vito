// Bridge to the Cocoa/Quartz calls used for clipboard and synthetic keystrokes.
// Implemented in inject_darwin.m; declared here so the cgo preamble stays C.
#ifndef VITO_INJECT_DARWIN_H
#define VITO_INJECT_DARWIN_H

#include <stdbool.h>

// vitoClipboardSet writes utf8 to the general pasteboard as plain text.
bool vitoClipboardSet(const char *utf8);

// vitoClipboardGet returns the pasteboard's plain text as a malloc'd UTF-8
// string the caller must free, or NULL when it holds no plain text.
char *vitoClipboardGet(void);

// vitoPasteShortcut posts Command+V to the frontmost app.
bool vitoPasteShortcut(void);

// vitoPressReturn posts a Return keystroke.
bool vitoPressReturn(void);

// vitoTypeUnicode types utf8 as synthetic key events, layout-independent.
bool vitoTypeUnicode(const char *utf8);

// vitoAXTrusted reports whether this process holds the Accessibility right.
// When prompt is true and it does not, macOS shows its "open System Settings"
// dialog — do that only in response to a user action, never on a hot path.
bool vitoAXTrusted(bool prompt);

#endif
