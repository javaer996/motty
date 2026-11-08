//go:build android

package mobilecore

import "motty/internal/vault"

// platformKeystore is only reached when the app did not supply a key. The
// Android front end always unwraps Config.VaultKey through the Android
// Keystore before Start; an empty store refuses to create a vault under a key
// the next launch could not open.
func platformKeystore(string) vault.Keystore {
	return vault.NewStaticKeystore(nil)
}
