package mobilecore

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"motty/internal/ai/gate"
	"motty/internal/ai/tools"
)

const (
	maxListEntries = 500
	maxScreenLines = 500
)

// aiBackend carries the assistant's tool calls out on this app's sessions.
type aiBackend struct {
	manager *sessionManager
}

var _ gate.Backend = (*aiBackend)(nil)

func (b *aiBackend) session(handle string) (*session, error) {
	if handle == "" {
		b.manager.mu.Lock()
		id := b.manager.activeID
		b.manager.mu.Unlock()
		if s, ok := b.manager.get(id); ok {
			return s, nil
		}
		return nil, errors.New("no session is selected")
	}
	parsed, err := strconv.ParseUint(handle, 10, 32)
	if err != nil {
		return nil, errors.New("no such session; call list_sessions")
	}
	s, ok := b.manager.get(uint32(parsed))
	if !ok {
		return nil, errors.New("no such session; call list_sessions")
	}
	return s, nil
}

func (b *aiBackend) Sessions(context.Context) ([]gate.SessionInfo, error) {
	return b.manager.aiSessions(), nil
}

func (b *aiBackend) ReadTerminal(ctx context.Context, args tools.TerminalReadArgs) (string, error) {
	s, err := b.session(args.Session)
	if err != nil {
		return "", err
	}
	text := s.readText(args.Scope == tools.ScopeScrollback, args.Lines)
	if text == "" {
		return "(the terminal is empty)", nil
	}
	return text, nil
}

func (b *aiBackend) Exec(ctx context.Context, args tools.TerminalExecArgs) (gate.ExecResult, error) {
	s, err := b.session(args.Session)
	if err != nil {
		return gate.ExecResult{}, err
	}
	return s.execCommand(ctx, args)
}

// SFTP arrives with M3; the gate still offers the tools because the desktop
// core shares them, so answer clearly instead of failing obscurely.
var errSFTPUnavailable = errors.New("SFTP is not available on this device yet")

func (b *aiBackend) ListDir(context.Context, tools.SFTPListArgs) ([]gate.FileEntry, error) {
	return nil, errSFTPUnavailable
}

func (b *aiBackend) ReadFile(context.Context, tools.SFTPReadArgs) (string, bool, error) {
	return "", false, errSFTPUnavailable
}

func (b *aiBackend) WriteFile(context.Context, tools.SFTPWriteArgs) error {
	return errSFTPUnavailable
}

func (b *aiBackend) Remove(context.Context, tools.SFTPRemoveArgs) error {
	return errSFTPUnavailable
}

// lastLines keeps the final n lines of text.
func lastLines(text string, n int) string {
	text = strings.TrimRight(text, "\n ")
	if n <= 0 {
		return ""
	}
	lines := strings.Split(text, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// aiSessions describes every open session for list_sessions.
func (m *sessionManager) aiSessions() []gate.SessionInfo {
	m.mu.Lock()
	all := make([]*session, 0, len(m.sessions))
	for _, s := range m.sessions {
		all = append(all, s)
	}
	active := m.activeID
	m.mu.Unlock()
	out := make([]gate.SessionInfo, 0, len(all))
	for _, s := range all {
		out = append(out, gate.SessionInfo{
			ID:      strconv.FormatUint(uint64(s.id), 10),
			Title:   s.host.Label,
			Address: s.host.Address,
			Active:  s.id == active,
			Ready:   s.ready(),
		})
	}
	return out
}

// setActive marks which session the assistant should use by default.
func (m *sessionManager) setActive(id uint32) {
	m.mu.Lock()
	m.activeID = id
	m.mu.Unlock()
}
