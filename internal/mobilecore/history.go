package mobilecore

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"motty/internal/cmdhistory"
	"motty/internal/completion"
)

// historyManager owns the command history file and the built-in completion
// catalog. Like the session journal it is optional: a history file that cannot
// be opened never blocks a connection.
type historyManager struct {
	queue   *cmdhistory.Queue
	openErr error

	mu    sync.Mutex
	cache map[string][]cmdhistory.Entry

	catalogOnce sync.Once
	catalog     *completion.Catalog
	catalogErr  error
}

func newHistoryManager(dataDir string) *historyManager {
	store, err := cmdhistory.Open(filepath.Join(dataDir, "history.db"))
	if err != nil {
		return &historyManager{openErr: err}
	}
	return &historyManager{
		queue: cmdhistory.NewQueue(store),
		cache: map[string][]cmdhistory.Entry{},
	}
}

func (m *historyManager) close() {
	if m == nil || m.queue == nil {
		return
	}
	_ = m.queue.Close()
	m.queue = nil
}

func (m *historyManager) available() bool {
	return m != nil && m.queue != nil
}

// record queues one command. The queue batches disk writes on a goroutine, so
// typing never waits for SQLite.
func (m *historyManager) record(host, command string) {
	if m == nil || !m.available() || host == "" || command == "" {
		return
	}
	_ = m.queue.Record(host, command, time.Now())
	m.mu.Lock()
	delete(m.cache, host)
	m.mu.Unlock()
}

// entries returns a host's history, newest first, cached until the next
// recorded command.
func (m *historyManager) entries(host string) []cmdhistory.Entry {
	if m == nil || !m.available() {
		return nil
	}
	m.mu.Lock()
	cached, ok := m.cache[host]
	m.mu.Unlock()
	if ok {
		return cached
	}
	list, err := m.queue.List(host)
	if err != nil {
		return nil
	}
	m.mu.Lock()
	m.cache[host] = list
	m.mu.Unlock()
	return list
}

// builtin returns the embedded syntax catalog, decoded once per process.
func (m *historyManager) builtin() *completion.Catalog {
	if m == nil {
		return nil
	}
	m.catalogOnce.Do(func() {
		m.catalog, m.catalogErr = completion.Builtin()
	})
	return m.catalog
}

type historyEntryJSON struct {
	Command string `json:"command"`
	Used    int64  `json:"used"`
	Count   int    `json:"count"`
}

type completionCandidateJSON struct {
	Text   string `json:"text"`
	Insert string `json:"insert"`
	Desc   string `json:"desc,omitempty"`
	Kind   string `json:"kind"`
}

func completionKindName(kind completion.Kind) string {
	switch kind {
	case completion.KindOption:
		return "option"
	case completion.KindValue:
		return "value"
	default:
		return "command"
	}
}

// historyKey groups typed commands the same way the desktop does: the saved
// host's identity, or its address when it has none.
func (s *session) historyKey() string {
	if s.host.ID != "" {
		return s.host.ID
	}
	return s.host.Address
}

// currentLine reads the command line being edited from the screen.
func (s *session) currentLine() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminal == nil {
		return ""
	}
	line, ok := s.recorder.Line(cmdhistory.TermScreen{Term: s.terminal})
	if !ok {
		return ""
	}
	return line
}

// candidates merges the host's remembered commands with the built-in catalog,
// exactly like the desktop completion popover.
func (a *App) candidates(host, line string) ([]completionCandidateJSON, bool) {
	out := make([]completionCandidateJSON, 0, 16)
	seen := make(map[string]struct{}, 16)
	add := func(candidate completion.Candidate) {
		if len(out) >= completion.MaxCandidates || candidate.Insert == "" {
			return
		}
		if _, ok := seen[candidate.Insert]; ok {
			return
		}
		seen[candidate.Insert] = struct{}{}
		out = append(out, completionCandidateJSON{
			Text:   candidate.Text,
			Insert: candidate.Insert,
			Desc:   candidate.Desc,
			Kind:   completionKindName(candidate.Kind),
		})
	}
	if line != "" && !cmdhistory.Secret(line) {
		for _, rest := range cmdhistory.SuggestAll(a.history.entries(host), line, 5) {
			add(completion.Candidate{Text: line + rest, Insert: rest, Kind: completion.KindCommand})
		}
	}
	suppressed := false
	if catalog := a.history.builtin(); catalog != nil {
		result := completion.Complete(catalog, line)
		suppressed = result.Suppressed
		if !result.Suppressed {
			for _, candidate := range result.Candidates {
				add(candidate)
			}
		}
	}
	return out, suppressed
}

func (a *App) callHistory(method string, params json.RawMessage) (bool, any, error) {
	if !strings.HasPrefix(method, "history.") && !strings.HasPrefix(method, "completion.") {
		return false, nil, nil
	}
	switch method {
	case "history.list":
		var request struct {
			HostID string `json:"hostId"`
			Limit  int    `json:"limit"`
		}
		if err := decodeParams(params, &request); err != nil {
			return true, nil, err
		}
		if a.history == nil || !a.history.available() {
			message := "the command history is unavailable"
			if a.history != nil && a.history.openErr != nil {
				message += ": " + a.history.openErr.Error()
			}
			return true, nil, errorf(CodeUnavailable, "%s", message)
		}
		entries := a.history.entries(request.HostID)
		if request.Limit <= 0 || request.Limit > 2000 {
			request.Limit = 500
		}
		if len(entries) > request.Limit {
			entries = entries[:request.Limit]
		}
		out := make([]historyEntryJSON, len(entries))
		for index, entry := range entries {
			out[index] = historyEntryJSON{
				Command: entry.Command,
				Used:    entry.Used.UnixMilli(),
				Count:   entry.Count,
			}
		}
		return true, map[string]any{"entries": out}, nil
	case "history.delete":
		var request struct {
			HostID  string `json:"hostId"`
			Command string `json:"command"`
		}
		if err := decodeParams(params, &request); err != nil {
			return true, nil, err
		}
		if a.history == nil || !a.history.available() {
			return true, nil, errorf(CodeUnavailable, "the command history is unavailable")
		}
		if request.Command == "" {
			return true, nil, errorf(CodeBadRequest, "a command is required")
		}
		if err := a.history.queue.Delete(request.HostID, request.Command); err != nil {
			return true, nil, errorf(CodeUnavailable, "cannot delete the command: %v", err)
		}
		a.history.mu.Lock()
		delete(a.history.cache, request.HostID)
		a.history.mu.Unlock()
		return true, map[string]bool{"deleted": true}, nil
	case "history.clear":
		if a.history == nil || !a.history.available() {
			return true, nil, errorf(CodeUnavailable, "the command history is unavailable")
		}
		if err := a.history.queue.Clear(); err != nil {
			return true, nil, errorf(CodeUnavailable, "cannot clear the history: %v", err)
		}
		a.history.mu.Lock()
		a.history.cache = map[string][]cmdhistory.Entry{}
		a.history.mu.Unlock()
		return true, map[string]bool{"cleared": true}, nil
	case "completion.complete":
		var request struct {
			SessionID uint32 `json:"sessionId"`
			Line      string `json:"line"`
		}
		if err := decodeParams(params, &request); err != nil {
			return true, nil, err
		}
		target, err := a.session(request.SessionID)
		if err != nil {
			return true, nil, err
		}
		line := request.Line
		derived := false
		if line == "" {
			line = target.currentLine()
			derived = true
		}
		if strings.ContainsAny(line, "\r\n") {
			return true, map[string]any{"line": "", "candidates": []completionCandidateJSON{}, "suppressed": true}, nil
		}
		candidates, suppressed := a.candidates(target.historyKey(), line)
		result := map[string]any{
			"line":       line,
			"candidates": candidates,
			"suppressed": suppressed,
			"derived":    derived,
		}
		return true, result, nil
	}
	return true, nil, errorf(CodeBadRequest, "unknown method %q", method)
}
