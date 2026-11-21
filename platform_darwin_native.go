//go:build darwin && !ios && cgo

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework AppKit
#include <stdbool.h>
#import <AppKit/AppKit.h>

static void orcaSetDockVisible(bool visible) {
    if (!visible) {
        for (NSWindow *window in NSApp.windows) {
            if ([window.identifier isEqualToString:@"orca-management"] && (window.isVisible || window.isMiniaturized)) return;
        }
    }
    [NSApp setActivationPolicy:visible ? NSApplicationActivationPolicyRegular : NSApplicationActivationPolicyAccessory];
    if (visible) [NSApp activateIgnoringOtherApps:YES];
}

static bool orcaTrayPrefersReducedMotion(void) {
    return [NSWorkspace sharedWorkspace].accessibilityDisplayShouldReduceMotion;
}

static NSStatusBarButton *orcaFindStatusBarButton(NSView *view) {
    if ([view isKindOfClass:[NSStatusBarButton class]]) {
        NSStatusBarButton *button = (NSStatusBarButton *)view;
        return button.image ? button : nil;
    }
    for (NSView *child in view.subviews) {
        NSStatusBarButton *button = orcaFindStatusBarButton(child);
        if (button) return button;
    }
    return nil;
}

static bool orcaTrayHasDarkBackground(void) {
    NSAppearance *appearance = NSApp.effectiveAppearance;
    for (NSWindow *window in NSApp.windows) {
        NSStatusBarButton *button = orcaFindStatusBarButton(window.contentView);
        if (button) {
            // The status bar can use a different appearance from the app window.
            appearance = button.effectiveAppearance;
            break;
        }
    }
    NSString *match = [appearance bestMatchFromAppearancesWithNames:@[NSAppearanceNameAqua, NSAppearanceNameDarkAqua]];
    return [match isEqualToString:NSAppearanceNameDarkAqua];
}

*/
import "C"

import "github.com/wailsapp/wails/v3/pkg/application"

func setDockVisible(visible bool) {
	application.InvokeSync(func() {
		C.orcaSetDockVisible(C.bool(visible))
	})
}

func readMacTrayDisplay() macTrayDisplay {
	return application.InvokeSyncWithResult(func() macTrayDisplay {
		return macTrayDisplay{
			dark:         bool(C.orcaTrayHasDarkBackground()),
			reduceMotion: bool(C.orcaTrayPrefersReducedMotion()),
		}
	})
}
