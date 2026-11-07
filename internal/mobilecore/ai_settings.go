package mobilecore

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"motty/internal/ai/llm"
)

// aiProvider is one configured assistant endpoint: the wire protocol, where it
// lives, the key, and the model and behaviour the assistant uses with it.
// Several may be configured at once; aiSettings.CurrentID picks the one the
// assistant talks to.
type aiProvider struct {
	ID          string   `json:"id"`
	Name        string   `json:"name,omitempty"`
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

// aiSettings is the assistant configuration: the configured providers and the
// active one. The API keys are not in the file; they live in the vault under
// aikey:<provider id>.
//
// The flat Provider/BaseURL/... fields are the single-provider shape earlier
// builds wrote to ai.json and accepted over RPC. They are read and folded into
// a provider on load; new saves only write the list.
type aiSettings struct {
	Providers []aiProvider `json:"providers"`
	CurrentID string       `json:"currentId,omitempty"`

	Provider    string   `json:"provider,omitempty"`
	BaseURL     string   `json:"baseUrl,omitempty"`
	APIKey      string   `json:"apiKey,omitempty"`
	Model       string   `json:"model,omitempty"`
	MaxTokens   int      `json:"maxTokens,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	System      string   `json:"system,omitempty"`
	Mode        string   `json:"mode,omitempty"`
}

// defaultAISettings keeps the endpoint earlier builds assumed, so a key synced
// from another device under aikey:openai still shows up on a fresh install.
// It has no model, so the assistant stays "not configured" until the user
// picks one.
func defaultAISettings() aiSettings {
	return aiSettings{
		Providers: []aiProvider{{ID: "openai", Provider: "openai", Mode: "agent"}},
		CurrentID: "openai",
	}
}

// newAIProviderID names a provider the same way the desktop does.
func newAIProviderID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "p" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return "p" + hex.EncodeToString(b[:])
}

// normalized fills defaults, checks what the front end sent, and gives every
// provider a stable unique id. The key is kept as-is; the store moves it to
// the vault.
func (s aiSettings) normalized() (aiSettings, error) {
	providers := make([]aiProvider, 0, len(s.Providers))
	providers = append(providers, s.Providers...)
	if len(providers) == 0 && (s.Provider != "" || s.Model != "") {
		legacy, err := aiProvider{
			Provider: s.Provider, BaseURL: s.BaseURL, APIKey: s.APIKey,
			Model: s.Model, MaxTokens: s.MaxTokens, Temperature: s.Temperature,
			System: s.System, Mode: s.Mode,
		}.normalized()
		if err != nil {
			return aiSettings{}, err
		}
		// The old vault name was aikey:<kind>; keeping the kind as the id
		// means the saved key is found without a migration step.
		legacy.ID = legacy.Provider
		providers = append(providers, legacy)
	}
	seen := make(map[string]bool, len(providers))
	for i := range providers {
		normalized, err := providers[i].normalized()
		if err != nil {
			return aiSettings{}, err
		}
		if normalized.ID == "" || seen[normalized.ID] {
			normalized.ID = newAIProviderID()
		}
		seen[normalized.ID] = true
		providers[i] = normalized
	}
	current := strings.TrimSpace(s.CurrentID)
	if current == "" || !seen[current] {
		current = ""
		if len(providers) > 0 {
			current = providers[0].ID
		}
	}
	return aiSettings{Providers: providers, CurrentID: current}, nil
}

// current returns the provider the assistant should use.
func (s aiSettings) current() (aiProvider, bool) {
	for _, provider := range s.Providers {
		if provider.ID == s.CurrentID {
			return provider, true
		}
	}
	if len(s.Providers) > 0 {
		return s.Providers[0], true
	}
	return aiProvider{}, false
}

func (p aiProvider) normalized() (aiProvider, error) {
	p.ID = strings.TrimSpace(p.ID)
	p.Name = strings.TrimSpace(p.Name)
	p.Provider = strings.ToLower(strings.TrimSpace(p.Provider))
	if p.Provider == "" {
		p.Provider = "openai"
	}
	switch p.Provider {
	case "openai", "anthropic", "responses":
	default:
		return aiProvider{}, errorf(CodeBadRequest, "unknown assistant provider %q", p.Provider)
	}
	p.BaseURL = strings.TrimSpace(p.BaseURL)
	p.APIKey = strings.TrimSpace(p.APIKey)
	p.Model = strings.TrimSpace(p.Model)
	p.Mode = strings.ToLower(strings.TrimSpace(p.Mode))
	if p.Mode != "ask" {
		p.Mode = "agent"
	}
	if p.MaxTokens < 0 {
		return aiProvider{}, errorf(CodeBadRequest, "maxTokens cannot be negative")
	}
	if p.Temperature != nil && (*p.Temperature < 0 || *p.Temperature > 2) {
		return aiProvider{}, errorf(CodeBadRequest, "temperature must be between 0 and 2")
	}
	p.System = strings.TrimSpace(p.System)
	return p, nil
}

// provider builds the wire client this configuration names.
func (p aiProvider) provider() (llm.Provider, error) {
	config := llm.Config{BaseURL: p.BaseURL, APIKey: p.APIKey}
	switch p.Provider {
	case "openai":
		return llm.NewOpenAI(config), nil
	case "anthropic":
		return llm.NewAnthropic(config), nil
	case "responses":
		return llm.NewOpenAIResponses(config), nil
	default:
		return nil, errorf(CodeBadRequest, "unknown assistant provider %q", p.Provider)
	}
}

// aiSettingsStore keeps settings in <dataDir>/ai.json. API keys are not in
// that file: they live in the vault under aikey:<provider id>, where a backup
// and a sync carry them like any other saved secret. Keys an earlier build
// wrote into ai.json are moved into the vault on the first start. A missing or
// damaged file yields defaults rather than preventing the app from starting.
type aiSettingsStore struct {
	mu      sync.Mutex
	path    string
	data    *dataStore
	current aiSettings
}

// aiKeySecret is the vault name of one provider's API key.
func aiKeySecret(providerID string) string { return "aikey:" + providerID }

func openAISettingsStore(data *dataStore) *aiSettingsStore {
	store := &aiSettingsStore{
		path:    filepath.Join(data.dir, "ai.json"),
		data:    data,
		current: defaultAISettings(),
	}
	raw, err := os.ReadFile(store.path)
	if err != nil {
		store.loadKeys()
		return store
	}
	var loaded aiSettings
	if err := json.Unmarshal(raw, &loaded); err != nil {
		store.loadKeys()
		return store
	}
	normalized, err := loaded.normalized()
	if err != nil {
		store.loadKeys()
		return store
	}
	// Move keys an earlier build left in the plain file into the vault.
	moved := false
	for i := range normalized.Providers {
		key := normalized.Providers[i].APIKey
		if key == "" {
			continue
		}
		if _, found, err := data.secretGet(aiKeySecret(normalized.Providers[i].ID)); err == nil && !found {
			_ = data.secretSet(aiKeySecret(normalized.Providers[i].ID), key)
		}
		normalized.Providers[i].APIKey = ""
		moved = true
	}
	if moved {
		// Write before loadKeys: it fills the keys back into the shared
		// provider slice, and the plain file must not carry them.
		if data, err := json.MarshalIndent(normalized, "", "  "); err == nil {
			_ = writeFileAtomic(store.path, data)
		}
	}
	store.current = normalized
	store.loadKeys()
	return store
}

// loadKeys fills every provider's API key from the vault. The caller holds no
// lock yet; this runs only while the store is being built.
func (s *aiSettingsStore) loadKeys() {
	for i := range s.current.Providers {
		if key, found, err := s.data.secretGet(aiKeySecret(s.current.Providers[i].ID)); err == nil && found {
			s.current.Providers[i].APIKey = key
		}
	}
}

// reloadKeys rereads the API keys after a sync or a restore replaced the vault.
func (s *aiSettingsStore) reloadKeys() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.current.Providers {
		key, found, err := s.data.secretGet(aiKeySecret(s.current.Providers[i].ID))
		if err != nil {
			continue
		}
		if found {
			s.current.Providers[i].APIKey = key
		} else {
			s.current.Providers[i].APIKey = ""
		}
	}
}

func (s *aiSettingsStore) get() aiSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	settings := s.current
	settings.Providers = append([]aiProvider(nil), s.current.Providers...)
	return settings
}

func (s *aiSettingsStore) save(settings aiSettings) (aiSettings, error) {
	normalized, err := settings.normalized()
	if err != nil {
		return aiSettings{}, err
	}
	keys := make(map[string]string, len(normalized.Providers))
	for i := range normalized.Providers {
		keys[normalized.Providers[i].ID] = normalized.Providers[i].APIKey
		normalized.Providers[i].APIKey = "" // the plain file never holds keys
	}
	data, err := json.MarshalIndent(normalized, "", "  ")
	if err != nil {
		return aiSettings{}, errorf(CodeInternal, "cannot encode the assistant settings: %v", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := writeFileAtomic(s.path, data); err != nil {
		return aiSettings{}, errorf(CodeInternal, "cannot save the assistant settings: %v", err)
	}
	// A provider removed from the list takes its key with it.
	for _, old := range s.current.Providers {
		if _, kept := keys[old.ID]; !kept {
			_ = s.data.secretDelete(aiKeySecret(old.ID))
		}
	}
	for id, key := range keys {
		if key != "" {
			if err := s.data.secretSet(aiKeySecret(id), key); err != nil {
				return aiSettings{}, errorf(CodeInternal, "cannot save the assistant API key: %v", err)
			}
		} else if err := s.data.secretDelete(aiKeySecret(id)); err != nil {
			return aiSettings{}, errorf(CodeInternal, "cannot forget the assistant API key: %v", err)
		}
	}
	for i := range normalized.Providers {
		normalized.Providers[i].APIKey = keys[normalized.Providers[i].ID]
	}
	s.current = normalized
	return normalized, nil
}
