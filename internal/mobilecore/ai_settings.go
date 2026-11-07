package mobilecore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"motty/internal/ai/llm"
)

// aiSettings is the assistant configuration. The API key lives in the same
// 0600 file as the rest for now; the app sandbox keeps it private, and a later
// milestone can move it to the Keychain without changing this API.
type aiSettings struct {
	Provider    string   `json:"provider"`
	BaseURL     string   `json:"baseUrl,omitempty"`
	APIKey      string   `json:"apiKey,omitempty"`
	Model       string   `json:"model"`
	MaxTokens   int      `json:"maxTokens,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	System      string   `json:"system,omitempty"`
	// Mode is "agent" (tools offered) or "ask" (answer only, with an offer to
	// switch). Defaults to agent.
	Mode string `json:"mode,omitempty"`
}

func defaultAISettings() aiSettings {
	return aiSettings{Provider: "openai", Mode: "agent"}
}

// normalized fills defaults and checks what the front end sent.
func (s aiSettings) normalized() (aiSettings, error) {
	s.Provider = strings.ToLower(strings.TrimSpace(s.Provider))
	if s.Provider == "" {
		s.Provider = "openai"
	}
	switch s.Provider {
	case "openai", "anthropic", "responses":
	default:
		return aiSettings{}, errorf(CodeBadRequest, "unknown assistant provider %q", s.Provider)
	}
	s.BaseURL = strings.TrimSpace(s.BaseURL)
	s.APIKey = strings.TrimSpace(s.APIKey)
	s.Model = strings.TrimSpace(s.Model)
	if s.Model == "" {
		return aiSettings{}, errorf(CodeBadRequest, "a model is required")
	}
	s.Mode = strings.ToLower(strings.TrimSpace(s.Mode))
	if s.Mode != "ask" {
		s.Mode = "agent"
	}
	if s.MaxTokens < 0 {
		return aiSettings{}, errorf(CodeBadRequest, "maxTokens cannot be negative")
	}
	if s.Temperature != nil && (*s.Temperature < 0 || *s.Temperature > 2) {
		return aiSettings{}, errorf(CodeBadRequest, "temperature must be between 0 and 2")
	}
	s.System = strings.TrimSpace(s.System)
	return s, nil
}

// provider builds the wire client the settings name.
func (s aiSettings) provider() (llm.Provider, error) {
	config := llm.Config{BaseURL: s.BaseURL, APIKey: s.APIKey}
	switch s.Provider {
	case "openai":
		return llm.NewOpenAI(config), nil
	case "anthropic":
		return llm.NewAnthropic(config), nil
	case "responses":
		return llm.NewOpenAIResponses(config), nil
	default:
		return nil, errorf(CodeBadRequest, "unknown assistant provider %q", s.Provider)
	}
}

// aiSettingsStore keeps settings in <dataDir>/ai.json. The API key is not in
// that file: it lives in the vault, under aikey:<provider>, where a backup and
// a sync carry it like any other saved secret. A key an earlier build wrote into
// ai.json is moved into the vault on the first start. A missing or damaged file
// yields defaults rather than preventing the app from starting.
type aiSettingsStore struct {
	mu      sync.Mutex
	path    string
	data    *dataStore
	current aiSettings
}

// aiKeySecret is the vault name of one provider's API key.
func aiKeySecret(provider string) string { return "aikey:" + provider }

func openAISettingsStore(data *dataStore) *aiSettingsStore {
	store := &aiSettingsStore{
		path:    filepath.Join(data.dir, "ai.json"),
		data:    data,
		current: defaultAISettings(),
	}
	raw, err := os.ReadFile(store.path)
	if err != nil {
		store.loadKey()
		return store
	}
	var loaded aiSettings
	if err := json.Unmarshal(raw, &loaded); err != nil {
		store.loadKey()
		return store
	}
	if loaded.APIKey != "" {
		// Move the key out of the plain file, once.
		if _, found, err := data.secretGet(aiKeySecret(loaded.Provider)); err == nil && !found {
			_ = data.secretSet(aiKeySecret(loaded.Provider), loaded.APIKey)
		}
		loaded.APIKey = ""
		if data, err := json.MarshalIndent(loaded, "", "  "); err == nil {
			_ = writeFileAtomic(store.path, data)
		}
	}
	if normalized, err := loaded.normalized(); err == nil {
		store.current = normalized
	}
	store.loadKey()
	return store
}

// loadKey fills the API key from the vault. The caller holds no lock yet; this
// runs only while the store is being built.
func (s *aiSettingsStore) loadKey() {
	if key, found, err := s.data.secretGet(aiKeySecret(s.current.Provider)); err == nil && found {
		s.current.APIKey = key
	}
}

// reloadKey rereads the API key after a sync or a restore replaced the vault.
func (s *aiSettingsStore) reloadKey() {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, found, err := s.data.secretGet(aiKeySecret(s.current.Provider))
	if err != nil {
		return
	}
	if found {
		s.current.APIKey = key
	} else {
		s.current.APIKey = ""
	}
}

func (s *aiSettingsStore) get() aiSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current
}

func (s *aiSettingsStore) save(settings aiSettings) (aiSettings, error) {
	normalized, err := settings.normalized()
	if err != nil {
		return aiSettings{}, err
	}
	key := normalized.APIKey
	normalized.APIKey = "" // the plain file never holds the key
	data, err := json.MarshalIndent(normalized, "", "  ")
	if err != nil {
		return aiSettings{}, errorf(CodeInternal, "cannot encode the assistant settings: %v", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := writeFileAtomic(s.path, data); err != nil {
		return aiSettings{}, errorf(CodeInternal, "cannot save the assistant settings: %v", err)
	}
	if key != "" {
		if err := s.data.secretSet(aiKeySecret(normalized.Provider), key); err != nil {
			return aiSettings{}, errorf(CodeInternal, "cannot save the assistant API key: %v", err)
		}
	} else {
		if err := s.data.secretDelete(aiKeySecret(normalized.Provider)); err != nil {
			return aiSettings{}, errorf(CodeInternal, "cannot forget the assistant API key: %v", err)
		}
	}
	normalized.APIKey = key
	s.current = normalized
	return normalized, nil
}
