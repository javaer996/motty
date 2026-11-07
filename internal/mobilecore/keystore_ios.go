//go:build ios

package mobilecore

/*
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <Security/Security.h>
#include <stdlib.h>

static OSStatus motty_keychain_load(const char *service, const char *account, CFDataRef *out) {
	CFStringRef svc = CFStringCreateWithCString(kCFAllocatorDefault, service, kCFStringEncodingUTF8);
	CFStringRef acc = CFStringCreateWithCString(kCFAllocatorDefault, account, kCFStringEncodingUTF8);
	if (svc == NULL || acc == NULL) {
		if (svc != NULL) CFRelease(svc);
		if (acc != NULL) CFRelease(acc);
		return errSecParam;
	}
	const void *keys[] = { kSecClass, kSecAttrService, kSecAttrAccount, kSecReturnData, kSecMatchLimit };
	const void *values[] = { kSecClassGenericPassword, svc, acc, kCFBooleanTrue, kSecMatchLimitOne };
	CFDictionaryRef query = CFDictionaryCreate(kCFAllocatorDefault, keys, values, 5,
		&kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	OSStatus status = errSecParam;
	if (query != NULL) {
		status = SecItemCopyMatching(query, (CFTypeRef *)out);
		CFRelease(query);
	}
	CFRelease(svc);
	CFRelease(acc);
	return status;
}

static OSStatus motty_keychain_store(const char *service, const char *account, const void *bytes, size_t length) {
	CFStringRef svc = CFStringCreateWithCString(kCFAllocatorDefault, service, kCFStringEncodingUTF8);
	CFStringRef acc = CFStringCreateWithCString(kCFAllocatorDefault, account, kCFStringEncodingUTF8);
	CFDataRef data = CFDataCreate(kCFAllocatorDefault, (const UInt8 *)bytes, (CFIndex)length);
	if (svc == NULL || acc == NULL || data == NULL) {
		if (svc != NULL) CFRelease(svc);
		if (acc != NULL) CFRelease(acc);
		if (data != NULL) CFRelease(data);
		return errSecParam;
	}
	// One key per service and account: replace what is there.
	const void *queryKeys[] = { kSecClass, kSecAttrService, kSecAttrAccount };
	const void *queryValues[] = { kSecClassGenericPassword, svc, acc };
	CFDictionaryRef query = CFDictionaryCreate(kCFAllocatorDefault, queryKeys, queryValues, 3,
		&kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	if (query != NULL) {
		SecItemDelete(query);
		CFRelease(query);
	}
	const void *keys[] = { kSecClass, kSecAttrService, kSecAttrAccount, kSecValueData, kSecAttrAccessible };
	const void *values[] = { kSecClassGenericPassword, svc, acc, data, kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly };
	CFDictionaryRef item = CFDictionaryCreate(kCFAllocatorDefault, keys, values, 5,
		&kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	OSStatus status = errSecParam;
	if (item != NULL) {
		status = SecItemAdd(item, NULL);
		CFRelease(item);
	}
	CFRelease(svc);
	CFRelease(acc);
	CFRelease(data);
	return status;
}

static OSStatus motty_keychain_delete(const char *service, const char *account) {
	CFStringRef svc = CFStringCreateWithCString(kCFAllocatorDefault, service, kCFStringEncodingUTF8);
	CFStringRef acc = CFStringCreateWithCString(kCFAllocatorDefault, account, kCFStringEncodingUTF8);
	if (svc == NULL || acc == NULL) {
		if (svc != NULL) CFRelease(svc);
		if (acc != NULL) CFRelease(acc);
		return errSecParam;
	}
	const void *keys[] = { kSecClass, kSecAttrService, kSecAttrAccount };
	const void *values[] = { kSecClassGenericPassword, svc, acc };
	CFDictionaryRef query = CFDictionaryCreate(kCFAllocatorDefault, keys, values, 3,
		&kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	OSStatus status = errSecParam;
	if (query != NULL) {
		status = SecItemDelete(query);
		CFRelease(query);
	}
	CFRelease(svc);
	CFRelease(acc);
	return status;
}
*/
import "C"

import (
	"fmt"
	"unsafe"

	"motty/internal/vault"
)

// keychainKeystore holds the vault's data key in the iOS Keychain, where the
// system keeps it for this device only and hands it back without a prompt. It
// is marked AfterFirstUnlockThisDeviceOnly: readable whenever the app runs
// after the first unlock since boot, never restored to another device and never
// in an iCloud backup.
type keychainKeystore struct{}

func platformKeystore(string) vault.Keystore { return keychainKeystore{} }

func (keychainKeystore) Store(service, account string, secret []byte) error {
	serviceText, accountText := C.CString(service), C.CString(account)
	defer C.free(unsafe.Pointer(serviceText))
	defer C.free(unsafe.Pointer(accountText))
	var bytes unsafe.Pointer
	if len(secret) > 0 {
		bytes = C.CBytes(secret)
		defer C.free(bytes)
	}
	status := C.motty_keychain_store(serviceText, accountText, bytes, C.size_t(len(secret)))
	return keychainError(status, "save the key")
}

func (keychainKeystore) Load(service, account string) ([]byte, error) {
	serviceText, accountText := C.CString(service), C.CString(account)
	defer C.free(unsafe.Pointer(serviceText))
	defer C.free(unsafe.Pointer(accountText))
	var data C.CFDataRef
	status := C.motty_keychain_load(serviceText, accountText, &data)
	switch status {
	case C.errSecSuccess:
		defer C.CFRelease(C.CFTypeRef(data))
		length := C.CFDataGetLength(data)
		if length == 0 {
			return []byte{}, nil
		}
		pointer := C.CFDataGetBytePtr(data)
		return C.GoBytes(unsafe.Pointer(pointer), C.int(length)), nil
	case C.errSecItemNotFound:
		return nil, vault.ErrKeystoreNotFound
	case C.errSecInteractionNotAllowed, C.errSecNotAvailable:
		return nil, vault.ErrKeystoreUnavailable
	case C.errSecAuthFailed, C.errSecUserCanceled:
		return nil, vault.ErrKeystoreDenied
	default:
		return nil, fmt.Errorf("keychain: cannot read the key: OSStatus %d", int(status))
	}
}

func (keychainKeystore) Delete(service, account string) error {
	serviceText, accountText := C.CString(service), C.CString(account)
	defer C.free(unsafe.Pointer(serviceText))
	defer C.free(unsafe.Pointer(accountText))
	status := C.motty_keychain_delete(serviceText, accountText)
	if status == C.errSecItemNotFound {
		return nil
	}
	return keychainError(status, "delete the key")
}

func keychainError(status C.OSStatus, action string) error {
	if status == C.errSecSuccess {
		return nil
	}
	switch status {
	case C.errSecInteractionNotAllowed, C.errSecNotAvailable:
		return fmt.Errorf("%w: cannot %s", vault.ErrKeystoreUnavailable, action)
	case C.errSecAuthFailed, C.errSecUserCanceled:
		return fmt.Errorf("%w: cannot %s", vault.ErrKeystoreDenied, action)
	default:
		return fmt.Errorf("keychain: cannot %s: OSStatus %d", action, int(status))
	}
}
