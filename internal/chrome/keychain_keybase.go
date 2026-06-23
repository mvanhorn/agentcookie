//go:build darwin && cgo

package chrome

/*
#cgo LDFLAGS: -framework CoreFoundation -framework Security
#include <CoreFoundation/CoreFoundation.h>
#include <Security/Security.h>

// SecKeychainSetUserInteractionAllowed is deprecated but is the only API that
// suppresses the legacy keychain ACL-confirmation dialog (the "agentcookie
// wants to use the Chrome Safe Storage key" prompt). kSecUseAuthenticationUI
// only governs LocalAuthentication UI, not this ACL gate. Silence the
// deprecation warning so the build stays clean.
#pragma clang diagnostic ignored "-Wdeprecated-declarations"
*/
import "C"

import (
	"errors"
	"fmt"
	"sync"
	"unsafe"
)

// ErrKeychainInteractionRequired means SecItemCopyMatching would have needed a
// GUI prompt to satisfy the read -- the calling binary is not in the item's
// ACL / Safe Storage partition, or the keychain is locked. Because user
// interaction is disabled for the call (SecKeychainSetUserInteractionAllowed),
// the API returns errSecInteractionNotAllowed (-25308) immediately instead of
// presenting a SecurityAgent dialog. Callers fall through to the `security` CLI
// lane, where locked-vs-missing-grant is classified (see SafeStoragePasswordFor).
var ErrKeychainInteractionRequired = errors.New("keychain read requires interaction (binary not in Safe Storage partition or keychain locked); fall back to the security CLI")

// interactionMu serializes the process-global
// SecKeychainSetUserInteractionAllowed flip so a concurrent in-process keychain
// caller never observes interaction in a half-flipped state. This package is the
// sole owner of that flag.
var interactionMu sync.Mutex

// safeStoragePasswordViaKeybaseFor reads Chrome Safe Storage via the modern
// SecItem API path. Used as the no-prompt fast path on darwin+CGO builds;
// callers fall back to the `security` CLI on any error.
//
// User interaction is disabled around the call via
// SecKeychainSetUserInteractionAllowed(false), so SecItemCopyMatching never
// opens the keychain ACL-confirmation dialog: when the binary is in the item's
// trust list / Safe Storage partition the read succeeds without a prompt, and
// otherwise it returns errSecInteractionNotAllowed (-25308) right away. That is
// what lets the call be synchronous -- there is no GUI prompt to hang on, so
// there is no goroutine to abandon and no orphaned dialog to leak (the bug
// behind the #106 prompt storm on signed binaries that lost their grant). The
// historical goroutine + timeout dance is gone by construction.
//
// kSecUseAuthenticationUIFail is set as well to cover the (unused here)
// LocalAuthentication path; the interaction toggle is what suppresses the
// legacy ACL dialog this code actually hits.
func safeStoragePasswordViaKeybaseFor(service, account string) (string, error) {
	// Process-global toggle; serialized and restored on return so a concurrent
	// in-process keychain caller never sees it half-flipped. This package is the
	// sole owner of the flag, so restoring to enabled (the macOS default) is
	// correct.
	interactionMu.Lock()
	C.SecKeychainSetUserInteractionAllowed(C.Boolean(0))
	defer func() {
		C.SecKeychainSetUserInteractionAllowed(C.Boolean(1))
		interactionMu.Unlock()
	}()

	// CFStringCreateWithBytes / CFDictionaryCreate return NULL on allocation
	// failure; CFRelease(NULL) is undefined behavior, so guard before deferring.
	svc := cfString(service)
	if svc == 0 {
		return "", errors.New("CFStringCreateWithBytes returned NULL for service")
	}
	defer C.CFRelease(C.CFTypeRef(svc))
	acc := cfString(account)
	if acc == 0 {
		return "", errors.New("CFStringCreateWithBytes returned NULL for account")
	}
	defer C.CFRelease(C.CFTypeRef(acc))

	keys := []C.CFTypeRef{
		C.CFTypeRef(C.kSecClass),
		C.CFTypeRef(C.kSecAttrService),
		C.CFTypeRef(C.kSecAttrAccount),
		C.CFTypeRef(C.kSecMatchLimit),
		C.CFTypeRef(C.kSecReturnData),
		C.CFTypeRef(C.kSecUseAuthenticationUI),
	}
	vals := []C.CFTypeRef{
		C.CFTypeRef(C.kSecClassGenericPassword),
		C.CFTypeRef(svc),
		C.CFTypeRef(acc),
		C.CFTypeRef(C.kSecMatchLimitOne),
		C.CFTypeRef(C.kCFBooleanTrue),
		C.CFTypeRef(C.kSecUseAuthenticationUIFail),
	}
	query := C.CFDictionaryCreate(
		C.kCFAllocatorDefault,
		(*unsafe.Pointer)(unsafe.Pointer(&keys[0])),
		(*unsafe.Pointer)(unsafe.Pointer(&vals[0])),
		C.CFIndex(len(keys)),
		&C.kCFTypeDictionaryKeyCallBacks,
		&C.kCFTypeDictionaryValueCallBacks,
	)
	if query == 0 {
		return "", errors.New("CFDictionaryCreate returned NULL")
	}
	defer C.CFRelease(C.CFTypeRef(query))

	var result C.CFTypeRef
	status := C.SecItemCopyMatching(C.CFDictionaryRef(query), &result)
	if status == C.errSecInteractionNotAllowed {
		return "", ErrKeychainInteractionRequired
	}
	if status != C.errSecSuccess {
		return "", fmt.Errorf("SecItemCopyMatching: OSStatus %d", int(status))
	}
	defer C.CFRelease(result)

	data := C.CFDataRef(result)
	length := C.CFDataGetLength(data)
	if length == 0 {
		return "", nil
	}
	return string(C.GoBytes(unsafe.Pointer(C.CFDataGetBytePtr(data)), C.int(length))), nil
}

// cfString builds an immutable CFStringRef from a Go string (UTF-8). The caller
// owns the result and must CFRelease it.
func cfString(s string) C.CFStringRef {
	b := []byte(s)
	var p *C.UInt8
	if len(b) > 0 {
		p = (*C.UInt8)(unsafe.Pointer(&b[0]))
	}
	return C.CFStringCreateWithBytes(C.kCFAllocatorDefault, p, C.CFIndex(len(b)), C.kCFStringEncodingUTF8, C.Boolean(0))
}

// keychainDefaultLocked reports whether the default (login) keychain is locked.
// Chrome Safe Storage lives in the login keychain, so this distinguishes a
// transient locked keychain (retry) from a permanent missing grant. Querying
// status does not require unlocking and never prompts. It is consulted before
// the `security` CLI read so a locked keychain short-circuits to a transient
// error instead of hanging the CLI on an unlock prompt until the timeout.
func keychainDefaultLocked() (bool, error) {
	var kc C.SecKeychainRef
	if st := C.SecKeychainCopyDefault(&kc); st != C.errSecSuccess {
		return false, fmt.Errorf("SecKeychainCopyDefault: OSStatus %d", int(st))
	}
	defer C.CFRelease(C.CFTypeRef(kc))
	var status C.SecKeychainStatus
	if st := C.SecKeychainGetStatus(kc, &status); st != C.errSecSuccess {
		return false, fmt.Errorf("SecKeychainGetStatus: OSStatus %d", int(st))
	}
	// kSecUnlockStateStatus set => unlocked; absent => locked.
	return status&C.kSecUnlockStateStatus == 0, nil
}
