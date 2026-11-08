package vault

import "sync"

// StaticKeystore keeps the data key the platform already unwrapped. Android
// generates the key, seals it with the Android Keystore in Kotlin, and hands
// the plain bytes to the core at startup; Go never talks to the Keystore
// itself.
//
// A store built with an empty key refuses to create one: writing a vault under
// a key the next launch cannot open would lose every saved secret. Load then
// reports ErrKeystoreNotFound and Store reports ErrKeystoreUnavailable, which
// is exactly how the vault treats a system without a secret store.
type StaticKeystore struct {
	mu  sync.Mutex
	key []byte
}

// NewStaticKeystore copies key. An empty key produces a store that cannot save.
func NewStaticKeystore(key []byte) *StaticKeystore {
	store := &StaticKeystore{}
	if len(key) > 0 {
		store.key = append([]byte(nil), key...)
	}
	return store
}

// Store replaces the in-memory key. The service and account name the
// platform's entry and mean nothing to a key that is already unwrapped.
func (store *StaticKeystore) Store(_, _ string, secret []byte) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.key) == 0 {
		return ErrKeystoreUnavailable
	}
	for i := range store.key {
		store.key[i] = 0
	}
	store.key = append([]byte(nil), secret...)
	return nil
}

// Load returns a copy of the key, so a caller cannot mutate the store.
func (store *StaticKeystore) Load(_, _ string) ([]byte, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.key) == 0 {
		return nil, ErrKeystoreNotFound
	}
	return append([]byte(nil), store.key...), nil
}

// Delete forgets the key.
func (store *StaticKeystore) Delete(_, _ string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	for i := range store.key {
		store.key[i] = 0
	}
	store.key = nil
	return nil
}
