//go:build darwin && cgo && swifttray

package main

/*
#cgo LDFLAGS: -framework AppKit -framework SwiftUI -framework UserNotifications
#include <stdbool.h>
#include <stdint.h>
#include <stdlib.h>

typedef void (*orca_tray_callback)(const char *, const char *);
extern bool orca_tray_start(orca_tray_callback, const uint8_t *, intptr_t);
extern void orca_tray_update(const uint8_t *, intptr_t);
extern void orca_tray_interaction(const uint8_t *, intptr_t);
extern void orca_tray_notify(const uint8_t *, intptr_t);
extern void orca_tray_play_task_sounds(const uint8_t *, intptr_t);
extern void orca_tray_clear(const uint8_t *, intptr_t);
extern void orca_tray_show_management(void);
extern char *orca_tray_shell_settings(const char *, const char *);
extern void orcaGoTrayAction(char *, char *);

static inline void forward_tray_action(const char *action, const char *argument) {
	orcaGoTrayAction((char *)action, (char *)argument);
}

static inline bool start_tray(const uint8_t *icon, intptr_t length) {
	return orca_tray_start(forward_tray_action, icon, length);
}
*/
import "C"

import (
	"encoding/json"
	"runtime"
	"sync/atomic"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
)

var nativeTrayDesktop atomic.Pointer[desktop]

func init() { runtime.LockOSThread() }

func (d *desktop) startNativeTray() bool {
	nativeTrayDesktop.Store(d)
	if !C.start_tray((*C.uint8_t)(unsafe.Pointer(unsafe.SliceData(trayIcon))), C.intptr_t(len(trayIcon))) {
		nativeTrayDesktop.Store(nil)
		return false
	}
	return true
}

//export orcaGoTrayAction
func orcaGoTrayAction(action, argument *C.char) {
	if d := nativeTrayDesktop.Load(); d != nil {
		kind, arg := C.GoString(action), C.GoString(argument)
		go d.nativeTrayAction(kind, arg)
	}
}

func publishToSwift(value any, send func(*C.uint8_t, C.intptr_t)) {
	data, err := json.Marshal(value)
	if err != nil {
		return
	}
	application.InvokeAsync(func() {
		send((*C.uint8_t)(unsafe.Pointer(unsafe.SliceData(data))), C.intptr_t(len(data)))
	})
}

func (*desktop) sendNativeSnapshot(snapshot map[string]any) {
	publishToSwift(snapshot, func(bytes *C.uint8_t, length C.intptr_t) { C.orca_tray_update(bytes, length) })
}

func (*desktop) sendNativeInteraction(result trayInteractionResult) {
	publishToSwift(result, func(bytes *C.uint8_t, length C.intptr_t) { C.orca_tray_interaction(bytes, length) })
}

func (*desktop) sendNativeNotice(notice trayNotice) {
	publishToSwift(notice, func(bytes *C.uint8_t, length C.intptr_t) { C.orca_tray_notify(bytes, length) })
}

func (*desktop) sendNativeTaskSounds(cues []string) {
	publishToSwift(cues, func(bytes *C.uint8_t, length C.intptr_t) { C.orca_tray_play_task_sounds(bytes, length) })
}

func (*desktop) clearNativeNotices(ids []string) {
	if len(ids) > 0 {
		publishToSwift(ids, func(bytes *C.uint8_t, length C.intptr_t) { C.orca_tray_clear(bytes, length) })
	}
}

func (d *desktop) showNativeManagement() {
	if d.nativeTray && !d.quitting.Load() {
		application.InvokeAsync(func() { C.orca_tray_show_management() })
	}
}

func nativeShellSettings(action, value string) (result json.RawMessage) {
	application.InvokeSync(func() {
		command, input := C.CString(action), C.CString(value)
		defer C.free(unsafe.Pointer(command))
		defer C.free(unsafe.Pointer(input))
		reply := C.orca_tray_shell_settings(command, input)
		if reply != nil {
			defer C.free(unsafe.Pointer(reply))
			result = json.RawMessage(C.GoString(reply))
		}
	})
	return result
}
