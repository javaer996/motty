//go:build ios && cgo

// Package main is the C ABI the iOS app links against. The Xcode project
// compiles it with -buildmode=c-archive and includes libmotty.h, which Go
// generates beside the library.
//
// The control plane is JSON in, JSON out. Terminal output is not pushed
// through callbacks: the front end polls ScreenSnapshot with the version it
// last drew, and renders only when the version moved.
package main

/*
#include <stdlib.h>
*/
import "C"

import (
	"encoding/json"
	"sync"
	"unsafe"

	"motty/internal/mobilecore"
)

var (
	appMu sync.Mutex
	app   *mobilecore.App
)

// MottyStart opens the data directory. Calling it twice returns the running
// core instead of opening a second one.
//
//export MottyStart
func MottyStart(dataDir, version *C.char) *C.char {
	appMu.Lock()
	defer appMu.Unlock()
	if app != nil {
		return C.CString(`{"ok":true,"result":{"reused":true}}`)
	}
	started, err := mobilecore.Start(mobilecore.Config{
		DataDir: C.GoString(dataDir),
		Version: C.GoString(version),
	})
	if err != nil {
		payload, _ := json.Marshal(map[string]any{
			"ok":    false,
			"error": map[string]string{"code": "internal", "message": err.Error()},
		})
		return C.CString(string(payload))
	}
	app = started
	return C.CString(`{"ok":true,"result":{"reused":false}}`)
}

// MottyCall runs one control method. The returned string must be freed with
// MottyFree.
//
//export MottyCall
func MottyCall(method, paramsJSON *C.char) *C.char {
	appMu.Lock()
	current := app
	appMu.Unlock()
	if current == nil {
		return C.CString(`{"ok":false,"error":{"code":"unavailable","message":"MottyStart has not run"}}`)
	}
	var params json.RawMessage
	if text := C.GoString(paramsJSON); text != "" {
		params = json.RawMessage(text)
	}
	return C.CString(string(current.Call(C.GoString(method), params)))
}

// MottyFree releases a string MottyStart or MottyCall returned.
//
//export MottyFree
func MottyFree(text *C.char) {
	if text != nil {
		C.free(unsafe.Pointer(text))
	}
}

// MottyPollEvent copies the oldest event into buf. It returns the byte count,
// 0 when no event is waiting, or -needed when the buffer is too small.
//
//export MottyPollEvent
func MottyPollEvent(buf unsafe.Pointer, capacity C.int) C.int {
	appMu.Lock()
	current := app
	appMu.Unlock()
	if current == nil || buf == nil || capacity <= 0 {
		return 0
	}
	dst := unsafe.Slice((*byte)(buf), int(capacity))
	return C.int(current.PollEvent(dst))
}

// MottyScreenSnapshot writes the terminal screen into buf. Pass the version
// last drawn as sinceVersion; 0 always writes. It returns the byte count, 0
// when nothing changed, or -needed when the buffer is too small. It returns
// -1 for an unknown session.
//
//export MottyScreenSnapshot
func MottyScreenSnapshot(sessionID C.uint, sinceVersion C.ulonglong, buf unsafe.Pointer, capacity C.int) C.int {
	appMu.Lock()
	current := app
	appMu.Unlock()
	if current == nil || buf == nil || capacity <= 0 {
		return 0
	}
	dst := unsafe.Slice((*byte)(buf), int(capacity))
	return C.int(current.ScreenSnapshot(uint32(sessionID), uint64(sinceVersion), dst))
}

// MottyDesktopFrame writes a remote desktop's changed rectangles into buf.
// Pass the version last drawn as sinceVersion; 0 always writes a full frame.
// It returns the byte count, 0 when nothing changed, -needed when the buffer
// is too small, and -1 for an unknown desktop. See
// mobilecore.desktopFrame.snapshot for the layout.
//
//export MottyDesktopFrame
func MottyDesktopFrame(desktopID C.uint, sinceVersion C.ulonglong, buf unsafe.Pointer, capacity C.int) C.int {
	appMu.Lock()
	current := app
	appMu.Unlock()
	if current == nil || buf == nil || capacity <= 0 {
		return 0
	}
	dst := unsafe.Slice((*byte)(buf), int(capacity))
	return C.int(current.DesktopFrame(uint32(desktopID), uint64(sinceVersion), dst))
}

// MottyStop closes sessions and releases the core.
//
//export MottyStop
func MottyStop() {
	appMu.Lock()
	current := app
	app = nil
	appMu.Unlock()
	if current != nil {
		current.Close()
	}
}

func main() {}
