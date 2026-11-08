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
	// "" is the cross-host list, which this command has also changed.
	delete(m.cache, "")
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
	case completion.KindCombo:
		return "combo"
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

// atCommandLine reports whether the terminal is on a plain shell command
// line. It is the gate for a line tracked on the client: the screen no longer
// has to echo the line first, but a password prompt or a full-screen program
// still gets no suggestions.
func (s *session) atCommandLine() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminal == nil {
		return false
	}
	return s.recorder.CommandLine(cmdhistory.TermScreen{Term: s.terminal})
}

// How many history rows to offer. On an empty prompt the recent commands are
// the whole answer; after typing a prefix a handful of matches is enough.
const (
	historyEmptyLimit = 8
	historyLineLimit  = 5
)

// candidates merges the host's remembered commands, the curated parameter
// combinations and the built-in catalogs. Descriptions come back in locale.
func (a *App) candidates(host, line, locale string) ([]completionCandidateJSON, bool) {
	out := make([]completionCandidateJSON, 0, 16)
	seen := make(map[string]struct{}, 16)
	add := func(candidate completion.Candidate) bool {
		if len(out) >= completion.MaxCandidates || candidate.Insert == "" {
			return false
		}
		if _, ok := seen[candidate.Insert]; ok {
			return false
		}
		seen[candidate.Insert] = struct{}{}
		out = append(out, completionCandidateJSON{
			Text:   candidate.Text,
			Insert: candidate.Insert,
			Desc:   completion.LocalizeDesc(candidate.Desc, locale),
			Kind:   completionKindName(candidate.Kind),
		})
		return true
	}
	// history adds a host's remembered commands: whole commands at an empty
	// prompt, or the rest of the line after what has been typed. When the host
	// has nothing to offer, commands from other hosts fill in.
	history := func(entries []cmdhistory.Entry, limit int) int {
		added := 0
		if line == "" {
			for _, entry := range entries {
				if added >= limit {
					break
				}
				if cmdhistory.Secret(entry.Command) {
					continue
				}
				if add(completion.Candidate{
					Text:   entry.Command,
					Insert: entry.Command,
					Desc:   "From your history",
					Kind:   completion.KindCommand,
				}) {
					added++
				}
			}
			return added
		}
		for _, rest := range cmdhistory.SuggestRanked(entries, line, limit) {
			if add(completion.Candidate{
				Text:   line + rest,
				Insert: rest,
				Desc:   "From your history",
				Kind:   completion.KindCommand,
			}) {
				added++
			}
		}
		return added
	}
	// historyCommands only completes the first word of remembered commands.
	// While the command itself is still being typed, arguments must not be
	// inserted.
	historyCommands := func(entries []cmdhistory.Entry, limit int) int {
		added := 0
		for _, rest := range cmdhistory.SuggestCommands(entries, line, limit) {
			if add(completion.Candidate{
				Text:   line + rest,
				Insert: rest,
				Desc:   "From your history",
				Kind:   completion.KindCommand,
			}) {
				added++
			}
		}
		return added
	}
	// addCatalogs merges the imported snapshot and the hand-written
	// essentials. It reports whether the line is syntax the snapshot does not
	// understand.
	addCatalogs := func() bool {
		catalog := a.history.builtin()
		if catalog == nil {
			return false
		}
		result := completion.Complete(catalog, line)
		if result.Suppressed {
			return true
		}
		for _, candidate := range result.Candidates {
			add(candidate)
		}
		// Essentials covers commands the imported snapshot lacks: net-tools,
		// iproute2, packet capture, database clients and the POSIX shell
		// builtins.
		if essentials := completion.Essentials(); essentials != nil {
			extra := completion.Complete(essentials, line)
			if !extra.Suppressed {
				for _, candidate := range extra.Candidates {
					add(candidate)
				}
			}
		}
		return false
	}

	if line == "" {
		// The alphabetical catalog root list is noise on an empty prompt; the
		// commands this host actually runs are the useful answer.
		if history(a.history.entries(host), historyEmptyLimit) == 0 {
			history(a.history.entries(""), historyEmptyLimit)
		}
		return out, false
	}

	// Until the first space the line is still the command word, even when it
	// happens to name a complete command: "ls" may still become "lsof" or
	// "lsblk". Only command names belong here; parameters and combinations
	// wait for the space.
	if !strings.ContainsAny(line, " \t") {
		if !cmdhistory.Secret(line) {
			// An unfinished command word is a lookup: every remembered command
			// that matches belongs in the list, not just the top few.
			if historyCommands(a.history.entries(host), completion.MaxCandidates) == 0 {
				historyCommands(a.history.entries(""), completion.MaxCandidates)
			}
		}
		return out, addCatalogs()
	}

	if !cmdhistory.Secret(line) {
		if history(a.history.entries(host), historyLineLimit) == 0 {
			history(a.history.entries(""), historyLineLimit)
		}
	}

	// The curated combinations answer "what do people usually type here" and
	// carry an explanation. A bare "-" or "--" is an explicit request for the
	// option table, so combinations step aside and the catalog answers; once
	// an option name has characters, both are useful.
	lastWord := line
	if index := strings.LastIndexByte(line, ' '); index >= 0 {
		lastWord = line[index+1:]
	}
	if lastWord != "-" && lastWord != "--" {
		for _, candidate := range completion.Combos(line, completion.MaxCandidates) {
			add(candidate)
		}
	}
	return out, addCatalogs()
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
			Typed     string `json:"typed"`
			Locale    string `json:"locale"`
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
		if line == "" && request.Typed != "" && target.atCommandLine() {
			// The client follows the line as it is typed. Using it directly
			// makes suggestions immediate on a slow link and works with shells
			// whose autosuggestion text perturbs the screen.
			line = request.Typed
		} else if line == "" {
			line = target.currentLine()
			derived = true
		}
		if strings.ContainsAny(line, "\r\n") {
			return true, map[string]any{"line": "", "candidates": []completionCandidateJSON{}, "suppressed": true}, nil
		}
		candidates, suppressed := a.candidates(target.historyKey(), line, request.Locale)
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
