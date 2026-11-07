package mobilecore

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"motty/internal/ai/llm"
)

func TestAISettingsDefaultsAndSave(t *testing.T) {
	dir := t.TempDir()
	data := openTestDataAt(t, dir)
	store := openAISettingsStore(data)
	defaults := store.get()
	if current, ok := defaults.current(); !ok || current.Provider != "openai" || current.Mode != "agent" {
		t.Fatalf("defaults = %+v", defaults)
	}

	if _, err := store.save(aiSettings{Providers: []aiProvider{{Provider: "nope", Model: "m"}}}); err == nil {
		t.Fatal("an unknown provider was accepted")
	}

	saved, err := store.save(aiSettings{Providers: []aiProvider{{
		Provider: " Anthropic ", BaseURL: "https://example.test", APIKey: "sk-test",
		Model: " claude-sonnet-5-5 ", Mode: "ASK",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	current, ok := saved.current()
	if !ok || current.Provider != "anthropic" || current.Model != "claude-sonnet-5-5" || current.Mode != "ask" {
		t.Fatalf("normalized = %+v", saved)
	}
	if current.ID == "" || saved.CurrentID != current.ID {
		t.Fatalf("the provider got no active id: %+v", saved)
	}

	reopened := openAISettingsStore(data)
	reopenedProvider, ok := reopened.get().current()
	if !ok || reopenedProvider.Provider != "anthropic" || reopenedProvider.Model != "claude-sonnet-5-5" || reopenedProvider.APIKey != "sk-test" {
		t.Fatalf("reopened settings = %+v", reopened.get())
	}
	if got := reopenedProvider.Mode; got != "ask" {
		t.Fatalf("mode = %q, want ask", got)
	}
}

// Several providers may be configured at once; switching the active id has to
// pick the matching endpoint and key, and removing one has to forget its key.
func TestAISettingsKeepSeveralProviders(t *testing.T) {
	dir := t.TempDir()
	data := openTestDataAt(t, dir)
	store := openAISettingsStore(data)

	saved, err := store.save(aiSettings{Providers: []aiProvider{
		{ID: "one", Provider: "openai", Model: "gpt", APIKey: "key-one"},
		{ID: "two", Provider: "anthropic", Model: "claude", APIKey: "key-two"},
	}, CurrentID: "two"})
	if err != nil {
		t.Fatal(err)
	}
	if saved.CurrentID != "two" {
		t.Fatalf("current = %q", saved.CurrentID)
	}
	current, _ := saved.current()
	if current.Model != "claude" || current.APIKey != "key-two" {
		t.Fatalf("active provider = %+v", current)
	}

	reopened := openAISettingsStore(data)
	if got := reopened.get().CurrentID; got != "two" {
		t.Fatalf("reopened current = %q", got)
	}
	if current, _ := reopened.get().current(); current.APIKey != "key-two" {
		t.Fatalf("reopened active key = %q", current.APIKey)
	}

	// Removing the second provider drops its key; the first one stays.
	if _, err := store.save(aiSettings{Providers: []aiProvider{
		{ID: "one", Provider: "openai", Model: "gpt", APIKey: "key-one"},
	}, CurrentID: "one"}); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := data.secretGet(aiKeySecret("two")); found {
		t.Fatal("the removed provider kept its key")
	}
	if key, found, _ := data.secretGet(aiKeySecret("one")); !found || key != "key-one" {
		t.Fatalf("the kept provider lost its key: %q, %v", key, found)
	}
}

// A new key or endpoint is often typed and then used to list models before the
// form is saved; that draft must reach the provider without being persisted.
func TestAIModelsUsesDraftSettings(t *testing.T) {
	app, err := Start(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	const savedKey = "key-one"
	const draftKey = "key-two"
	if saved := callApp(t, app, "ai.settings.save",
		`{"provider":"openai","model":"saved-model","apiKey":"`+savedKey+`"}`); !saved.OK {
		t.Fatalf("saving settings failed: %+v", saved.Error)
	}

	var seen aiProvider
	app.ai.provider = func(settings aiProvider) (llm.Provider, error) {
		seen = settings
		return &fakeProvider{}, nil
	}

	listed := callApp(t, app, "ai.models",
		`{"provider":"Anthropic","baseUrl":" https://draft.test ","apiKey":"`+draftKey+`"}`)
	if !listed.OK {
		t.Fatalf("ai.models failed: %+v", listed.Error)
	}
	if seen.Provider != "anthropic" || seen.BaseURL != "https://draft.test" || seen.APIKey != draftKey {
		t.Fatalf("provider built from %+v, want the draft settings", seen)
	}
	if got, _ := app.ai.settings.get().current(); got.Provider != "openai" || got.APIKey != savedKey {
		t.Fatalf("listing models persisted the draft: %+v", got)
	}

	// Without a draft the saved settings still apply.
	seen = aiProvider{}
	if again := callApp(t, app, "ai.models", `{}`); !again.OK {
		t.Fatalf("ai.models with empty params failed: %+v", again.Error)
	}
	if seen.Provider != "openai" || seen.APIKey != savedKey {
		t.Fatalf("provider built from %+v, want the saved settings", seen)
	}

	// A draft can change one field without clearing the saved key.
	seen = aiProvider{}
	if partial := callApp(t, app, "ai.models",
		`{"provider":"openai","baseUrl":"https://other.test"}`); !partial.OK {
		t.Fatalf("ai.models with a partial draft failed: %+v", partial.Error)
	}
	if seen.BaseURL != "https://other.test" || seen.APIKey != savedKey {
		t.Fatalf("provider built from %+v, want the saved key with the draft endpoint", seen)
	}
}

// The models endpoint must authenticate with the key on screen, not only with
// one that was saved earlier; otherwise loading models before Save is a 401.
func TestAIModelsSendsDraftKey(t *testing.T) {
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		if authorization != "Bearer key-two" {
			http.Error(w, `{"error":{"message":"unauthorized"}}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"model-b"},{"id":"model-a"}]}`)
	}))
	defer server.Close()

	app, err := Start(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	listed := callApp(t, app, "ai.models",
		`{"provider":"openai","baseUrl":"`+server.URL+`","apiKey":"key-two"}`)
	if !listed.OK {
		t.Fatalf("ai.models failed: %+v", listed.Error)
	}
	if authorization != "Bearer key-two" {
		t.Fatalf("Authorization = %q, want the draft key", authorization)
	}
	var result struct {
		Models []string `json:"models"`
	}
	if err := json.Unmarshal(listed.Result, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Models) != 2 || result.Models[0] != "model-a" || result.Models[1] != "model-b" {
		t.Fatalf("models = %v", result.Models)
	}
}

func TestAIRunErrors(t *testing.T) {
	app, err := Start(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	// No model configured yet.
	missing := callApp(t, app, "ai.send", `{"sessionId":1,"text":"hi"}`)
	if missing.OK || missing.Error == nil || missing.Error.Code != CodeBadRequest {
		t.Fatalf("ai.send without settings returned %+v", missing)
	}

	if cancelled := callApp(t, app, "ai.cancel", `{"runId":99}`); cancelled.OK || cancelled.Error == nil || cancelled.Error.Code != CodeNotFound {
		t.Fatalf("ai.cancel for an unknown run returned %+v", cancelled)
	}
	if approved := callApp(t, app, "ai.approve", `{"runId":99,"approvalId":"1","approved":true}`); approved.OK || approved.Error == nil || approved.Error.Code != CodeNotFound {
		t.Fatalf("ai.approve for an unknown run returned %+v", approved)
	}

	settings := callApp(t, app, "ai.settings.get", "")
	if !settings.OK {
		t.Fatalf("ai.settings.get failed: %+v", settings.Error)
	}
	var loaded aiSettings
	if err := json.Unmarshal(settings.Result, &loaded); err != nil {
		t.Fatal(err)
	}
	if current, ok := loaded.current(); !ok || current.Provider != "openai" {
		t.Fatalf("settings = %+v", loaded)
	}
}

func TestAISendReportsModelRequirement(t *testing.T) {
	app, err := Start(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if saved := callApp(t, app, "ai.settings.save", `{"provider":"openai","model":"test-model"}`); !saved.OK {
		t.Fatalf("saving settings failed: %+v", saved.Error)
	}
	// The session does not exist, so send must fail before dialing a provider.
	missing := callApp(t, app, "ai.send", `{"sessionId":42,"text":"hi"}`)
	if missing.OK || missing.Error == nil || missing.Error.Code != CodeNotFound {
		t.Fatalf("ai.send for a missing session returned %+v", missing)
	}
}

// The settings screen saves the whole provider list and switches the active
// one by id; the wire shape the app sends has to round-trip and keep every
// key in the vault.
func TestAISettingsRPCSwitchesProviders(t *testing.T) {
	app, err := Start(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	const payload = `{"providers":[
		{"id":"work","name":"Work","provider":"openai","baseUrl":"https://work.test","model":"gpt-work","apiKey":"key-work"},
		{"id":"home","provider":"anthropic","model":"claude-home","apiKey":"key-home","mode":"ask"}
	],"currentId":"home"}`
	saved := callApp(t, app, "ai.settings.save", payload)
	if !saved.OK {
		t.Fatalf("ai.settings.save failed: %+v", saved.Error)
	}
	var settings aiSettings
	if err := json.Unmarshal(saved.Result, &settings); err != nil {
		t.Fatal(err)
	}
	current, ok := settings.current()
	if !ok || current.ID != "home" || current.Model != "claude-home" || current.APIKey != "key-home" || current.Mode != "ask" {
		t.Fatalf("active provider = %+v", current)
	}
	if len(settings.Providers) != 2 {
		t.Fatalf("providers = %+v", settings.Providers)
	}

	// Switching only the active id keeps both keys.
	const switched = `{"providers":[
		{"id":"work","name":"Work","provider":"openai","baseUrl":"https://work.test","model":"gpt-work","apiKey":"key-work"},
		{"id":"home","provider":"anthropic","model":"claude-home","apiKey":"key-home","mode":"ask"}
	],"currentId":"work"}`
	if again := callApp(t, app, "ai.settings.save", switched); !again.OK {
		t.Fatalf("switching providers failed: %+v", again.Error)
	}
	got := app.ai.settings.get()
	current, _ = got.current()
	if current.ID != "work" || current.APIKey != "key-work" {
		t.Fatalf("switched provider = %+v", current)
	}
	for id, want := range map[string]string{"work": "key-work", "home": "key-home"} {
		if key, found, err := app.data.secretGet(aiKeySecret(id)); err != nil || !found || key != want {
			t.Fatalf("vault key %q = %q, %v, %v", id, key, found, err)
		}
	}

	// The plain file never holds a key.
	raw, err := os.ReadFile(filepath.Join(app.cfg.DataDir, "ai.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "key-work") || strings.Contains(string(raw), "key-home") {
		t.Fatalf("ai.json holds a key: %s", raw)
	}
}
