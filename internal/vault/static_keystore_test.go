package vault

import (
	"bytes"
	"errors"
	"testing"
)

func TestStaticKeystoreRoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	store := NewStaticKeystore(key)

	loaded, err := store.Load("", "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !bytes.Equal(loaded, key) {
		t.Fatalf("load returned %x, want %x", loaded, key)
	}
	// A caller mutating what it got must not change the store.
	loaded[0] = 0
	again, err := store.Load("", "")
	if err != nil {
		t.Fatalf("load again: %v", err)
	}
	if again[0] != 7 {
		t.Fatalf("the store aliased its key")
	}
}

func TestStaticKeystoreEmptyRefuses(t *testing.T) {
	store := NewStaticKeystore(nil)
	if _, err := store.Load("", ""); !errors.Is(err, ErrKeystoreNotFound) {
		t.Fatalf("load: got %v, want ErrKeystoreNotFound", err)
	}
	if err := store.Store("", "", bytes.Repeat([]byte{1}, 32)); !errors.Is(err, ErrKeystoreUnavailable) {
		t.Fatalf("store: got %v, want ErrKeystoreUnavailable", err)
	}
}

func TestStaticKeystoreDelete(t *testing.T) {
	store := NewStaticKeystore(bytes.Repeat([]byte{3}, 32))
	if err := store.Delete("", ""); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.Load("", ""); !errors.Is(err, ErrKeystoreNotFound) {
		t.Fatalf("load after delete: got %v, want ErrKeystoreNotFound", err)
	}
}
