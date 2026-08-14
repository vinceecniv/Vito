// Bridge to the macOS UserNotifications framework.
// Implemented in notify_darwin.m; declared here so the cgo preamble stays C.
#ifndef VITO_NOTIFY_DARWIN_H
#define VITO_NOTIFY_DARWIN_H

#include <stdbool.h>

// vitoNotifyAvailable reports whether the notification centre can be used at
// all: it needs a bundle identifier, so a bare binary run from the terminal
// has to fall back to osascript.
bool vitoNotifyAvailable(void);

// vitoNotifyRequestAuth asks the user for notification permission once. Safe to
// call repeatedly; macOS only shows its prompt the first time.
void vitoNotifyRequestAuth(void);

// vitoNotifySend posts a notification. ident is the request identifier: reuse
// one identifier to replace a notification in place, pass a fresh one to let it
// stack up alongside the others.
void vitoNotifySend(const char *ident, const char *title, const char *body);

#endif
