package mobilecore

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"motty/internal/sessionlog"
)

// logManager mirrors connection lifecycles into the private session journal.
// A journal that cannot be opened never blocks a connection: the manager keeps
// the error and the RPC surface reports it.
type logManager struct {
	store   *sessionlog.Store
	queue   *sessionlog.Queue
	runID   string
	openErr error

	mu      sync.Mutex
	records map[uint32]*logSession
}

type logSession struct {
	id       string
	status   sessionlog.Status
	lastKind sessionlog.EventKind
	finished bool
}

func newLogManager(dataDir string) *logManager {
	manager := &logManager{records: map[uint32]*logSession{}}
	store, err := sessionlog.Open(filepath.Join(dataDir, "session-logs.db"))
	if err != nil {
		manager.openErr = err
		return manager
	}
	runID, err := sessionlog.NewID()
	if err != nil || runID == "" {
		runID = fmt.Sprintf("run-%d", time.Now().UnixNano())
	}
	manager.store = store
	manager.runID = runID
	manager.queue = sessionlog.NewQueue(store)
	// Rows a previous process run left open are interrupted, not still active.
	_ = store.Recover(runID, time.Now().UTC())
	return manager
}

func (m *logManager) close() {
	if m == nil || m.queue == nil {
		return
	}
	_ = m.queue.Close()
	m.queue = nil
}

func (m *logManager) available() bool {
	return m != nil && m.queue != nil
}

// desktopLogKey moves a desktop session id into its own range, so a journal
// record for a remote desktop can never collide with one for a terminal tab.
func desktopLogKey(id uint32) uint32 { return id | 0x80000000 }

// beginDesktop opens a record for a VNC or RDP session. It mirrors begin,
// with the protocol taken from the host.
func (m *logManager) beginDesktop(key uint32, host Host, protocol sessionlog.Protocol) {
	if m == nil || !m.available() {
		return
	}
	id, err := sessionlog.NewID()
	if err != nil {
		return
	}
	hostName := strings.TrimSpace(host.Label)
	if hostName == "" {
		hostName = host.Address
	}
	record := sessionlog.Record{
		ID:         id,
		RunID:      m.runID,
		Host:       hostName,
		Address:    host.Address,
		Account:    host.User,
		Protocol:   protocol,
		StartedAt:  time.Now().UTC(),
		Port:       host.Port,
		CatalogID:  host.ID,
		TargetKind: sessionlog.TargetSavedHost,
	}
	if host.ID == "" {
		record.TargetKind = sessionlog.TargetUnknown
	}
	m.mu.Lock()
	m.records[key] = &logSession{id: id, status: sessionlog.StatusConnecting, lastKind: sessionlog.EventStarted}
	m.mu.Unlock()
	m.queue.Submit(sessionlog.Operation{Kind: sessionlog.OpStart, Record: record})
}

// connectedDesktop marks a desktop record active. A desktop transport is
// usable as soon as it is connected; there is no shell to open, so the
// journal gets the one event statusAfterEvent moves on.
func (m *logManager) connectedDesktop(key uint32) {
	if m == nil || !m.available() {
		return
	}
	m.mu.Lock()
	state := m.records[key]
	if state == nil || state.finished || state.status == sessionlog.StatusActive {
		m.mu.Unlock()
		return
	}
	state.status = sessionlog.StatusActive
	state.lastKind = sessionlog.EventConnected
	recordID := state.id
	m.mu.Unlock()
	m.queue.Submit(sessionlog.Operation{
		Kind:  sessionlog.OpAppendEvent,
		Event: sessionlog.Event{RecordID: recordID, Kind: sessionlog.EventConnected, At: time.Now().UTC()},
	})
}

// begin opens a record for a freshly created session.
func (m *logManager) begin(s *session) {
	if m == nil || !m.available() || s == nil {
		return
	}
	id, err := sessionlog.NewID()
	if err != nil {
		return
	}
	hostName := strings.TrimSpace(s.host.Label)
	if hostName == "" {
		hostName = s.host.Address
	}
	record := sessionlog.Record{
		ID:         id,
		RunID:      m.runID,
		Host:       hostName,
		Address:    s.host.Address,
		Account:    s.host.User,
		Protocol:   sessionlog.ProtocolSSH,
		StartedAt:  time.Now().UTC(),
		Port:       s.host.Port,
		CatalogID:  s.host.ID,
		TargetKind: sessionlog.TargetSavedHost,
	}
	if s.host.ID == "" {
		record.TargetKind = sessionlog.TargetUnknown
	}
	m.mu.Lock()
	m.records[s.id] = &logSession{id: id, status: sessionlog.StatusConnecting, lastKind: sessionlog.EventStarted}
	m.mu.Unlock()
	m.queue.Submit(sessionlog.Operation{Kind: sessionlog.OpStart, Record: record})
}

// event appends one lifecycle step. Repeated prompt events for a phase are
// collapsed, exactly like the desktop journal does.
func (m *logManager) event(sessionID uint32, kind sessionlog.EventKind) {
	if m == nil || !m.available() {
		return
	}
	m.mu.Lock()
	state := m.records[sessionID]
	if state == nil || state.finished {
		m.mu.Unlock()
		return
	}
	if (kind == sessionlog.EventHostKeyPrompt || kind == sessionlog.EventAuthenticating) && state.lastKind == kind {
		m.mu.Unlock()
		return
	}
	state.lastKind = kind
	recordID := state.id
	m.mu.Unlock()
	m.queue.Submit(sessionlog.Operation{
		Kind: sessionlog.OpAppendEvent,
		Event: sessionlog.Event{
			RecordID: recordID,
			Kind:     kind,
			At:       time.Now().UTC(),
		},
	})
}

func (m *logManager) connected(sessionID uint32) {
	if m == nil || !m.available() {
		return
	}
	m.mu.Lock()
	state := m.records[sessionID]
	if state == nil || state.finished || state.status == sessionlog.StatusActive {
		m.mu.Unlock()
		return
	}
	state.status = sessionlog.StatusActive
	state.lastKind = sessionlog.EventShellOpened
	recordID := state.id
	m.mu.Unlock()
	now := time.Now().UTC()
	// The transport connecting and the shell opening are separate steps of the
	// same handshake; both are part of what the journal proves.
	for _, kind := range []sessionlog.EventKind{sessionlog.EventConnected, sessionlog.EventShellOpened} {
		m.queue.Submit(sessionlog.Operation{
			Kind:  sessionlog.OpAppendEvent,
			Event: sessionlog.Event{RecordID: recordID, Kind: kind, At: now},
		})
	}
}

// finish writes the terminal status once; later calls do nothing.
func (m *logManager) finish(sessionID uint32, status sessionlog.Status, reason sessionlog.Reason) {
	if m == nil || !m.available() {
		return
	}
	m.mu.Lock()
	state := m.records[sessionID]
	if state == nil || state.finished {
		m.mu.Unlock()
		return
	}
	if status == sessionlog.StatusEnded && state.status != sessionlog.StatusActive {
		// A connection that never became usable was a failure, not a session
		// the user closed.
		status = sessionlog.StatusFailed
		reason = sessionlog.ReasonConnectFailed
	}
	state.finished = true
	state.status = status
	recordID := state.id
	m.mu.Unlock()
	m.queue.Submit(sessionlog.Operation{
		Kind:    sessionlog.OpFinish,
		ID:      recordID,
		Status:  status,
		EndedAt: time.Now().UTC(),
		Reason:  reason,
	})
}

// ---- journal queries ----

func logStatusName(status sessionlog.Status) string {
	switch status {
	case sessionlog.StatusConnecting:
		return "connecting"
	case sessionlog.StatusActive:
		return "active"
	case sessionlog.StatusReconnecting:
		return "reconnecting"
	case sessionlog.StatusEnded:
		return "ended"
	case sessionlog.StatusFailed:
		return "failed"
	case sessionlog.StatusInterrupted:
		return "interrupted"
	case sessionlog.StatusTrackingStopped:
		return "tracking-stopped"
	}
	return "unknown"
}

func logStatusFromName(name string) (sessionlog.Status, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "connecting":
		return sessionlog.StatusConnecting, true
	case "active":
		return sessionlog.StatusActive, true
	case "reconnecting":
		return sessionlog.StatusReconnecting, true
	case "ended":
		return sessionlog.StatusEnded, true
	case "failed":
		return sessionlog.StatusFailed, true
	case "interrupted":
		return sessionlog.StatusInterrupted, true
	case "tracking-stopped":
		return sessionlog.StatusTrackingStopped, true
	}
	return 0, false
}

func logReasonName(reason sessionlog.Reason) string {
	switch reason {
	case sessionlog.ReasonNone:
		return ""
	case sessionlog.ReasonUserClosed:
		return "user-closed"
	case sessionlog.ReasonConnectFailed:
		return "connect-failed"
	case sessionlog.ReasonAuthFailed:
		return "auth-failed"
	case sessionlog.ReasonHostKeyRejected:
		return "host-key-rejected"
	case sessionlog.ReasonCanceled:
		return "canceled"
	case sessionlog.ReasonTimeout:
		return "timeout"
	case sessionlog.ReasonNetworkLost:
		return "network-lost"
	case sessionlog.ReasonShellFailed:
		return "shell-failed"
	case sessionlog.ReasonTrackingOff:
		return "tracking-off"
	case sessionlog.ReasonProcessRestart:
		return "process-restart"
	case sessionlog.ReasonUnknown:
		return "unknown"
	}
	return ""
}

func logEventName(kind sessionlog.EventKind) string {
	switch kind {
	case sessionlog.EventConnected:
		return "connected"
	case sessionlog.EventShellOpened:
		return "shell-opened"
	case sessionlog.EventShellFailed:
		return "shell-failed"
	case sessionlog.EventDisconnected:
		return "disconnected"
	case sessionlog.EventFailed:
		return "failed"
	case sessionlog.EventReconnectScheduled:
		return "reconnect-scheduled"
	case sessionlog.EventReconnectStarted:
		return "reconnect-started"
	case sessionlog.EventReconnectExhausted:
		return "reconnect-exhausted"
	case sessionlog.EventReconnectCanceled:
		return "reconnect-canceled"
	case sessionlog.EventTrackingStopped:
		return "tracking-stopped"
	case sessionlog.EventStarted:
		return "started"
	case sessionlog.EventHostKeyPrompt:
		return "host-key-prompt"
	case sessionlog.EventAuthenticating:
		return "authenticating"
	case sessionlog.EventFinished:
		return "finished"
	case sessionlog.EventRecovered:
		return "recovered"
	}
	return "unknown"
}

type logRecordJSON struct {
	ID             string `json:"id"`
	Host           string `json:"host"`
	Address        string `json:"address,omitempty"`
	Account        string `json:"account,omitempty"`
	Protocol       string `json:"protocol"`
	StartedAt      int64  `json:"startedAt"`
	EndedAt        int64  `json:"endedAt,omitempty"`
	Status         string `json:"status"`
	Favorite       bool   `json:"favorite"`
	Reason         string `json:"reason,omitempty"`
	DurationMillis int64  `json:"durationMillis"`
	Truncated      bool   `json:"truncated,omitempty"`
}

type logEventJSON struct {
	Kind    string `json:"kind"`
	At      int64  `json:"at"`
	Attempt int    `json:"attempt,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

func logStatusTerminal(status sessionlog.Status) bool {
	switch status {
	case sessionlog.StatusEnded, sessionlog.StatusFailed, sessionlog.StatusInterrupted, sessionlog.StatusTrackingStopped:
		return true
	}
	return false
}

func logProtocolName(protocol sessionlog.Protocol) string {
	switch protocol {
	case sessionlog.ProtocolVNC:
		return "vnc"
	case sessionlog.ProtocolRDP:
		return "rdp"
	case sessionlog.ProtocolLocal:
		return "local"
	case sessionlog.ProtocolTelnet:
		return "telnet"
	case sessionlog.ProtocolSerial:
		return "serial"
	default:
		return "ssh"
	}
}

func exportLogRecord(record sessionlog.Record, now time.Time) logRecordJSON {
	out := logRecordJSON{
		ID:        record.ID,
		Host:      record.Host,
		Address:   record.Address,
		Account:   record.Account,
		Protocol:  logProtocolName(record.Protocol),
		StartedAt: record.StartedAt.UnixMilli(),
		Status:    logStatusName(record.Status),
		Favorite:  record.Favorite,
		Reason:    logReasonName(record.Reason),
		Truncated: record.Truncated,
	}
	end := record.EndedAt
	if !end.IsZero() {
		out.EndedAt = end.UnixMilli()
	} else if !logStatusTerminal(record.Status) {
		end = now
	} else {
		end = record.StartedAt
	}
	out.DurationMillis = end.Sub(record.StartedAt).Milliseconds()
	if out.DurationMillis < 0 {
		out.DurationMillis = 0
	}
	return out
}

func (a *App) logsOrError() (*logManager, error) {
	if a.logs == nil || !a.logs.available() {
		message := "the session journal is unavailable"
		if a.logs != nil && a.logs.openErr != nil {
			message = "the session journal is unavailable: " + a.logs.openErr.Error()
		}
		return nil, errorf(CodeUnavailable, "%s", message)
	}
	return a.logs, nil
}

func (a *App) callLogs(method string, params json.RawMessage) (bool, any, error) {
	if !strings.HasPrefix(method, "logs.") {
		return false, nil, nil
	}
	switch method {
	case "logs.list":
		var request struct {
			Search     string `json:"search"`
			Favorite   *bool  `json:"favorite"`
			Outcome    string `json:"outcome"`
			Status     string `json:"status"`
			Limit      int    `json:"limit"`
			CursorAt   int64  `json:"cursorStartedAt"`
			CursorID   string `json:"cursorId"`
		}
		if err := decodeParams(params, &request); err != nil {
			return true, nil, err
		}
		manager, err := a.logsOrError()
		if err != nil {
			return true, nil, err
		}
		query := sessionlog.Query{
			Sort:    sessionlog.SortStartedDesc,
			Search:  strings.TrimSpace(request.Search),
			Limit:   request.Limit,
			Favorite: request.Favorite,
		}
		switch strings.ToLower(strings.TrimSpace(request.Outcome)) {
		case "", "all":
			query.Outcome = sessionlog.OutcomeAll
		case "normal":
			query.Outcome = sessionlog.OutcomeNormal
		case "abnormal":
			query.Outcome = sessionlog.OutcomeAbnormal
		case "tracking-stopped":
			query.Outcome = sessionlog.OutcomeTrackingStopped
		default:
			return true, nil, errorf(CodeBadRequest, "unknown outcome %q", request.Outcome)
		}
		if request.Status != "" {
			status, ok := logStatusFromName(request.Status)
			if !ok {
				return true, nil, errorf(CodeBadRequest, "unknown status %q", request.Status)
			}
			query.Status = &status
		}
		if request.CursorAt > 0 {
			query.Cursor = sessionlog.Cursor{
				StartedAt: time.UnixMilli(request.CursorAt).UTC(),
				ID:        request.CursorID,
				Sort:      sessionlog.SortStartedDesc,
			}
		}
		page, err := manager.store.List(query)
		if err != nil {
			return true, nil, errorf(CodeUnavailable, "cannot read the session journal: %v", err)
		}
		now := time.Now()
		records := make([]logRecordJSON, len(page.Records))
		for index, record := range page.Records {
			records[index] = exportLogRecord(record, now)
		}
		result := map[string]any{
			"records": records,
			"matched": page.Matched,
			"total":   page.Total,
			"more":    page.More,
		}
		if page.More {
			result["nextStartedAt"] = page.Next.StartedAt.UnixMilli()
			result["nextId"] = page.Next.ID
		}
		return true, result, nil
	case "logs.events":
		var request struct {
			ID string `json:"id"`
		}
		if err := decodeParams(params, &request); err != nil {
			return true, nil, err
		}
		manager, err := a.logsOrError()
		if err != nil {
			return true, nil, err
		}
		events, err := manager.store.Events(request.ID)
		if err != nil {
			if errors.Is(err, sessionlog.ErrNotFound) {
				return true, nil, errorf(CodeNotFound, "no log record with id %q", request.ID)
			}
			return true, nil, errorf(CodeUnavailable, "cannot read the timeline: %v", err)
		}
		out := make([]logEventJSON, len(events))
		for index, event := range events {
			out[index] = logEventJSON{
				Kind:    logEventName(event.Kind),
				At:      event.At.UnixMilli(),
				Attempt: event.Attempt,
				Reason:  logReasonName(event.Reason),
			}
		}
		return true, map[string]any{"events": out}, nil
	case "logs.stats":
		manager, err := a.logsOrError()
		if err != nil {
			return true, nil, err
		}
		stats, err := manager.store.Stats()
		if err != nil {
			return true, nil, errorf(CodeUnavailable, "cannot read the journal stats: %v", err)
		}
		return true, map[string]any{
			"records":   stats.Records,
			"favorites": stats.Favorites,
			"events":    stats.Events,
			"bytes":     stats.Bytes,
		}, nil
	case "logs.favorite":
		var request struct {
			ID       string `json:"id"`
			Favorite bool   `json:"favorite"`
		}
		if err := decodeParams(params, &request); err != nil {
			return true, nil, err
		}
		manager, err := a.logsOrError()
		if err != nil {
			return true, nil, err
		}
		if err := manager.store.SetFavorite(request.ID, request.Favorite); err != nil {
			if errors.Is(err, sessionlog.ErrNotFound) {
				return true, nil, errorf(CodeNotFound, "no log record with id %q", request.ID)
			}
			return true, nil, errorf(CodeUnavailable, "cannot update the record: %v", err)
		}
		return true, map[string]bool{"favorite": request.Favorite}, nil
	case "logs.delete":
		var request struct {
			ID string `json:"id"`
		}
		if err := decodeParams(params, &request); err != nil {
			return true, nil, err
		}
		manager, err := a.logsOrError()
		if err != nil {
			return true, nil, err
		}
		if err := manager.store.Delete(request.ID); err != nil {
			return true, nil, errorf(CodeUnavailable, "cannot delete the record: %v", err)
		}
		return true, map[string]bool{"deleted": true}, nil
	case "logs.clear":
		var request struct {
			IncludeFavorites bool `json:"includeFavorites"`
		}
		if err := decodeParams(params, &request); err != nil {
			return true, nil, err
		}
		manager, err := a.logsOrError()
		if err != nil {
			return true, nil, err
		}
		if err := manager.store.Clear(request.IncludeFavorites); err != nil {
			return true, nil, errorf(CodeUnavailable, "cannot clear the journal: %v", err)
		}
		return true, map[string]bool{"cleared": true}, nil
	}
	return true, nil, errorf(CodeBadRequest, "unknown method %q", method)
}
