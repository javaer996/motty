package mobilecore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type testEnvelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  *RPCError       `json:"error"`
}

func callApp(t *testing.T, app *App, method, params string) testEnvelope {
	t.Helper()
	var raw json.RawMessage
	if params != "" {
		raw = json.RawMessage(params)
	}
	var envelope testEnvelope
	if err := json.Unmarshal(app.Call(method, raw), &envelope); err != nil {
		t.Fatalf("%s returned malformed JSON: %v", method, err)
	}
	return envelope
}

func TestAppHostsLifecycle(t *testing.T) {
	app, err := Start(Config{DataDir: t.TempDir(), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	saved := callApp(t, app, "hosts.save", `{"label":"web","address":"example.com","user":"root"}`)
	if !saved.OK {
		t.Fatalf("hosts.save failed: %+v", saved.Error)
	}
	var host Host
	if err := json.Unmarshal(saved.Result, &host); err != nil {
		t.Fatal(err)
	}

	listed := callApp(t, app, "hosts.list", "")
	if !listed.OK {
		t.Fatalf("hosts.list failed: %+v", listed.Error)
	}
	var list struct {
		Hosts []Host `json:"hosts"`
	}
	if err := json.Unmarshal(listed.Result, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Hosts) != 1 || list.Hosts[0].ID != host.ID {
		t.Fatalf("hosts.list returned %+v", list.Hosts)
	}

	groups := callApp(t, app, "hosts.groups", "")
	if !groups.OK {
		t.Fatalf("hosts.groups failed: %+v", groups.Error)
	}

	deleted := callApp(t, app, "hosts.delete", `{"id":"`+host.ID+`"}`)
	if !deleted.OK {
		t.Fatalf("hosts.delete failed: %+v", deleted.Error)
	}
	missing := callApp(t, app, "hosts.get", `{"id":"`+host.ID+`"}`)
	if missing.OK {
		t.Fatal("hosts.get found a deleted host")
	}
	if missing.Error == nil || missing.Error.Code != CodeNotFound {
		t.Fatalf("hosts.get error = %+v, want not_found", missing.Error)
	}
}

func TestAppRejectsBadCalls(t *testing.T) {
	app, err := Start(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	unknown := callApp(t, app, "hosts.explode", "")
	if unknown.OK || unknown.Error == nil || unknown.Error.Code != CodeBadRequest {
		t.Fatalf("unknown method returned %+v", unknown)
	}

	badParams := callApp(t, app, "hosts.save", `{"label":`)
	if badParams.OK || badParams.Error == nil || badParams.Error.Code != CodeBadRequest {
		t.Fatalf("malformed params returned %+v", badParams)
	}

	invalidHost := callApp(t, app, "hosts.save", `{"label":"","address":"","user":""}`)
	if invalidHost.OK || invalidHost.Error == nil || invalidHost.Error.Code != CodeBadRequest {
		t.Fatalf("invalid host returned %+v", invalidHost)
	}

	missingSession := callApp(t, app, "sessions.input", `{"id":42,"data":""}`)
	if missingSession.OK || missingSession.Error == nil || missingSession.Error.Code != CodeNotFound {
		t.Fatalf("missing session returned %+v", missingSession)
	}
}

func TestAppInfo(t *testing.T) {
	app, err := Start(Config{DataDir: t.TempDir(), Version: "9.9.9"})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	info := callApp(t, app, "app.info", "")
	if !info.OK {
		t.Fatalf("app.info failed: %+v", info.Error)
	}
	var payload struct {
		Version string `json:"version"`
		DataDir string `json:"dataDir"`
	}
	if err := json.Unmarshal(info.Result, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Version != "9.9.9" {
		t.Fatalf("version = %q", payload.Version)
	}
	if payload.DataDir == "" {
		t.Fatal("dataDir is empty")
	}
}

func TestAppImportWarningsUseFrontEndNames(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config")
	if err := os.WriteFile(configPath, []byte("Host broken\n  Port not-a-number\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	app, err := Start(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	result := callApp(t, app, "hosts.importSSHConfig", `{"path":"`+configPath+`"}`)
	if !result.OK {
		t.Fatalf("import failed: %+v", result.Error)
	}
	var payload struct {
		Hosts    []Host `json:"hosts"`
		Warnings []struct {
			Code string `json:"code"`
			Line int    `json:"line"`
		} `json:"warnings"`
	}
	if err := json.Unmarshal(result.Result, &payload); err != nil {
		t.Fatalf("warnings do not match the front-end shape: %v\n%s", err, result.Result)
	}
	if len(payload.Warnings) == 0 || payload.Warnings[0].Code == "" {
		t.Fatalf("expected a documented warning, got %s", result.Result)
	}
}
