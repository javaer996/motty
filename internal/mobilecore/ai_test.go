package mobilecore

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"motty/internal/ai/llm"
)

func TestAISettingsDefaultsAndSave(t *testing.T) {
	dir := t.TempDir()
	data := openTestDataAt(t, dir)
	store := openAISettingsStore(data)
	defaults := store.get()
	if defaults.Provider != "openai" || defaults.Mode != "agent" {
		t.Fatalf("defaults = %+v", defaults)
	}

	if _, err := store.save(aiSettings{Provider: "openai"}); err == nil {
		t.Fatal("a settings save without a model succeeded")
	}
	if _, err := store.save(aiSettings{Provider: "nope", Model: "m"}); err == nil {
		t.Fatal("an unknown provider was accepted")
	}

	saved, err := store.save(aiSettings{
		Provider: " Anthropic ", BaseURL: "https://example.test", APIKey: "sk-test",
		Model: " claude-sonnet-5-5 ", Mode: "ASK",
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Provider != "anthropic" || saved.Model != "claude-sonnet-5-5" || saved.Mode != "ask" {
		t.Fatalf("normalized = %+v", saved)
	}

	reopened := openAISettingsStore(data)
	if got := reopened.get(); got.Provider != "anthropic" || got.Model != "claude-sonnet-5-5" || got.APIKey != "sk-test" {
		t.Fatalf("reopened settings = %+v", got)
	}
	if got := reopened.get().Mode; got != "ask" {
		t.Fatalf("mode = %q, want ask", got)
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

	var seen aiSettings
	app.ai.provider = func(settings aiSettings) (llm.Provider, error) {
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
	if got := app.ai.settings.get(); got.Provider != "openai" || got.APIKey != savedKey {
		t.Fatalf("listing models persisted the draft: %+v", got)
	}

	// Without a draft the saved settings still apply.
	seen = aiSettings{}
	if again := callApp(t, app, "ai.models", `{}`); !again.OK {
		t.Fatalf("ai.models with empty params failed: %+v", again.Error)
	}
	if seen.Provider != "openai" || seen.APIKey != savedKey {
		t.Fatalf("provider built from %+v, want the saved settings", seen)
	}

	// A draft can change one field without clearing the saved key.
	seen = aiSettings{}
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
	if loaded.Provider != "openai" {
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
