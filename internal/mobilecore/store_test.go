package mobilecore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"motty/internal/configstore"
)

// openTestData opens the shared store in a temporary directory and closes it
// when the test ends.
func openTestData(t *testing.T) *dataStore {
	t.Helper()
	return openTestDataAt(t, t.TempDir())
}

func openTestDataAt(t *testing.T, dir string) *dataStore {
	t.Helper()
	data, err := openDataStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(data.close)
	return data
}

func TestHostRecordUsesTheDesktopFormat(t *testing.T) {
	data := openTestData(t)
	store, err := openHostStore(data)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := store.save(Host{
		Label: "desk", Address: "vnc.example.com", User: "alice", Protocol: ProtocolVNC,
		Tags: []string{"office"}, Group: "work",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(saved.ID, "host_") || len(saved.ID) != len("host_")+24 {
		t.Fatalf("id = %q, want the desktop catalog form", saved.ID)
	}
	entries, _, err := data.config.Load()
	if err != nil {
		t.Fatal(err)
	}
	var payload []byte
	for _, entry := range entries {
		if entry.Kind == kindHostRecord && entry.ID == saved.ID {
			payload = entry.Payload
		}
	}
	if payload == nil {
		t.Fatal("host record missing from the configstore")
	}
	var envelope struct {
		Ord  int64           `json:"o"`
		Data json.RawMessage `json:"d"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data) == 0 {
		t.Fatal("record has no data")
	}
	var stored struct {
		ID       string   `json:"id"`
		Label    string   `json:"label"`
		Address  string   `json:"address"`
		User     string   `json:"user"`
		Port     uint16   `json:"port"`
		Protocol string   `json:"protocol"`
		Tags     []string `json:"tags"`
		Mobile   struct {
			CreatedAt int64 `json:"created_at"`
		} `json:"mobile"`
	}
	if err := json.Unmarshal(envelope.Data, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.ID != saved.ID || stored.Address != "vnc.example.com" || stored.User != "alice" {
		t.Fatalf("stored host = %+v", stored)
	}
	if stored.Port != 5900 || stored.Protocol != ProtocolVNC {
		t.Fatalf("port/protocol = %d/%q, want 5900/vnc", stored.Port, stored.Protocol)
	}
	if len(stored.Tags) != 1 || stored.Tags[0] != "office" {
		t.Fatalf("tags = %v", stored.Tags)
	}
	if stored.Mobile.CreatedAt == 0 {
		t.Fatal("created_at was not kept")
	}
}

func TestHostSaveKeepsFieldsItDoesNotKnow(t *testing.T) {
	data := openTestData(t)
	payload := []byte(`{"o":7,"d":{"id":"host_keep","label":"old","address":"example.com","user":"root","port":22,"system":"linux","future_field":{"a":1}}}`)
	if _, err := data.config.Commit(configstore.Change{Writer: "test", Put: []configstore.Entry{
		{Kind: kindHostRecord, ID: "host_keep", Payload: payload},
	}}); err != nil {
		t.Fatal(err)
	}
	store, err := openHostStore(data)
	if err != nil {
		t.Fatal(err)
	}
	host, ok := store.get("host_keep")
	if !ok {
		t.Fatal("host not loaded")
	}
	host.Label = "new"
	if _, err := store.save(host); err != nil {
		t.Fatal(err)
	}
	entries, _, err := data.config.Load()
	if err != nil {
		t.Fatal(err)
	}
	var patched []byte
	for _, entry := range entries {
		if entry.ID == "host_keep" {
			patched = entry.Payload
		}
	}
	var dataPart struct {
		Ord  int64          `json:"o"`
		Data map[string]any `json:"d"`
	}
	if err := json.Unmarshal(patched, &dataPart); err != nil {
		t.Fatal(err)
	}
	if dataPart.Ord != 7 {
		t.Fatalf("ord = %d, want it kept at 7", dataPart.Ord)
	}
	if dataPart.Data["label"] != "new" {
		t.Fatalf("label = %v", dataPart.Data["label"])
	}
	if dataPart.Data["system"] != "linux" {
		t.Fatalf("system field was dropped: %v", dataPart.Data)
	}
	if _, ok := dataPart.Data["future_field"]; !ok {
		t.Fatalf("unknown field was dropped: %v", dataPart.Data)
	}
}

func TestLegacyFilesMigrateIntoTheSharedStore(t *testing.T) {
	dir := t.TempDir()
	keyBytes := testPrivateKey(t, "")
	keyDir := filepath.Join(dir, "keys")
	if err := os.MkdirAll(keyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(keyDir, "id_ed25519")
	if err := os.WriteFile(keyPath, keyBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	legacy := []Host{{
		ID: "old-random-id", Label: "web", Address: "example.com", User: "root", Port: 22,
		Identity: keyPath, ProxyJump: "bastion", CreatedAt: 11, UpdatedAt: 12,
	}}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	hostsPath := filepath.Join(dir, "hosts.json")
	if err := os.WriteFile(hostsPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ai.json"), []byte(`{"provider":"openai","apiKey":"sk-old","model":"m"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	data := openTestDataAt(t, dir)
	keys, err := openKeyStore(data)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := keys.migrateLegacyFiles()
	if err != nil {
		t.Fatal(err)
	}
	ref, ok := refs[keyPath]
	if !ok || !strings.HasPrefix(ref, "sshkey:") {
		t.Fatalf("refs = %v", refs)
	}
	keyData, err := keys.keyData(ref)
	if err != nil {
		t.Fatal(err)
	}
	if string(keyData) != string(keyBytes) {
		t.Fatal("vault key differs from the file")
	}

	imported, err := migrateLegacyHosts(data, refs)
	if err != nil {
		t.Fatal(err)
	}
	if imported != 1 {
		t.Fatalf("imported %d hosts, want 1", imported)
	}
	hosts, err := openHostStore(data)
	if err != nil {
		t.Fatal(err)
	}
	wantID := catalogIDFor(Host{Address: "example.com", User: "root", Port: 22})
	host, ok := hosts.get(wantID)
	if !ok {
		t.Fatalf("migrated host missing; have %+v", hosts.list())
	}
	if host.Identity != ref {
		t.Fatalf("identity = %q, want %q", host.Identity, ref)
	}
	if host.ProxyJump != "bastion" {
		t.Fatalf("proxy jump = %q", host.ProxyJump)
	}
	if host.CreatedAt != 11 {
		t.Fatalf("created at = %d, want 11", host.CreatedAt)
	}

	// The old files stay, and a second start does not import anything again.
	if _, err := os.Stat(hostsPath); err != nil {
		t.Fatalf("the old catalog was removed: %v", err)
	}
	again, err := migrateLegacyHosts(data, refs)
	if err != nil || again != 0 {
		t.Fatalf("second migration imported %d hosts: %v", again, err)
	}
	refsAgain, err := keys.migrateLegacyFiles()
	if err != nil {
		t.Fatal(err)
	}
	if refsAgain[keyPath] != ref {
		t.Fatalf("second key migration chose %q, want %q", refsAgain[keyPath], ref)
	}
	if keys, _ := keys.vaultRefForName("id_ed25519"); keys != ref {
		t.Fatalf("key was imported twice: %q vs %q", keys, ref)
	}

	// The assistant key moved out of ai.json and into the vault.
	settings := openAISettingsStore(data)
	if current, _ := settings.get().current(); current.APIKey != "sk-old" {
		t.Fatalf("assistant key = %q", current.APIKey)
	}
	file, err := os.ReadFile(filepath.Join(dir, "ai.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(file), "sk-old") {
		t.Fatalf("ai.json still holds the API key: %s", file)
	}
	if stored, found, err := data.secretGet(aiKeySecret("openai")); err != nil || !found || stored != "sk-old" {
		t.Fatalf("vault key = %q, %v, %v", stored, found, err)
	}
}

// Early catalogs sometimes kept the key path relative to the data directory;
// the migration has to recognize those too, or the host loses its key.
func TestLegacyHostIdentityRelativePathMigrates(t *testing.T) {
	for _, identity := range []string{"keys/id_ed25519", "id_ed25519"} {
		t.Run(identity, func(t *testing.T) {
			dir := t.TempDir()
			keyDir := filepath.Join(dir, "keys")
			if err := os.MkdirAll(keyDir, 0o700); err != nil {
				t.Fatal(err)
			}
			keyPath := filepath.Join(keyDir, "id_ed25519")
			if err := os.WriteFile(keyPath, testPrivateKey(t, ""), 0o600); err != nil {
				t.Fatal(err)
			}
			catalog := []Host{{ID: "old", Label: "web", Address: "example.com", User: "root", Identity: identity}}
			raw, err := json.Marshal(catalog)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "hosts.json"), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			data := openTestDataAt(t, dir)
			keys, err := openKeyStore(data)
			if err != nil {
				t.Fatal(err)
			}
			refs, err := keys.migrateLegacyFiles()
			if err != nil {
				t.Fatal(err)
			}
			ref := refs[keyPath]
			if ref == "" {
				t.Fatalf("the key was not migrated: %v", refs)
			}
			if _, err := migrateLegacyHosts(data, refs); err != nil {
				t.Fatal(err)
			}
			hosts, err := openHostStore(data)
			if err != nil {
				t.Fatal(err)
			}
			host, ok := hosts.get(catalogIDFor(Host{Address: "example.com", User: "root", Port: 22}))
			if !ok {
				t.Fatalf("migrated host missing: %+v", hosts.list())
			}
			if host.Identity != ref {
				t.Fatalf("identity = %q, want %q", host.Identity, ref)
			}
		})
	}
}

func TestHostDeleteLeavesATombstone(t *testing.T) {
	data := openTestData(t)
	store, err := openHostStore(data)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := store.save(Host{Label: "gone", Address: "example.com", User: "root"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.delete(saved.ID); err != nil {
		t.Fatal(err)
	}
	records, err := data.config.Records()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, record := range records {
		if record.Kind == kindHostRecord && record.ID == saved.ID {
			found = record.Deleted
		}
	}
	if !found {
		t.Fatal("no tombstone for the deleted host")
	}
}
