//go:build darwin && !ios && cgo

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework AppKit
#include <stdbool.h>
#import <AppKit/AppKit.h>

static void orcaSetDockVisible(bool visible) {
    [NSApp setActivationPolicy:visible ? NSApplicationActivationPolicyRegular : NSApplicationActivationPolicyAccessory];
    if (visible) [NSApp activateIgnoringOtherApps:YES];
}
*/
import "C"

import "github.com/wailsapp/wails/v3/pkg/application"

func setDockVisible(visible bool) {
	application.InvokeSync(func() {
		C.orcaSetDockVisible(C.bool(visible))
	})
}
