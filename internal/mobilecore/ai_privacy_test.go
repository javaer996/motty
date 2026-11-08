package mobilecore

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"motty/internal/ai/llm"
	"motty/internal/ai/privacy"
)

func TestAIPrivacySettingsDefaultAndSave(t *testing.T) {
	data := openTestDataAt(t, t.TempDir())
	store := openAISettingsStore(data)
	defaults := store.privacy()
	if !defaults.Enabled || !defaults.KnownValues || !defaults.Secrets || !defaults.IPs ||
		!defaults.Accounts || !defaults.Custom || defaults.Hosts || defaults.Emails {
		t.Fatalf("default privacy = %+v", defaults)
	}

	if _, err := store.save(aiSettings{Providers: []aiProvider{{
		ID: "one", Provider: "openai", Model: "m", APIKey: "key-one",
	}}}); err != nil {
		t.Fatal(err)
	}
	if got := store.privacy(); got != defaults {
		t.Fatalf("saving providers changed privacy: %+v", got)
	}

	custom := aiPrivacySettings{
		Enabled: true, KnownValues: true, Secrets: true, IPs: false,
		Accounts: true, Hosts: true, Emails: true, Custom: false,
	}
	if _, err := store.savePrivacy(custom); err != nil {
		t.Fatal(err)
	}
	reopened := openAISettingsStore(data)
	if got := reopened.privacy(); got != custom {
		t.Fatalf("reopened privacy = %+v, want %+v", got, custom)
	}
	current, ok := reopened.get().current()
	if !ok || current.APIKey != "key-one" {
		t.Fatalf("privacy save lost the provider key: %+v", current)
	}

	// A front end that saves provider settings without a privacy section must
	// not reset what the user chose.
	if _, err := store.save(aiSettings{Providers: []aiProvider{{
		ID: "one", Provider: "openai", Model: "m2", APIKey: "key-one",
	}}}); err != nil {
		t.Fatal(err)
	}
	if got := store.privacy(); got != custom {
		t.Fatalf("provider save reset privacy: %+v", got)
	}
}

func TestAIPrivacyGuardUsesHostSecretsAndCustomStrings(t *testing.T) {
	data := openTestDataAt(t, t.TempDir())
	if err := data.secretSet("host:h1:ssh", "supersecret-value"); err != nil {
		t.Fatal(err)
	}
	state := &aiState{data: data}
	if err := state.saveCustomStrings([]string{"Project Falcon"}); err != nil {
		t.Fatal(err)
	}
	host := Host{Address: "10.1.2.3", User: "alice", Label: "prod-db"}
	guard, err := state.privacyGuard(defaultAIPrivacySettings(), host)
	if err != nil {
		t.Fatal(err)
	}
	input := "alice on prod-db uses supersecret-value and Project Falcon from 10.1.2.3"
	protected, replacements := guard.Protect(input)
	if strings.Contains(protected, "alice") || strings.Contains(protected, "prod-db") ||
		strings.Contains(protected, "supersecret-value") || strings.Contains(protected, "Project Falcon") ||
		strings.Contains(protected, "10.1.2.3") {
		t.Fatalf("value leaked: %q", protected)
	}
	for _, category := range []privacy.Category{
		privacy.CategoryIP, privacy.CategoryUser, privacy.CategoryHost,
		privacy.CategorySecret, privacy.CategoryCustom,
	} {
		found := false
		for _, replacement := range replacements {
			if replacement.Category == category {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("no %s replacement in %+v", category, replacements)
		}
	}
	restored, _ := guard.Restore(protected)
	if restored != input {
		t.Fatalf("restored %q, want %q", restored, input)
	}
}

// tokenForTest assigns a token the same way a request would, so a scripted
// model answer can refer to it.
func tokenForTest(t *testing.T, guard *privacy.Guard, value string) string {
	t.Helper()
	_, replacements := guard.Protect(value)
	return replacementToken(t, replacements, value)
}

func replacementToken(t *testing.T, replacements []privacy.Replacement, value string) string {
	t.Helper()
	for _, replacement := range replacements {
		if replacement.Original == value {
			return replacement.Token
		}
	}
	t.Fatalf("no token for %q in %+v", value, replacements)
	return ""
}

func TestAIPrivacyProviderProtectsAndRestores(t *testing.T) {
	guard := privacy.NewGuard(privacy.Config{
		KnownValues: true,
		Secrets:     true,
		IPs:         true,
		Accounts:    true,
		Known: []privacy.Known{
			{Value: "example.test", Category: privacy.CategoryHost, Boundary: true},
			{Value: "s3cret-note-42", Category: privacy.CategorySecret, Boundary: true},
		},
	})
	_, probe := guard.Protect("ssh alice@192.168.1.20")
	userToken := replacementToken(t, probe, "alice")
	ipToken := replacementToken(t, probe, "192.168.1.20")
	toolToken := tokenForTest(t, guard, "s3cret-note-42")

	inner := &fakeProvider{turns: [][]llm.Event{{
		{Kind: llm.EventText, Text: "connecting to " + ipToken + ", then "},
		{Kind: llm.EventToolCall, ToolCall: &llm.ToolCall{
			ID: "call-1", Name: "terminal_run",
			Arguments: `{"command":"ssh ` + userToken + `@` + ipToken + `","note":"` + toolToken + `"}`,
		}},
		{Kind: llm.EventText, Text: "as " + userToken + "."},
		{Kind: llm.EventDone},
	}}}
	audit := openAIPrivacyAudit(t.TempDir())
	wrapper := &aiPrivacyProvider{inner: inner, guard: guard, audit: audit, runID: 7, sessionID: 3}
	request := llm.Request{
		Model:  "m",
		System: "you are on example.test",
		Messages: []llm.Message{{
			Role: llm.RoleUser,
			Text: "ssh alice@192.168.1.20",
		}},
	}

	events, err := wrapper.Stream(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	var toolArguments string
	done := false
	for event := range events {
		switch event.Kind {
		case llm.EventText:
			text.WriteString(event.Text)
		case llm.EventToolCall:
			if event.ToolCall != nil {
				toolArguments = event.ToolCall.Arguments
			}
		case llm.EventDone:
			done = true
		}
	}
	if !done {
		t.Fatal("the stream ended without a done event")
	}
	if got, want := text.String(), "connecting to 192.168.1.20, then as alice."; got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
	var arguments map[string]string
	if err := json.Unmarshal([]byte(toolArguments), &arguments); err != nil {
		t.Fatalf("tool arguments are not JSON: %q (%v)", toolArguments, err)
	}
	if arguments["command"] != "ssh alice@192.168.1.20" || arguments["note"] != "s3cret-note-42" {
		t.Fatalf("tool arguments = %+v", arguments)
	}

	if len(inner.requests) != 1 {
		t.Fatalf("inner got %d requests", len(inner.requests))
	}
	sent := inner.requests[0]
	if strings.Contains(sent.System, "example.test") {
		t.Fatalf("system prompt leaked the host: %q", sent.System)
	}
	if strings.Contains(sent.Messages[0].Text, "alice") || strings.Contains(sent.Messages[0].Text, "192.168.1.20") {
		t.Fatalf("user message leaked: %q", sent.Messages[0].Text)
	}
	if request.Messages[0].Text != "ssh alice@192.168.1.20" {
		t.Fatalf("the caller's history was modified: %q", request.Messages[0].Text)
	}

	entries := audit.list(10)
	if len(entries) != 2 {
		t.Fatalf("audit entries = %+v", entries)
	}
	inbound, outbound := entries[0], entries[1]
	if inbound.Direction != "in" || outbound.Direction != "out" {
		t.Fatalf("directions = %q, %q", inbound.Direction, outbound.Direction)
	}
	if outbound.Count == 0 || strings.Contains(outbound.Preview, "192.168.1.20") {
		t.Fatalf("outbound audit = %+v", outbound)
	}
	if inbound.Count != 3 {
		t.Fatalf("inbound audit = %+v", inbound)
	}
	if inbound.RunID != 7 || inbound.SessionID != 3 || inbound.Turn != 1 {
		t.Fatalf("inbound identity = %+v", inbound)
	}
}

func TestAIPrivacyAuditKeepsNewestAndPersists(t *testing.T) {
	dir := t.TempDir()
	audit := openAIPrivacyAudit(dir)
	for index := 0; index < 60; index++ {
		audit.add(aiPrivacyAuditEntry{Direction: "out"})
	}
	entries := audit.list(0)
	if len(entries) != aiPrivacyLogLimit {
		t.Fatalf("default view has %d entries", len(entries))
	}
	if entries[0].ID == entries[1].ID {
		t.Fatal("audit ids repeat")
	}

	reopened := openAIPrivacyAudit(dir)
	if got := len(reopened.list(aiPrivacyAuditLimit)); got != aiPrivacyAuditLimit {
		t.Fatalf("persisted entries = %d", got)
	}
	if _, err := os.Stat(filepath.Join(dir, aiPrivacyAuditFile)); err != nil {
		t.Fatalf("audit file missing: %v", err)
	}
	if err := audit.clear(); err != nil {
		t.Fatal(err)
	}
	if len(audit.list(10)) != 0 {
		t.Fatal("clear kept entries")
	}
	if _, err := os.Stat(filepath.Join(dir, aiPrivacyAuditFile)); !os.IsNotExist(err) {
		t.Fatalf("audit file survived clear: %v", err)
	}
}

func TestAIPrivacyRPC(t *testing.T) {
	app, err := Start(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	got := callApp(t, app, "ai.privacy.get", "")
	if !got.OK {
		t.Fatalf("ai.privacy.get failed: %+v", got.Error)
	}
	// A nil slice encodes as null, and the Swift model decodes an array: an
	// empty list must stay a list on the wire.
	if !strings.Contains(string(got.Result), `"customStrings":[]`) {
		t.Fatalf("empty custom strings must encode as an array: %s", got.Result)
	}
	var info aiPrivacyInfo
	if err := json.Unmarshal(got.Result, &info); err != nil {
		t.Fatal(err)
	}
	if !info.Settings.Enabled {
		t.Fatalf("privacy is off by default: %+v", info)
	}

	saved := callApp(t, app, "ai.privacy.save", `{"settings":{"enabled":true,"knownValues":true,"secrets":true,"ips":true,"accounts":true,"hosts":true,"emails":false,"custom":true},"customStrings":["Acme Internal","","Acme Internal"]}`)
	if !saved.OK {
		t.Fatalf("ai.privacy.save failed: %+v", saved.Error)
	}
	if err := json.Unmarshal(saved.Result, &info); err != nil {
		t.Fatal(err)
	}
	if len(info.CustomStrings) != 1 || info.CustomStrings[0] != "Acme Internal" {
		t.Fatalf("custom strings = %+v", info.CustomStrings)
	}
	if !info.Settings.Hosts || info.Settings.Emails {
		t.Fatalf("saved settings = %+v", info.Settings)
	}

	preview := callApp(t, app, "ai.privacy.preview", `{"text":"ssh alice@192.168.1.20"}`)
	if !preview.OK {
		t.Fatalf("ai.privacy.preview failed: %+v", preview.Error)
	}
	var result struct {
		Text  string `json:"text"`
		Count int    `json:"count"`
	}
	if err := json.Unmarshal(preview.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.Count == 0 || strings.Contains(result.Text, "192.168.1.20") {
		t.Fatalf("preview = %+v", result)
	}

	log := callApp(t, app, "ai.privacy.log", `{"limit":5}`)
	if !log.OK {
		t.Fatalf("ai.privacy.log failed: %+v", log.Error)
	}
	cleared := callApp(t, app, "ai.privacy.clear", "")
	if !cleared.OK {
		t.Fatalf("ai.privacy.clear failed: %+v", cleared.Error)
	}
}
