package mobilecore

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestHostStoreRoundTrip(t *testing.T) {
	store, err := openHostStore(openTestData(t))
	if err != nil {
		t.Fatal(err)
	}
	saved, err := store.save(Host{Label: " web ", Address: "example.com", User: "root"})
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID == "" {
		t.Fatal("saved host has no id")
	}
	if saved.Label != "web" {
		t.Fatalf("label = %q, want it trimmed", saved.Label)
	}
	if saved.Port != 22 {
		t.Fatalf("port = %d, want the default 22", saved.Port)
	}
	if saved.CreatedAt == 0 || saved.UpdatedAt == 0 {
		t.Fatalf("timestamps not stamped: %+v", saved)
	}

	saved.Label = "prod"
	saved.Group = "/prod//"
	updated, err := store.save(saved)
	if err != nil {
		t.Fatal(err)
	}
	if updated.CreatedAt != saved.CreatedAt {
		t.Fatalf("CreatedAt changed on update: %d -> %d", saved.CreatedAt, updated.CreatedAt)
	}
	if updated.Group != "prod" {
		t.Fatalf("group = %q, want %q", updated.Group, "prod")
	}

	got, ok := store.get(saved.ID)
	if !ok || got.Label != "prod" {
		t.Fatalf("get returned %+v, %v", got, ok)
	}
	if hosts := store.list(); len(hosts) != 1 {
		t.Fatalf("list returned %d hosts, want 1", len(hosts))
	}

	if err := store.delete(saved.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.get(saved.ID); ok {
		t.Fatal("host survived delete")
	}
	if err := store.delete(saved.ID); err == nil {
		t.Fatal("deleting a missing host succeeded")
	}
}

func TestHostStorePersists(t *testing.T) {
	dir := t.TempDir()
	data, err := openDataStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := openHostStore(data)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := store.save(Host{
		Label: "gateway", Address: "192.0.2.1", User: "deploy", Port: 2222,
		Tags: []string{"infra"}, Identity: "/keys/id_ed25519", ProxyJump: "jump@bastion:2200",
	})
	if err != nil {
		t.Fatal(err)
	}

	data.close()
	reopened, err := openHostStore(openTestDataAt(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	got, ok := reopened.get(saved.ID)
	if !ok {
		t.Fatal("host was not persisted")
	}
	if !reflect.DeepEqual(got, saved) {
		t.Fatalf("reopened host = %+v, want %+v", got, saved)
	}
}

func TestHostStoreGroups(t *testing.T) {
	store, err := openHostStore(openTestData(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range []string{"prod/web", "prod/db", "lab", ""} {
		if _, err := store.save(Host{Label: group, Address: "example.com", User: "root", Group: group}); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"lab", "prod", "prod/db", "prod/web"}
	if got := store.groups(); !reflect.DeepEqual(got, want) {
		t.Fatalf("groups = %v, want %v", got, want)
	}
}

func TestHostNormalizeAndValidate(t *testing.T) {
	host := Host{Address: " example.com ", User: " root "}.Normalized()
	if host.Label != "example.com" {
		t.Fatalf("label = %q, want the address as fallback", host.Label)
	}
	if host.Port != 22 {
		t.Fatalf("port = %d, want 22", host.Port)
	}
	if host.Tags == nil {
		t.Fatal("tags left nil")
	}
	if err := host.Validate(); err != nil {
		t.Fatalf("valid host rejected: %v", err)
	}

	broken := Host{Label: "x", Address: "example.com", User: "root", Auth: "kerberos"}
	rpcErr, ok := broken.Validate().(*RPCError)
	if !ok || rpcErr.Code != CodeBadRequest {
		t.Fatalf("unknown auth mode returned %v, want a bad_request RPC error", broken.Validate())
	}
}

func TestImportSSHConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config")
	content := "Host web\n" +
		"  HostName 192.0.2.10\n" +
		"  User deploy\n" +
		"  Port 2222\n" +
		"\n" +
		"Host bastion\n" +
		"  HostName 192.0.2.11\n" +
		"  ProxyJump web\n"
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := openHostStore(openTestDataAt(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	imported, warnings, err := store.importSSHConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if len(imported) != 2 {
		t.Fatalf("imported %d hosts, want 2", len(imported))
	}

	byLabel := map[string]Host{}
	for _, host := range imported {
		byLabel[host.Label] = host
	}
	web, ok := byLabel["web"]
	if !ok {
		t.Fatalf("web missing from %+v", imported)
	}
	if web.Address != "192.0.2.10" || web.User != "deploy" || web.Port != 2222 {
		t.Fatalf("web = %+v", web)
	}
	bastion, ok := byLabel["bastion"]
	if !ok {
		t.Fatalf("bastion missing from %+v", imported)
	}
	if bastion.User != "root" {
		t.Fatalf("default user = %q, want root", bastion.User)
	}
	if bastion.ProxyJump != "web" {
		t.Fatalf("proxy jump = %q, want web", bastion.ProxyJump)
	}

	again, _, err := store.importSSHConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("second import added %d duplicate hosts", len(again))
	}
	if hosts := store.list(); len(hosts) != 2 {
		t.Fatalf("catalog has %d hosts after reimport, want 2", len(hosts))
	}
}
