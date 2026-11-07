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

	"motty/internal/sshconn"
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

// A host carries three kinds of identity: this app's vault reference, the
// desktop's key fingerprint (a synced host) and the file an SSH config named.
// The key store has to turn the last two into a vault reference, or the
// connection has no key to offer.
func TestKeyStoreResolvesHostIdentities(t *testing.T) {
	app, err := Start(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	info, err := app.keys.importKey("id_ed25519", testPrivateKey(t, ""), false)
	if err != nil {
		t.Fatal(err)
	}
	if info.Fingerprint == "" {
		t.Fatal("the imported key has no fingerprint")
	}
	if got := app.keys.resolveIdentity(info.Fingerprint); got != info.Path {
		t.Fatalf("fingerprint resolved to %q, want %q", got, info.Path)
	}
	if got := app.keys.resolveIdentity("/Users/me/.ssh/id_ed25519"); got != info.Path {
		t.Fatalf("config path resolved to %q, want %q", got, info.Path)
	}
	if got := app.keys.resolveIdentity("SHA256:nothing"); got != "SHA256:nothing" {
		t.Fatalf("an unknown identity changed to %q", got)
	}

	// A host synced from the desktop names the key by fingerprint and uses the
	// private-key policy: the client must be handed the vault reference.
	synced, err := app.hosts.save(Host{
		Label: "synced", Address: "synced.example.com", User: "root",
		Auth: AuthKey, Identity: info.Fingerprint,
	})
	if err != nil {
		t.Fatal(err)
	}
	config, err := (&session{host: synced, manager: app.sessions}).dialConfig()
	if err != nil {
		t.Fatal(err)
	}
	policy := config.Authentication[sshconn.Endpoint{Host: synced.Address, Port: synced.Port, User: synced.User}]
	if len(policy.IdentityFiles) != 1 || policy.IdentityFiles[0] != info.Path {
		t.Fatalf("key policy identities = %v, want [%s]", policy.IdentityFiles, info.Path)
	}
	if len(policy.AdditionalIdentityFiles) != 0 {
		t.Fatalf("key mode also tried %v", policy.AdditionalIdentityFiles)
	}

	// A host imported from an SSH config names a file this device does not
	// have; automatic mode still tries every saved key.
	imported, err := app.hosts.save(Host{
		Label: "imported", Address: "imported.example.com", User: "root",
		Identity: "/Users/me/.ssh/id_ed25519",
	})
	if err != nil {
		t.Fatal(err)
	}
	config, err = (&session{host: imported, manager: app.sessions}).dialConfig()
	if err != nil {
		t.Fatal(err)
	}
	policy = config.Authentication[sshconn.Endpoint{Host: imported.Address, Port: imported.Port, User: imported.User}]
	if len(policy.IdentityFiles) != 1 || policy.IdentityFiles[0] != info.Path {
		t.Fatalf("automatic identities = %v, want [%s]", policy.IdentityFiles, info.Path)
	}
	if len(policy.AdditionalIdentityFiles) != 0 {
		t.Fatalf("the selected key was tried twice: %v", policy.AdditionalIdentityFiles)
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
