//go:build !ios

package mobilecore

import (
	"path/filepath"

	"motty/internal/vault"
)

// platformKeystore is the key store of a system without the iOS Keychain: a
// private file beside the vault. It is what tests and a desktop test build use;
// the iOS build gets the Keychain instead.
func platformKeystore(dataDir string) vault.Keystore {
	return vault.FileKeystore{Path: filepath.Join(dataDir, "vault.key")}
}
