//go:build android && cgo

// Package main is the C ABI the Android app loads. The Android build compiles
// it with -buildmode=c-shared into libmotty.so; jni_android.c in this package
// is part of the same shared object and adapts the byte-oriented ABI to JNI.
//
// Strings travel as bytes rather than jstring: JNI modified UTF-8 corrupts
// supplementary-plane characters, and host labels, key comments and chat text
// may contain emoji. The control plane stays JSON in, JSON out; terminal
// output and events are polled through caller-owned buffers.
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

const (
	maxMethodBytes = 256
	maxParamsBytes = 16 << 20
	maxStartBytes  = 1 << 20
	maxResultBytes = 64 << 20
)

var (
	appMu sync.Mutex
	app   *mobilecore.App
)

func errorEnvelope(code, message string) []byte {
	payload, err := json.Marshal(map[string]any{
		"ok":    false,
		"error": map[string]string{"code": code, "message": message},
	})
	if err != nil {
		return []byte(`{"ok":false,"error":{"code":"internal","message":"cannot encode the response"}}`)
	}
	return payload
}

// copyBytes copies a caller-owned buffer of length n. Ok is false for a
// negative length or a missing pointer with a nonzero length.
func copyBytes(ptr unsafe.Pointer, n C.int) ([]byte, bool) {
	if n < 0 {
		return nil, false
	}
	if n == 0 {
		return []byte{}, true
	}
	if ptr == nil {
		return nil, false
	}
	return C.GoBytes(ptr, n), true
}

// allocResult hands a byte slice to the caller through C memory. The caller
// must return it with MottyFreeBuffer.
func allocResult(payload []byte, outLen *C.int) unsafe.Pointer {
	if outLen == nil || len(payload) == 0 {
		return nil
	}
	buffer := C.CBytes(payload)
	if buffer == nil {
		return nil
	}
	*outLen = C.int(len(payload))
	return buffer
}

// MottyStartAlloc opens the data directory. dataDir, version and vaultKey are
// UTF-8 bytes; vaultKey is the 32-byte data key the Android Keystore already
// unwrapped. Calling it twice returns the running core instead of opening a
// second one.
//
//export MottyStartAlloc
func MottyStartAlloc(dataDir unsafe.Pointer, dataDirLen C.int,
	version unsafe.Pointer, versionLen C.int,
	vaultKey unsafe.Pointer, vaultKeyLen C.int,
	outLen *C.int) unsafe.Pointer {
	dirBytes, ok := copyBytes(dataDir, dataDirLen)
	if !ok || len(dirBytes) == 0 || len(dirBytes) > maxStartBytes {
		return allocResult(errorEnvelope("bad_request", "invalid data directory"), outLen)
	}
	versionBytes, ok := copyBytes(version, versionLen)
	if !ok || len(versionBytes) > maxStartBytes {
		return allocResult(errorEnvelope("bad_request", "invalid version"), outLen)
	}
	keyBytes, ok := copyBytes(vaultKey, vaultKeyLen)
	if !ok || len(keyBytes) > maxStartBytes {
		return allocResult(errorEnvelope("bad_request", "invalid vault key"), outLen)
	}

	appMu.Lock()
	defer appMu.Unlock()
	if app != nil {
		return allocResult([]byte(`{"ok":true,"result":{"reused":true}}`), outLen)
	}
	started, err := mobilecore.Start(mobilecore.Config{
		DataDir:    string(dirBytes),
		Version:    string(versionBytes),
		LocalShell: true,
		VaultKey:   keyBytes,
	})
	if err != nil {
		return allocResult(errorEnvelope("internal", err.Error()), outLen)
	}
	app = started
	return allocResult([]byte(`{"ok":true,"result":{"reused":false}}`), outLen)
}

// MottyCallAlloc runs one control method and returns the JSON envelope. The
// result is a fresh allocation so a non-idempotent method (sessions.open,
// hosts.save, ai.send) is never executed twice; the caller frees it with
// MottyFreeBuffer.
//
//export MottyCallAlloc
func MottyCallAlloc(method unsafe.Pointer, methodLen C.int,
	params unsafe.Pointer, paramsLen C.int,
	outLen *C.int) unsafe.Pointer {
	methodBytes, ok := copyBytes(method, methodLen)
	if !ok || len(methodBytes) == 0 || len(methodBytes) > maxMethodBytes {
		return allocResult(errorEnvelope("bad_request", "invalid method"), outLen)
	}
	paramsBytes, ok := copyBytes(params, paramsLen)
	if !ok || len(paramsBytes) > maxParamsBytes {
		return allocResult(errorEnvelope("bad_request", "invalid parameters"), outLen)
	}

	appMu.Lock()
	current := app
	appMu.Unlock()
	if current == nil {
		return allocResult(errorEnvelope("unavailable", "MottyStartAlloc has not run"), outLen)
	}

	var raw json.RawMessage
	if len(paramsBytes) > 0 {
		raw = json.RawMessage(paramsBytes)
	}
	result := current.Call(string(methodBytes), raw)
	if len(result) > maxResultBytes {
		return allocResult(errorEnvelope("internal", "the response is too large"), outLen)
	}
	return allocResult(result, outLen)
}

// MottyFreeBuffer releases a buffer returned by MottyStartAlloc or
// MottyCallAlloc.
//
//export MottyFreeBuffer
func MottyFreeBuffer(ptr unsafe.Pointer) {
	if ptr != nil {
		C.free(ptr)
	}
}

// MottyPollEventBytes copies the oldest event into buf. It returns the byte
// count, 0 when no event is waiting, or -needed when the buffer is too small.
//
//export MottyPollEventBytes
func MottyPollEventBytes(buf unsafe.Pointer, capacity C.int) C.int {
	appMu.Lock()
	current := app
	appMu.Unlock()
	if current == nil || buf == nil || capacity <= 0 {
		return 0
	}
	dst := unsafe.Slice((*byte)(buf), int(capacity))
	return C.int(current.PollEvent(dst))
}

// MottyScreenSnapshotBytes writes the terminal screen into buf. Pass the
// version last drawn as sinceVersion; 0 always writes. It returns the byte
// count, 0 when nothing changed, -needed when the buffer is too small, and -1
// for an unknown session.
//
//export MottyScreenSnapshotBytes
func MottyScreenSnapshotBytes(sessionID C.uint, sinceVersion C.ulonglong,
	buf unsafe.Pointer, capacity C.int) C.int {
	appMu.Lock()
	current := app
	appMu.Unlock()
	if current == nil || buf == nil || capacity <= 0 {
		return 0
	}
	dst := unsafe.Slice((*byte)(buf), int(capacity))
	return C.int(current.ScreenSnapshot(uint32(sessionID), uint64(sinceVersion), dst))
}

// MottyDesktopFrameBytes writes a remote desktop's changed rectangles into
// buf. It returns the byte count, 0 when nothing changed, -needed when the
// buffer is too small, and -1 for an unknown desktop.
//
//export MottyDesktopFrameBytes
func MottyDesktopFrameBytes(desktopID C.uint, sinceVersion C.ulonglong,
	buf unsafe.Pointer, capacity C.int) C.int {
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
