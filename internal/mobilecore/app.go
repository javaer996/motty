package mobilecore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Config tells Start where the application keeps its files.
type Config struct {
	// DataDir is the writable application directory: hosts, known_hosts and
	// the assistant database live under it.
	DataDir string
	// Version is the marketing version the front end shows.
	Version string
}

// App is the whole application core. One per process.
type App struct {
	cfg      Config
	data     *dataStore
	hosts    *hostStore
	keys     *keyStore
	sessions *sessionManager
	files    *filesRegistry
	forwards *forwardManager
	logs     *logManager
	history  *historyManager
	desktops *desktopManager
	sync     *syncManager
	events   *eventQueue
	ai       *aiState
	started  time.Time
}

// Start opens the data directory and returns the core. It does not dial
// anything; sessions start when the front end opens one.
func Start(cfg Config) (*App, error) {
	if strings.TrimSpace(cfg.DataDir) == "" {
		return nil, errorf(CodeBadRequest, "a data directory is required")
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, errorf(CodeInternal, "cannot create the data directory: %v", err)
	}
	data, err := openDataStore(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	keys, err := openKeyStore(data)
	if err != nil {
		data.close()
		return nil, err
	}
	identityRefs, err := keys.migrateLegacyFiles()
	if err != nil {
		data.close()
		return nil, err
	}
	if _, err := migrateLegacyHosts(data, identityRefs); err != nil {
		data.close()
		return nil, err
	}
	hosts, err := openHostStore(data)
	if err != nil {
		data.close()
		return nil, err
	}
	events := newEventQueue(4096)
	sessions := newSessionManager(hosts, events, cfg.DataDir)
	sessions.keys = keys
	sessions.data = data
	files := newFilesRegistry(cfg.DataDir)
	sessions.files = files
	forwards, err := newForwardManager(cfg.DataDir, sessions)
	if err != nil {
		data.close()
		return nil, err
	}
	sessions.forwards = forwards
	logs := newLogManager(cfg.DataDir)
	history := newHistoryManager(cfg.DataDir)
	sessions.logs = logs
	sessions.history = history
	desktops := newDesktopManager(cfg.DataDir, data, sessions, logs, events)
	app := &App{
		cfg:      cfg,
		data:     data,
		hosts:    hosts,
		keys:     keys,
		sessions: sessions,
		files:    files,
		forwards: forwards,
		logs:     logs,
		history:  history,
		desktops: desktops,
		events:   events,
		ai:       newAIState(data, sessions, events),
		started:  time.Now(),
	}
	app.sync = newSyncManager(app)
	sessions.changed = app.requestSync
	desktops.changed = app.requestSync
	return app, nil
}

// Call runs one control method and returns the JSON envelope the front end
// parses. It never panics: a bug becomes an internal error, not a crash.
func (a *App) Call(method string, params json.RawMessage) (data []byte) {
	defer func() {
		if recovered := recover(); recovered != nil {
			data = mustEnvelope(Envelope{
				OK:    false,
				Error: &RPCError{Code: CodeInternal, Message: "internal error: " + toString(recovered)},
			})
		}
	}()
	result, err := a.call(method, params)
	if err != nil {
		var rpcErr *RPCError
		if !errors.As(err, &rpcErr) {
			rpcErr = &RPCError{Code: CodeInternal, Message: err.Error()}
		}
		return mustEnvelope(Envelope{OK: false, Error: rpcErr})
	}
	return mustEnvelope(Envelope{OK: true, Result: result})
}

func mustEnvelope(envelope Envelope) []byte {
	data, err := json.Marshal(envelope)
	if err != nil {
		return []byte(`{"ok":false,"error":{"code":"internal","message":"cannot encode the response"}}`)
	}
	return data
}

func toString(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	if err, ok := value.(error); ok {
		return err.Error()
	}
	return "unknown failure"
}

func (a *App) call(method string, params json.RawMessage) (any, error) {
	switch method {
	case "app.info":
		return a.info(), nil
	case "hosts.list":
		return map[string]any{"hosts": a.hosts.list()}, nil
	case "hosts.get":
		var request struct {
			ID string `json:"id"`
		}
		if err := decodeParams(params, &request); err != nil {
			return nil, err
		}
		host, ok := a.hosts.get(request.ID)
		if !ok {
			return nil, errorf(CodeNotFound, "no host with id %q", request.ID)
		}
		return host, nil
	case "hosts.save":
		var host Host
		if err := decodeParams(params, &host); err != nil {
			return nil, err
		}
		saved, err := a.hosts.save(host)
		if err == nil {
			if saved.IsSSH() && (saved.Auth == AuthKey || saved.Auth == AuthAgent) {
				// The host no longer signs in with a password; the old one is
				// dead weight in the vault and in every backup.
				_ = a.data.secretDelete(hostSecretName(saved.ID, hostPasswordPart))
			}
			a.requestSync()
		}
		return saved, err
	case "hosts.delete":
		var request struct {
			ID string `json:"id"`
		}
		if err := decodeParams(params, &request); err != nil {
			return nil, err
		}
		host, found := a.hosts.get(request.ID)
		err := a.hosts.delete(request.ID)
		if err == nil {
			_ = a.data.secretDelete(hostSecretName(request.ID, hostPasswordPart))
			_ = a.data.secretDelete(hostSecretName(request.ID, hostOTPPart))
			if found {
				_ = a.data.secretDelete(hostProxyPasswordSecret(request.ID, host.Proxy))
			}
			a.requestSync()
		}
		return map[string]bool{"deleted": true}, err
	case "hosts.passwordStatus":
		var request struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		}
		if err := decodeParams(params, &request); err != nil {
			return nil, err
		}
		host, ok := a.hosts.get(request.ID)
		if !ok {
			return nil, errorf(CodeNotFound, "no host with id %q", request.ID)
		}
		name, err := hostSecretFor(host, request.Kind)
		if err != nil {
			return nil, err
		}
		_, found, err := a.data.secretGet(name)
		return map[string]bool{"saved": found}, err
	case "hosts.password":
		var request struct {
			ID       string `json:"id"`
			Password string `json:"password"`
			Remove   bool   `json:"remove"`
			Kind     string `json:"kind"`
		}
		if err := decodeParams(params, &request); err != nil {
			return nil, err
		}
		host, ok := a.hosts.get(request.ID)
		if !ok {
			return nil, errorf(CodeNotFound, "no host with id %q", request.ID)
		}
		name, err := hostSecretFor(host, request.Kind)
		if err != nil {
			return nil, err
		}
		if request.Remove {
			err = a.data.secretDelete(name)
		} else {
			if request.Password == "" {
				return nil, errorf(CodeBadRequest, "the password is empty")
			}
			err = a.data.secretSet(name, request.Password)
		}
		if err == nil {
			a.requestSync()
		}
		return map[string]bool{"saved": !request.Remove}, err
	case "hosts.groups":
		return map[string]any{"groups": a.hosts.groups()}, nil
	case "hosts.importSSHConfig":
		var request struct {
			Path string `json:"path"`
		}
		if err := decodeParams(params, &request); err != nil {
			return nil, err
		}
		if strings.TrimSpace(request.Path) == "" {
			return nil, errorf(CodeBadRequest, "a path is required")
		}
		imported, rawWarnings, err := a.hosts.importSSHConfig(request.Path)
		if err != nil {
			return nil, err
		}
		if len(imported) > 0 {
			a.requestSync()
		}
		return map[string]any{"hosts": imported, "warnings": exportWarnings(rawWarnings)}, nil
	case "keys.list":
		return map[string]any{"keys": a.keys.list()}, nil
	case "keys.import":
		var request struct {
			Name      string `json:"name"`
			Data      string `json:"data"`
			Overwrite bool   `json:"overwrite"`
		}
		if err := decodeParams(params, &request); err != nil {
			return nil, err
		}
		decoded, err := base64.StdEncoding.DecodeString(request.Data)
		if err != nil {
			return nil, errorf(CodeBadRequest, "the key is not base64: %v", err)
		}
		info, err := a.keys.importKey(request.Name, decoded, request.Overwrite)
		if err == nil {
			a.requestSync()
		}
		return info, err
	case "keys.delete":
		var request struct {
			Name string `json:"name"`
		}
		if err := decodeParams(params, &request); err != nil {
			return nil, err
		}
		err := a.keys.delete(request.Name)
		if err == nil {
			a.requestSync()
		}
		return map[string]bool{"deleted": true}, err
	case "ai.settings.get":
		return a.ai.settings.get(), nil
	case "ai.settings.save":
		var settings aiSettings
		if err := decodeParams(params, &settings); err != nil {
			return nil, err
		}
		saved, err := a.ai.settings.save(settings)
		if err == nil {
			a.requestSync()
		}
		return saved, err
	case "ai.privacy.get":
		return a.ai.privacyInfo()
	case "ai.privacy.save":
		var request struct {
			Settings      aiPrivacySettings `json:"settings"`
			CustomStrings []string          `json:"customStrings"`
		}
		if err := decodeParams(params, &request); err != nil {
			return nil, err
		}
		info, err := a.ai.privacySave(request.Settings, request.CustomStrings)
		if err == nil {
			a.requestSync()
		}
		return info, err
	case "ai.privacy.log":
		var request struct {
			Limit int `json:"limit"`
		}
		if err := decodeParams(params, &request); err != nil {
			return nil, err
		}
		return map[string]any{"entries": a.ai.privacy.list(request.Limit)}, nil
	case "ai.privacy.clear":
		return map[string]bool{"cleared": true}, a.ai.privacy.clear()
	case "ai.privacy.preview":
		var request struct {
			SessionID uint32 `json:"sessionId"`
			Text      string `json:"text"`
		}
		if err := decodeParams(params, &request); err != nil {
			return nil, err
		}
		return a.ai.privacyPreview(request.SessionID, request.Text)
	case "ai.models":
		settings := a.ai.settings.get()
		// The user may list models before saving the form, for example right
		// after typing a new key, so the call may carry a draft: it is used for
		// this request only and never written to disk. An omitted field keeps
		// the saved value of the provider being edited.
		var draft struct {
			ID       string  `json:"id"`
			Provider string  `json:"provider"`
			BaseURL  *string `json:"baseUrl"`
			APIKey   *string `json:"apiKey"`
		}
		if err := decodeParams(params, &draft); err != nil {
			return nil, err
		}
		current, _ := settings.current()
		if id := strings.TrimSpace(draft.ID); id != "" && id != current.ID {
			// The form is editing another provider; its own fields are the
			// whole truth, and the active provider's key must not leak in.
			current = aiProvider{ID: id}
		}
		if provider := strings.ToLower(strings.TrimSpace(draft.Provider)); provider != "" {
			current.Provider = provider
		}
		if draft.BaseURL != nil {
			current.BaseURL = strings.TrimSpace(*draft.BaseURL)
		}
		if draft.APIKey != nil {
			current.APIKey = strings.TrimSpace(*draft.APIKey)
		}
		if current.Provider == "" {
			current.Provider = "openai"
		}
		provider, err := a.ai.provider(current)
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		models, err := provider.Models(ctx)
		if err != nil {
			return nil, errorf(CodeUnavailable, "cannot list models: %v", err)
		}
		sort.Strings(models)
		return map[string]any{"models": models}, nil
	case "ai.send":
		var request struct {
			SessionID uint32 `json:"sessionId"`
			Text      string `json:"text"`
		}
		if err := decodeParams(params, &request); err != nil {
			return nil, err
		}
		a.sessions.setActive(request.SessionID)
		runID, err := a.ai.send(request.SessionID, request.Text)
		if err != nil {
			return nil, err
		}
		return map[string]any{"runId": runID}, nil
	case "ai.cancel":
		var request struct {
			RunID uint64 `json:"runId"`
		}
		if err := decodeParams(params, &request); err != nil {
			return nil, err
		}
		return map[string]bool{"cancelled": true}, a.ai.cancel(request.RunID)
	case "ai.approve":
		var request struct {
			RunID       uint64 `json:"runId"`
			ApprovalID  string `json:"approvalId"`
			Approved    bool   `json:"approved"`
			ConfirmText string `json:"confirmText"`
			Always      bool   `json:"always"`
		}
		if err := decodeParams(params, &request); err != nil {
			return nil, err
		}
		return map[string]bool{"answered": true}, a.ai.approve(request.RunID, request.ApprovalID, request.Approved, request.ConfirmText, request.Always)
	case "ai.reset":
		var request struct {
			SessionID uint32 `json:"sessionId"`
		}
		if err := decodeParams(params, &request); err != nil {
			return nil, err
		}
		session, ok := a.sessions.get(request.SessionID)
		if !ok {
			return nil, errorf(CodeNotFound, "no session with id %d", request.SessionID)
		}
		a.ai.reset(session.host.ID)
		return map[string]bool{"reset": true}, nil
	case "ai.conversations":
		var request struct {
			SessionID uint32 `json:"sessionId"`
		}
		if err := decodeParams(params, &request); err != nil {
			return nil, err
		}
		session, ok := a.sessions.get(request.SessionID)
		if !ok {
			return nil, errorf(CodeNotFound, "no session with id %d", request.SessionID)
		}
		conversations, err := a.ai.conversations(session.host.ID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"conversations": conversations}, nil
	case "ai.conversation.get":
		var request struct {
			SessionID      uint32 `json:"sessionId"`
			ConversationID int64  `json:"conversationId"`
		}
		if err := decodeParams(params, &request); err != nil {
			return nil, err
		}
		session, ok := a.sessions.get(request.SessionID)
		if !ok {
			return nil, errorf(CodeNotFound, "no session with id %d", request.SessionID)
		}
		return a.ai.conversation(session.host.ID, request.ConversationID)
	case "ai.conversation.delete":
		var request struct {
			SessionID      uint32 `json:"sessionId"`
			ConversationID int64  `json:"conversationId"`
		}
		if err := decodeParams(params, &request); err != nil {
			return nil, err
		}
		session, ok := a.sessions.get(request.SessionID)
		if !ok {
			return nil, errorf(CodeNotFound, "no session with id %d", request.SessionID)
		}
		active, err := a.ai.deleteConversation(session.host.ID, request.ConversationID)
		if err != nil {
			return nil, err
		}
		return map[string]bool{"deleted": true, "active": active}, nil
	case "sessions.open":
		var request struct {
			HostID string `json:"hostId"`
			Cols   int    `json:"cols"`
			Rows   int    `json:"rows"`
		}
		if err := decodeParams(params, &request); err != nil {
			return nil, err
		}
		return a.sessions.open(request.HostID, request.Cols, request.Rows)
	case "sessions.close":
		var request struct {
			ID uint32 `json:"id"`
		}
		if err := decodeParams(params, &request); err != nil {
			return nil, err
		}
		return map[string]bool{"closed": true}, a.sessions.close(request.ID)
	case "sessions.list":
		return map[string]any{"sessions": a.sessions.list()}, nil
	case "sessions.reconnect":
		var request struct {
			ID uint32 `json:"id"`
		}
		if err := decodeParams(params, &request); err != nil {
			return nil, err
		}
		session, err := a.session(request.ID)
		if err != nil {
			return nil, err
		}
		a.sessions.setActive(request.ID)
		return session.reconnect()
	case "sessions.input":
		var request struct {
			ID   uint32 `json:"id"`
			Data string `json:"data"`
		}
		if err := decodeParams(params, &request); err != nil {
			return nil, err
		}
		session, err := a.session(request.ID)
		if err != nil {
			return nil, err
		}
		a.sessions.setActive(request.ID)
		decoded, err := base64.StdEncoding.DecodeString(request.Data)
		if err != nil {
			return nil, errorf(CodeBadRequest, "input is not base64: %v", err)
		}
		return map[string]bool{"sent": true}, session.input(decoded)
	case "sessions.resize":
		var request struct {
			ID   uint32 `json:"id"`
			Cols int    `json:"cols"`
			Rows int    `json:"rows"`
		}
		if err := decodeParams(params, &request); err != nil {
			return nil, err
		}
		session, err := a.session(request.ID)
		if err != nil {
			return nil, err
		}
		return map[string]bool{"resized": true}, session.resize(request.Cols, request.Rows)
	case "sessions.answer":
		var request struct {
			ID uint32 `json:"id"`
			answerRequest
		}
		if err := decodeParams(params, &request); err != nil {
			return nil, err
		}
		session, err := a.session(request.ID)
		if err != nil {
			return nil, err
		}
		return map[string]bool{"answered": true}, session.answer(request.answerRequest)
	case "sessions.scroll":
		var request struct {
			ID    uint32 `json:"id"`
			Lines int    `json:"lines"`
		}
		if err := decodeParams(params, &request); err != nil {
			return nil, err
		}
		session, err := a.session(request.ID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"offset": session.scroll(request.Lines)}, nil
	case "sessions.scrollTo":
		var request struct {
			ID     uint32 `json:"id"`
			Offset int    `json:"offset"`
		}
		if err := decodeParams(params, &request); err != nil {
			return nil, err
		}
		session, err := a.session(request.ID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"offset": session.scrollTo(request.Offset)}, nil
	default:
		if handled, result, err := a.callSync(method, params); handled {
			return result, err
		}
		if handled, result, err := a.callBackup(method, params); handled {
			return result, err
		}
		if handled, result, err := a.callDesktop(method, params); handled {
			return result, err
		}
		if handled, result, err := a.callForwards(method, params); handled {
			return result, err
		}
		if handled, result, err := a.callLogs(method, params); handled {
			return result, err
		}
		if handled, result, err := a.callHistory(method, params); handled {
			return result, err
		}
		if handled, result, err := a.callFiles(method, params); handled {
			return result, err
		}
		return nil, errorf(CodeBadRequest, "unknown method %q", method)
	}
}

func (a *App) session(id uint32) (*session, error) {
	s, ok := a.sessions.get(id)
	if !ok {
		return nil, errorf(CodeNotFound, "no session with id %d", id)
	}
	return s, nil
}

func (a *App) info() map[string]any {
	return map[string]any{
		"version":       a.cfg.Version,
		"dataDir":       a.cfg.DataDir,
		"platform":      runtime.GOOS + "/" + runtime.GOARCH,
		"goVersion":     runtime.Version(),
		"startedAt":     a.started.Unix(),
		"eventsDropped": a.events.droppedCount(),
	}
}

// PollEvent copies the oldest event into dst. It returns 0 when the queue is
// empty and -needed when dst is too small.
func (a *App) PollEvent(dst []byte) int {
	return a.events.poll(dst)
}

// ScreenSnapshot writes a terminal snapshot into dst. See session.snapshot.
func (a *App) ScreenSnapshot(sessionID uint32, sinceVersion uint64, dst []byte) int {
	session, ok := a.sessions.get(sessionID)
	if !ok {
		return -1
	}
	return session.snapshot(dst, sinceVersion)
}

// DesktopFrame writes the remote desktop's changes into dst. It returns 0
// when the front end already has this version, -needed when dst is too small,
// and -1 for an unknown desktop. See desktopFrame.snapshot for the layout.
func (a *App) DesktopFrame(desktopID uint32, sinceVersion uint64, dst []byte) int {
	d, err := a.desktops.get(desktopID)
	if err != nil {
		return -1
	}
	return d.frame.snapshot(sinceVersion, dst)
}

// Close ends every session and releases what the core opened.
func (a *App) Close() {
	if a.sync != nil {
		a.sync.close()
	}
	a.ai.close()
	a.desktops.closeAll()
	a.sessions.shutdown()
	a.files.closeAll()
	a.forwards.closeAll()
	a.history.close()
	a.logs.close()
	a.data.close()
}

// groups returns the distinct group paths, parents before children.
func (s *hostStore) groups() []string {
	seen := map[string]bool{}
	s.mu.RLock()
	for _, host := range s.hosts {
		if host.Group == "" {
			continue
		}
		parts := strings.Split(host.Group, "/")
		for i := range parts {
			seen[strings.Join(parts[:i+1], "/")] = true
		}
	}
	s.mu.RUnlock()
	out := make([]string, 0, len(seen))
	for group := range seen {
		out = append(out, group)
	}
	sort.Strings(out)
	return out
}

// dataDir is where the known_hosts and hosts files live, exposed for tests.
func (a *App) dataDir() string {
	return filepath.Clean(a.cfg.DataDir)
}
