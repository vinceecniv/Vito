//go:build darwin

#import <Foundation/Foundation.h>
#import <UserNotifications/UserNotifications.h>

#include "notify_darwin.h"

// UNUserNotificationCenter is only usable from a bundled app: asking for
// currentNotificationCenter without a bundle identifier raises. Everything
// here therefore checks the bundle first and does nothing when there isn't
// one, leaving the Go side to fall back to osascript.
bool vitoNotifyAvailable(void) {
    @autoreleasepool {
        return [[NSBundle mainBundle] bundleIdentifier] != nil;
    }
}

void vitoNotifyRequestAuth(void) {
    @autoreleasepool {
        if (![[NSBundle mainBundle] bundleIdentifier]) return;
        UNUserNotificationCenter *center = [UNUserNotificationCenter currentNotificationCenter];
        UNAuthorizationOptions opts = UNAuthorizationOptionAlert | UNAuthorizationOptionSound;
        [center requestAuthorizationWithOptions:opts
                              completionHandler:^(BOOL granted, NSError *error) {
            // Best effort: a refused permission must never break dictation, so
            // there is nothing to do with the result here.
            (void)granted;
            (void)error;
        }];
    }
}

void vitoNotifySend(const char *ident, const char *title, const char *body) {
    @autoreleasepool {
        if (![[NSBundle mainBundle] bundleIdentifier]) return;
        if (ident == NULL || title == NULL) return;

        NSString *nsIdent = [NSString stringWithUTF8String:ident];
        NSString *nsTitle = [NSString stringWithUTF8String:title];
        NSString *nsBody = body != NULL ? [NSString stringWithUTF8String:body] : @"";
        if (nsIdent == nil || nsTitle == nil) return;
        if (nsBody == nil) nsBody = @"";

        UNMutableNotificationContent *content = [[UNMutableNotificationContent alloc] init];
        content.title = nsTitle;
        content.body = nsBody;

        // A nil trigger delivers immediately. Reusing the identifier replaces
        // the pending/delivered request with the same id, which is how the
        // rolling status notification stays a single entry.
        UNNotificationRequest *req = [UNNotificationRequest requestWithIdentifier:nsIdent
                                                                         content:content
                                                                         trigger:nil];
        [[UNUserNotificationCenter currentNotificationCenter] addNotificationRequest:req
                                                              withCompletionHandler:^(NSError *error) {
            (void)error; // best effort
        }];
    }
}
