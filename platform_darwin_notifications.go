//go:build darwin && !ios && cgo

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Foundation -framework UserNotifications
#import <Foundation/Foundation.h>
#import <UserNotifications/UserNotifications.h>
#include <stdlib.h>

@interface OrcaNotificationDelegate : NSObject <UNUserNotificationCenterDelegate>
@end

@implementation OrcaNotificationDelegate
- (void)userNotificationCenter:(UNUserNotificationCenter *)center
       willPresentNotification:(UNNotification *)notification
         withCompletionHandler:(void (^)(UNNotificationPresentationOptions))complete {
	complete(UNNotificationPresentationOptionBanner | UNNotificationPresentationOptionList | UNNotificationPresentationOptionSound);
}
@end

static OrcaNotificationDelegate *orcaNotificationDelegate;

static void orcaNotifyDesktop(const char *message, size_t length) {
	@autoreleasepool {
		NSBundle *bundle = [NSBundle mainBundle];
		if (!bundle.bundleIdentifier || ![bundle.bundlePath.pathExtension isEqualToString:@"app"]) {
			return;
		}
		NSString *body = [[[NSString alloc] initWithBytes:message length:length encoding:NSUTF8StringEncoding] autorelease];
		if (!body.length) {
			return;
		}
		dispatch_async(dispatch_get_main_queue(), ^{
			UNUserNotificationCenter *center = [UNUserNotificationCenter currentNotificationCenter];
			if (!center.delegate) {
				if (!orcaNotificationDelegate) {
					orcaNotificationDelegate = [OrcaNotificationDelegate new];
				}
				center.delegate = orcaNotificationDelegate;
			}
			[center requestAuthorizationWithOptions:UNAuthorizationOptionAlert | UNAuthorizationOptionSound
			                      completionHandler:^(BOOL granted, NSError *error) {
				if (!granted) {
					return;
				}
				@autoreleasepool {
					UNMutableNotificationContent *content = [UNMutableNotificationContent new];
					content.title = @"Orca-Wails";
					content.body = body;
					content.sound = [UNNotificationSound defaultSound];
					UNNotificationRequest *request = [UNNotificationRequest
						requestWithIdentifier:[NSUUID UUID].UUIDString content:content trigger:nil];
					[center addNotificationRequest:request withCompletionHandler:nil];
					[content release];
				}
			}];
		});
	}
}
*/
import "C"

import "unsafe"

func notifyDesktop(message string) {
	body := C.CString(message)
	defer C.free(unsafe.Pointer(body))
	C.orcaNotifyDesktop(body, C.size_t(len(message)))
}
