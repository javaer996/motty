package mobilecore

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func testPrivateKey(t *testing.T, passphrase string) []byte {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var block *pem.Block
	if passphrase == "" {
		block, err = ssh.MarshalPrivateKey(private, "")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(private, "", []byte(passphrase))
	}
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(block)
}

func TestKeyStoreImportListDelete(t *testing.T) {
	store, err := openKeyStore(openTestData(t))
	if err != nil {
		t.Fatal(err)
	}
	key := testPrivateKey(t, "")
	info, err := store.importKey("id_ed25519", key, false)
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "id_ed25519" || info.Path == "" || info.Encrypted {
		t.Fatalf("imported info = %+v", info)
	}
	if info.PublicKey == "" || !strings.Contains(info.PublicKey, "ssh-ed25519") {
		t.Fatalf("public key = %q", info.PublicKey)
	}

	stored, err := store.keyData(info.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != string(key) {
		t.Fatal("stored key differs from the imported bytes")
	}
	stat, err := os.Stat(filepath.Join(store.dir, "..", "vault.json"))
	if err != nil {
		t.Fatal(err)
	}
	if mode := stat.Mode().Perm(); mode != 0o600 {
		t.Fatalf("vault mode = %o, want 600", mode)
	}

	listed := store.list()
	if len(listed) != 1 || listed[0].Name != "id_ed25519" || listed[0].PublicKey == "" {
		t.Fatalf("list = %+v", listed)
	}

	if err := store.delete("id_ed25519"); err != nil {
		t.Fatal(err)
	}
	if len(store.list()) != 0 {
		t.Fatal("key survived delete")
	}
	if err := store.delete("id_ed25519"); err == nil {
		t.Fatal("deleting a missing key succeeded")
	}
}

func TestKeyStoreEncryptedKeyNeedsPassphrase(t *testing.T) {
	store, err := openKeyStore(openTestData(t))
	if err != nil {
		t.Fatal(err)
	}
	info, err := store.importKey("locked", testPrivateKey(t, "secret"), false)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Encrypted {
		t.Fatal("encrypted key was reported as plain")
	}
	if info.PublicKey == "" {
		t.Fatal("public half of an encrypted key was not recovered")
	}
}

func TestKeyStoreRejectsBadInput(t *testing.T) {
	store, err := openKeyStore(openTestData(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.importKey("garbage", []byte("not a key"), false); err == nil {
		t.Fatal("garbage accepted as a key")
	}
	if _, err := store.importKey("../escape", testPrivateKey(t, ""), false); err == nil {
		t.Fatal("path traversal accepted")
	}
	if _, err := store.importKey("empty", nil, false); err == nil {
		t.Fatal("empty key accepted")
	}
	if _, err := store.importKey("one", testPrivateKey(t, ""), false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.importKey("one", testPrivateKey(t, ""), false); err == nil {
		t.Fatal("duplicate name accepted without overwrite")
	}
	if _, err := store.importKey("one", testPrivateKey(t, ""), true); err != nil {
		t.Fatalf("overwrite refused: %v", err)
	}
}
