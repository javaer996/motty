package mobilecore

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"motty/internal/ai/llm"
	"motty/internal/ai/privacy"
)

const (
	// aiPrivacyCustomSecret is the vault entry holding the words the user
	// listed by hand. It lives with the other secrets so a backup or a sync
	// carries it to the next device.
	aiPrivacyCustomSecret = "privacy:custom"

	aiPrivacyAuditFile    = "ai-privacy-audit.json"
	aiPrivacyAuditLimit   = 50
	aiPrivacyLogLimit     = 10
	aiPrivacyPreviewRunes = 200
	aiPrivacyMaxCustom    = 32
	aiPrivacyMaxCustomLen = 256
)

// aiPrivacyReplacement is one row of the audit detail: the token the model
// saw, and the value it stands for. Secrets are masked; addresses, users and
// names are shown in full so the user can check what was hidden.
type aiPrivacyReplacement struct {
	Category string `json:"category"`
	Token    string `json:"token"`
	Value    string `json:"value"`
}

// aiPrivacyAuditEntry is one request sent to the provider or one reply
// received. Preview keeps the tokenized text, never the original.
type aiPrivacyAuditEntry struct {
	ID           string                 `json:"id"`
	Time         int64                  `json:"time"`
	Direction    string                 `json:"direction"` // "out" or "in"
	RunID        uint64                 `json:"runId"`
	SessionID    uint32                 `json:"sessionId"`
	Turn         int                    `json:"turn"`
	Preview      string                 `json:"preview,omitempty"`
	Count        int                    `json:"count"`
	Replacements []aiPrivacyReplacement `json:"replacements,omitempty"`
}

// aiPrivacyAudit keeps the newest exchanges in memory and on disk. A failure
// to persist never blocks a conversation: the audit is evidence, not state.
type aiPrivacyAudit struct {
	mu      sync.Mutex
	path    string
	entries []aiPrivacyAuditEntry
	nextID  uint64
}

func openAIPrivacyAudit(dir string) *aiPrivacyAudit {
	audit := &aiPrivacyAudit{path: filepath.Join(dir, aiPrivacyAuditFile)}
	raw, err := os.ReadFile(audit.path)
	if err != nil {
		return audit
	}
	var entries []aiPrivacyAuditEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return audit
	}
	if len(entries) > aiPrivacyAuditLimit {
		entries = entries[:aiPrivacyAuditLimit]
	}
	audit.entries = entries
	audit.nextID = uint64(len(entries))
	return audit
}

func (a *aiPrivacyAudit) add(entry aiPrivacyAuditEntry) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nextID++
	entry.ID = fmt.Sprintf("%d-%d", time.Now().UnixNano(), a.nextID)
	entry.Time = time.Now().UnixMilli()
	a.entries = append([]aiPrivacyAuditEntry{entry}, a.entries...)
	if len(a.entries) > aiPrivacyAuditLimit {
		a.entries = a.entries[:aiPrivacyAuditLimit]
	}
	a.persistLocked()
}

func (a *aiPrivacyAudit) list(limit int) []aiPrivacyAuditEntry {
	if a == nil {
		return nil
	}
	if limit <= 0 {
		limit = aiPrivacyLogLimit
	}
	if limit > aiPrivacyAuditLimit {
		limit = aiPrivacyAuditLimit
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if limit > len(a.entries) {
		limit = len(a.entries)
	}
	return append([]aiPrivacyAuditEntry(nil), a.entries[:limit]...)
}

func (a *aiPrivacyAudit) clear() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = nil
	if err := os.Remove(a.path); err != nil && !os.IsNotExist(err) {
		return errorf(CodeInternal, "cannot clear the privacy log: %v", err)
	}
	return nil
}

func (a *aiPrivacyAudit) persistLocked() {
	encoded, err := json.MarshalIndent(a.entries, "", "  ")
	if err != nil {
		return
	}
	_ = writeFileAtomic(a.path, encoded)
}

// customStrings reads the words the user listed for replacement. A damaged
// entry reads as an empty list: protection still works, the list is just gone.
func (s *aiState) customStrings() ([]string, error) {
	raw, found, err := s.data.secretGet(aiPrivacyCustomSecret)
	if err != nil {
		return nil, errorf(CodeInternal, "cannot read the custom privacy strings: %v", err)
	}
	if !found || strings.TrimSpace(raw) == "" {
		return []string{}, nil
	}
	var list []string
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return []string{}, nil
	}
	return normalizeCustomStrings(list), nil
}

func (s *aiState) saveCustomStrings(list []string) error {
	normalized := normalizeCustomStrings(list)
	if len(normalized) == 0 {
		if err := s.data.secretDelete(aiPrivacyCustomSecret); err != nil {
			return errorf(CodeInternal, "cannot clear the custom privacy strings: %v", err)
		}
		return nil
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return errorf(CodeInternal, "cannot encode the custom privacy strings: %v", err)
	}
	if err := s.data.secretSet(aiPrivacyCustomSecret, string(encoded)); err != nil {
		return errorf(CodeInternal, "cannot save the custom privacy strings: %v", err)
	}
	return nil
}

func normalizeCustomStrings(list []string) []string {
	seen := make(map[string]bool, len(list))
	out := make([]string, 0, len(list))
	for _, item := range list {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		if utf8.RuneCountInString(item) > aiPrivacyMaxCustomLen {
			item = string([]rune(item)[:aiPrivacyMaxCustomLen])
		}
		seen[item] = true
		out = append(out, item)
		if len(out) == aiPrivacyMaxCustom {
			break
		}
	}
	return out
}

// privacyGuard builds the guard for one run. Known values come from the
// current host and from every saved secret; the list is read once per run so
// a secret saved mid-conversation is protected from the next message on.
func (s *aiState) privacyGuard(settings aiPrivacySettings, host Host) (*privacy.Guard, error) {
	config := privacy.Config{
		KnownValues: settings.KnownValues,
		Secrets:     settings.Secrets,
		IPs:         settings.IPs,
		Accounts:    settings.Accounts,
		Hosts:       settings.Hosts,
		Emails:      settings.Emails,
		Custom:      settings.Custom,
	}
	if settings.KnownValues {
		known := make([]privacy.Known, 0, 16)
		add := func(value string, category privacy.Category) {
			value = strings.TrimSpace(value)
			if value == "" {
				return
			}
			known = append(known, privacy.Known{Value: value, Category: category, Boundary: true})
		}
		address := strings.TrimSpace(host.Address)
		category := privacy.CategoryHost
		if net.ParseIP(address) != nil {
			category = privacy.CategoryIP
		}
		add(address, category)
		if !privacy.DefaultExcludedUsers[strings.ToLower(strings.TrimSpace(host.User))] {
			add(host.User, privacy.CategoryUser)
		}
		add(host.Label, privacy.CategoryHost)

		names, err := s.data.vault.Names()
		if err != nil {
			return nil, errorf(CodeInternal, "cannot read the saved secrets for privacy: %v", err)
		}
		for _, name := range names {
			if strings.HasPrefix(name, "privacy:") {
				continue
			}
			value, found, err := s.data.secretGet(name)
			if err != nil {
				return nil, errorf(CodeInternal, "cannot read a saved secret for privacy: %v", err)
			}
			if !found || len(value) < 6 {
				continue
			}
			known = append(known, privacy.Known{Value: value, Category: privacy.CategorySecret, Boundary: true})
		}
		config.Known = known
	}
	if settings.Custom {
		custom, err := s.customStrings()
		if err != nil {
			return nil, err
		}
		config.CustomStrings = custom
	}
	return privacy.NewGuard(config), nil
}

// aiPrivacyProvider wraps the real provider: everything that leaves the
// device is tokenized, and every token in the answer or in tool arguments is
// restored before the agent or the user sees it.
type aiPrivacyProvider struct {
	inner     llm.Provider
	guard     *privacy.Guard
	audit     *aiPrivacyAudit
	runID     uint64
	sessionID uint32

	mu   sync.Mutex
	turn int
}

func (p *aiPrivacyProvider) Models(ctx context.Context) ([]string, error) {
	return p.inner.Models(ctx)
}

func (p *aiPrivacyProvider) Stream(ctx context.Context, request llm.Request) (<-chan llm.Event, error) {
	p.mu.Lock()
	p.turn++
	turn := p.turn
	p.mu.Unlock()

	request, replacements, preview := p.protectRequest(request)
	source, err := p.inner.Stream(ctx, request)
	if err != nil {
		return nil, err
	}
	p.audit.add(p.entry("out", turn, replacements, preview))

	events := make(chan llm.Event)
	go p.restore(ctx, source, events, turn)
	return events, nil
}

// protectRequest works on a copy: the agent keeps the unprotected history and
// every turn tokenizes it again, which keeps tokens stable and history local.
func (p *aiPrivacyProvider) protectRequest(request llm.Request) (llm.Request, []privacy.Replacement, string) {
	var replacements []privacy.Replacement
	add := func(found []privacy.Replacement) {
		replacements = mergeReplacements(replacements, found)
	}
	if request.System != "" {
		protected, found := p.guard.Protect(request.System)
		request.System = protected
		add(found)
	}
	messages := make([]llm.Message, len(request.Messages))
	copy(messages, request.Messages)
	preview := ""
	for index := range messages {
		message := &messages[index]
		if message.Text != "" {
			protected, found := p.guard.Protect(message.Text)
			message.Text = protected
			add(found)
			if message.Role == llm.RoleUser && protected != "" {
				preview = protected
			}
		}
		if len(message.ToolCalls) > 0 {
			calls := make([]llm.ToolCall, len(message.ToolCalls))
			copy(calls, message.ToolCalls)
			for call := range calls {
				protected, found := p.guard.ProtectJSON(calls[call].Arguments)
				calls[call].Arguments = protected
				add(found)
			}
			message.ToolCalls = calls
		}
	}
	request.Messages = messages
	if preview == "" {
		for index := len(messages) - 1; index >= 0; index-- {
			if messages[index].Text != "" {
				preview = messages[index].Text
				break
			}
		}
	}
	if preview == "" {
		preview = request.System
	}
	return request, replacements, privacy.Preview(preview, aiPrivacyPreviewRunes)
}

func (p *aiPrivacyProvider) restore(ctx context.Context, source <-chan llm.Event, out chan<- llm.Event, turn int) {
	defer close(out)
	restorer := p.guard.NewStreamRestorer()
	var raw strings.Builder
	var extra []privacy.Replacement
	lastKind := llm.EventText
	terminal := false

	send := func(event llm.Event) bool {
		select {
		case out <- event:
			return true
		case <-ctx.Done():
			return false
		}
	}
	flush := func() {
		if tail := restorer.Flush(); tail != "" {
			send(llm.Event{Kind: lastKind, Text: tail})
		}
	}
	record := func() {
		replacements := mergeReplacements(restorer.Replacements(), extra)
		p.audit.add(p.entry("in", turn, replacements, privacy.Preview(raw.String(), aiPrivacyPreviewRunes)))
	}

	for event := range source {
		switch event.Kind {
		case llm.EventText, llm.EventReasoning:
			lastKind = event.Kind
			if raw.Len() < 4<<10 {
				raw.WriteString(event.Text)
			}
			event.Text = restorer.Write(event.Text)
			if event.Text == "" {
				continue
			}
			if !send(event) {
				record()
				return
			}
		case llm.EventToolCall:
			if event.ToolCall != nil {
				arguments, found := p.guard.RestoreJSON(event.ToolCall.Arguments)
				event.ToolCall = &llm.ToolCall{
					ID:        event.ToolCall.ID,
					Name:      event.ToolCall.Name,
					Arguments: arguments,
				}
				extra = mergeReplacements(extra, found)
			}
			if !send(event) {
				record()
				return
			}
		case llm.EventDone, llm.EventError:
			flush()
			record()
			terminal = true
			send(event)
		default:
			if !send(event) {
				record()
				return
			}
		}
	}
	if !terminal {
		flush()
		record()
	}
}

func (p *aiPrivacyProvider) entry(direction string, turn int, replacements []privacy.Replacement, preview string) aiPrivacyAuditEntry {
	rows := make([]aiPrivacyReplacement, 0, len(replacements))
	for _, replacement := range replacements {
		rows = append(rows, aiPrivacyReplacement{
			Category: string(replacement.Category),
			Token:    replacement.Token,
			Value:    privacy.Mask(replacement.Category, replacement.Original),
		})
	}
	return aiPrivacyAuditEntry{
		Direction:    direction,
		RunID:        p.runID,
		SessionID:    p.sessionID,
		Turn:         turn,
		Preview:      preview,
		Count:        len(rows),
		Replacements: rows,
	}
}

func mergeReplacements(into, found []privacy.Replacement) []privacy.Replacement {
	for _, replacement := range found {
		duplicate := false
		for _, existing := range into {
			if existing.Token == replacement.Token {
				duplicate = true
				break
			}
		}
		if !duplicate {
			into = append(into, replacement)
		}
	}
	return into
}

// privacyInfo is the shape of ai.privacy.get and ai.privacy.save.
type aiPrivacyInfo struct {
	Settings      aiPrivacySettings `json:"settings"`
	CustomStrings []string          `json:"customStrings"`
}

func (s *aiState) privacyInfo() (aiPrivacyInfo, error) {
	custom, err := s.customStrings()
	if err != nil {
		return aiPrivacyInfo{}, err
	}
	return aiPrivacyInfo{Settings: s.settings.privacy(), CustomStrings: custom}, nil
}

func (s *aiState) privacySave(settings aiPrivacySettings, custom []string) (aiPrivacyInfo, error) {
	saved, err := s.settings.savePrivacy(settings)
	if err != nil {
		return aiPrivacyInfo{}, err
	}
	if err := s.saveCustomStrings(custom); err != nil {
		return aiPrivacyInfo{}, err
	}
	normalized, err := s.customStrings()
	if err != nil {
		return aiPrivacyInfo{}, err
	}
	return aiPrivacyInfo{Settings: saved, CustomStrings: normalized}, nil
}

// privacyPreview runs the current rules on a draft message without sending it.
func (s *aiState) privacyPreview(sessionID uint32, text string) (map[string]any, error) {
	settings := s.settings.privacy()
	var host Host
	if sessionID != 0 {
		if session, ok := s.sessions.get(sessionID); ok {
			host = session.host
		}
	}
	guard, err := s.privacyGuard(settings, host)
	if err != nil {
		return nil, err
	}
	protected, replacements := guard.Protect(text)
	rows := make([]aiPrivacyReplacement, 0, len(replacements))
	for _, replacement := range replacements {
		rows = append(rows, aiPrivacyReplacement{
			Category: string(replacement.Category),
			Token:    replacement.Token,
			Value:    privacy.Mask(replacement.Category, replacement.Original),
		})
	}
	return map[string]any{
		"enabled":      settings.Enabled,
		"text":         protected,
		"count":        len(rows),
		"replacements": rows,
	}, nil
}
