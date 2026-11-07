package mobilecore

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"motty/internal/portforward"
	"motty/internal/sshconn"
)

const (
	forwardMaxStreams = 16
	forwardDialWait   = 10 * time.Second
)

// forwardConfig is one saved tunnel. It names the host, not a session: iOS
// opens one tab per host, and a tunnel can start before or after that tab.
type forwardConfig struct {
	ID          string `json:"id"`
	Label       string `json:"label,omitempty"`
	HostID      string `json:"hostId"`
	Mode        string `json:"mode,omitempty"`
	LocalPort   uint16 `json:"localPort"`
	Destination string `json:"destination,omitempty"`
	AutoStart   bool   `json:"autoStart,omitempty"`
}

// forwardState is the live view the front end polls.
type forwardState struct {
	ID            string `json:"id"`
	Label         string `json:"label,omitempty"`
	HostID        string `json:"hostId"`
	Mode          string `json:"mode"`
	ListenAddress string `json:"listenAddress"`
	Destination   string `json:"destination,omitempty"`
	AutoStart     bool   `json:"autoStart"`
	SessionID     uint32 `json:"sessionId,omitempty"`
	State         string `json:"state"`
	Error         string `json:"error,omitempty"`
	Active        int    `json:"active"`
	Accepted      uint64 `json:"accepted"`
	Uploaded      uint64 `json:"uploaded"`
	Downloaded    uint64 `json:"downloaded"`
}

type forwardRuntime struct {
	forward   *portforward.Forward
	sessionID uint32
	starting  bool
	err       string
}

// forwardManager owns saved forwards and the listeners they started. The SSH
// session they run through is looked up on demand; stopping a session stops
// every forward bound to it.
type forwardManager struct {
	path     string
	sessions *sessionManager

	mu       sync.Mutex
	configs  map[string]*forwardConfig
	order    []string
	runtimes map[string]*forwardRuntime
	nextID   uint64
	closed   bool
}

func newForwardManager(dataDir string, sessions *sessionManager) (*forwardManager, error) {
	m := &forwardManager{
		path:     filepath.Join(dataDir, "forwards.json"),
		sessions: sessions,
		configs:  map[string]*forwardConfig{},
		runtimes: map[string]*forwardRuntime{},
	}
	data, err := os.ReadFile(m.path)
	if err != nil {
		if os.IsNotExist(err) {
			return m, nil
		}
		return nil, errorf(CodeInternal, "cannot read the forwards file: %v", err)
	}
	var stored struct {
		Forwards []forwardConfig `json:"forwards"`
	}
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, errorf(CodeInternal, "cannot parse the forwards file: %v", err)
	}
	for index := range stored.Forwards {
		config := stored.Forwards[index]
		if _, name, err := forwardModeName(config.Mode); err == nil {
			config.Mode = name
		}
		if config.ID == "" {
			m.nextID++
			config.ID = fmt.Sprintf("forward-%d", m.nextID)
		}
		if number, ok := forwardIDNumber(config.ID); ok && number > m.nextID {
			m.nextID = number
		}
		m.configs[config.ID] = &config
		m.order = append(m.order, config.ID)
	}
	return m, nil
}

func forwardIDNumber(id string) (uint64, bool) {
	text, ok := strings.CutPrefix(id, "forward-")
	if !ok {
		return 0, false
	}
	number, err := strconv.ParseUint(text, 10, 64)
	return number, err == nil
}

func forwardModeName(name string) (portforward.Mode, string, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "local":
		return portforward.ModeLocal, "local", nil
	case "remote":
		return portforward.ModeRemote, "remote", nil
	case "socks5", "socks":
		return portforward.ModeSOCKS5, "socks5", nil
	case "http-connect", "httpconnect", "http":
		return portforward.ModeHTTPConnect, "http-connect", nil
	}
	return 0, "", errorf(CodeBadRequest, "unknown forward mode %q", name)
}

func forwardModeForName(name string) (portforward.Mode, error) {
	mode, _, err := forwardModeName(name)
	return mode, err
}

func forwardListenAddress(config *forwardConfig) string {
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(int(config.LocalPort)))
}

func forwardDestination(config *forwardConfig, mode portforward.Mode) string {
	if mode != portforward.ModeLocal && mode != portforward.ModeRemote {
		return ""
	}
	return config.Destination
}

// validateForward checks and normalizes a config from the front end.
func validateForward(config forwardConfig) (forwardConfig, portforward.Mode, error) {
	mode, name, err := forwardModeName(config.Mode)
	if err != nil {
		return config, 0, err
	}
	config.Mode = name
	config.HostID = strings.TrimSpace(config.HostID)
	config.Label = strings.TrimSpace(config.Label)
	if config.HostID == "" {
		return config, 0, errorf(CodeBadRequest, "a host is required")
	}
	if config.LocalPort == 0 {
		return config, 0, errorf(CodeBadRequest, "a port between 1 and 65535 is required")
	}
	config.Destination = strings.TrimSpace(config.Destination)
	if mode == portforward.ModeLocal || mode == portforward.ModeRemote {
		host, portText, splitErr := net.SplitHostPort(config.Destination)
		if splitErr != nil || strings.TrimSpace(host) == "" {
			return config, 0, errorf(CodeBadRequest, "the destination must be host:port")
		}
		port, portErr := strconv.ParseUint(portText, 10, 16)
		if portErr != nil || port == 0 {
			return config, 0, errorf(CodeBadRequest, "the destination port must be 1-65535")
		}
		config.Destination = net.JoinHostPort(host, strconv.Itoa(int(port)))
	} else {
		config.Destination = ""
	}
	if config.Label == "" {
		config.Label = fmt.Sprintf("%s %d", name, config.LocalPort)
	}
	return config, mode, nil
}

// ---- manager operations ----

func (m *forwardManager) list() []forwardState {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]forwardState, 0, len(m.order))
	for _, id := range m.order {
		config := m.configs[id]
		if config == nil {
			continue
		}
		out = append(out, m.stateLocked(config, m.runtimes[id]))
	}
	return out
}

func (m *forwardManager) state(id string) (forwardState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	config := m.configs[id]
	if config == nil {
		return forwardState{}, errorf(CodeNotFound, "no forward with id %q", id)
	}
	return m.stateLocked(config, m.runtimes[id]), nil
}

func (m *forwardManager) stateLocked(config *forwardConfig, runtime *forwardRuntime) forwardState {
	mode, _ := forwardModeForName(config.Mode)
	state := forwardState{
		ID:            config.ID,
		Label:         config.Label,
		HostID:        config.HostID,
		Mode:          config.Mode,
		ListenAddress: forwardListenAddress(config),
		Destination:   forwardDestination(config, mode),
		AutoStart:     config.AutoStart,
		State:         "stopped",
	}
	if mode == portforward.ModeSOCKS5 {
		state.Destination = ""
	} else if mode == portforward.ModeHTTPConnect {
		state.Destination = ""
	}
	if runtime == nil {
		return state
	}
	state.SessionID = runtime.sessionID
	switch {
	case runtime.starting:
		state.State = "starting"
	case runtime.forward != nil:
		status := runtime.forward.Status()
		state.State = "running"
		state.Active = status.Active
		state.Accepted = status.Accepted
		state.Uploaded = status.Uploaded
		state.Downloaded = status.Downloaded
		if status.Closed {
			state.State = "stopped"
		}
		if status.LastError != nil {
			state.Error = status.LastError.Error()
		}
	case runtime.err != "":
		state.State = "failed"
		state.Error = runtime.err
	}
	return state
}

func (m *forwardManager) add(config forwardConfig) (forwardState, error) {
	config, _, err := validateForward(config)
	if err != nil {
		return forwardState{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	config.ID = fmt.Sprintf("forward-%d", m.nextID)
	m.configs[config.ID] = &config
	m.order = append(m.order, config.ID)
	if err := m.saveLocked(); err != nil {
		delete(m.configs, config.ID)
		m.order = m.order[:len(m.order)-1]
		return forwardState{}, err
	}
	return m.stateLocked(&config, nil), nil
}

func (m *forwardManager) update(id string, patch forwardConfig) (forwardState, error) {
	patch, _, err := validateForward(patch)
	if err != nil {
		return forwardState{}, err
	}
	m.mu.Lock()
	config := m.configs[id]
	if config == nil {
		m.mu.Unlock()
		return forwardState{}, errorf(CodeNotFound, "no forward with id %q", id)
	}
	previous := *config
	config.Label = patch.Label
	config.HostID = patch.HostID
	config.Mode = patch.Mode
	config.LocalPort = patch.LocalPort
	config.Destination = patch.Destination
	config.AutoStart = patch.AutoStart
	if err := m.saveLocked(); err != nil {
		*config = previous
		m.mu.Unlock()
		return forwardState{}, err
	}
	runtime := m.runtimes[id]
	changed := previous.HostID != config.HostID || previous.Mode != config.Mode ||
		previous.LocalPort != config.LocalPort || previous.Destination != config.Destination
	state := m.stateLocked(config, runtime)
	m.mu.Unlock()
	if changed && runtime != nil {
		m.stop(id)
		state, _ = m.state(id)
		state.State = "stopped"
		state.Error = ""
		state.SessionID = 0
	}
	return state, nil
}

func (m *forwardManager) remove(id string) error {
	m.mu.Lock()
	if m.configs[id] == nil {
		m.mu.Unlock()
		return errorf(CodeNotFound, "no forward with id %q", id)
	}
	m.mu.Unlock()
	m.stop(id)
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.configs, id)
	for index, ordered := range m.order {
		if ordered == id {
			m.order = append(m.order[:index], m.order[index+1:]...)
			break
		}
	}
	return m.saveLocked()
}

// start binds one forward. Local modes return once the listener is bound;
// remote mode asks the server for a listener in the background.
func (m *forwardManager) start(id string, sessionID uint32) (forwardState, error) {
	m.mu.Lock()
	config := m.configs[id]
	if config == nil {
		m.mu.Unlock()
		return forwardState{}, errorf(CodeNotFound, "no forward with id %q", id)
	}
	if m.closed {
		m.mu.Unlock()
		return forwardState{}, errorf(CodeUnavailable, "the core is shutting down")
	}
	if runtime := m.runtimes[id]; runtime != nil && (runtime.forward != nil || runtime.starting) {
		state := m.stateLocked(config, runtime)
		m.mu.Unlock()
		return state, nil
	}
	snapshot := *config
	m.mu.Unlock()

	mode, err := forwardModeForName(snapshot.Mode)
	if err != nil {
		return forwardState{}, err
	}
	target, err := m.sessionFor(&snapshot, sessionID)
	if err != nil {
		m.setRuntimeError(id, sessionID, err)
		state, _ := m.state(id)
		return state, nil
	}
	connection, ok := target.connection()
	if !ok {
		problem := errorf(CodeUnavailable, "the session is not connected")
		m.setRuntimeError(id, target.id, problem)
		state, _ := m.state(id)
		return state, nil
	}
	runtime := &forwardRuntime{sessionID: target.id, starting: true}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return forwardState{}, errorf(CodeUnavailable, "the core is shutting down")
	}
	m.runtimes[id] = runtime
	m.mu.Unlock()

	settings := portforward.Config{
		ListenAddress: forwardListenAddress(&snapshot),
		Destination:   forwardDestination(&snapshot, mode),
		Mode:          mode,
		MaxStreams:    forwardMaxStreams,
		Dial:          connection.DialContext,
	}
	if mode == portforward.ModeRemote {
		settings.Listen = connection.Listen
		dialer := &net.Dialer{Timeout: forwardDialWait}
		settings.Dial = dialer.DialContext
	}

	launch := func() {
		forward, startErr := portforward.Start(settings)
		m.mu.Lock()
		current := m.runtimes[id]
		if current != runtime {
			m.mu.Unlock()
			if forward != nil {
				forward.CloseAsync()
			}
			return
		}
		runtime.starting = false
		if startErr != nil {
			runtime.err = startErr.Error()
		} else {
			runtime.forward = forward
		}
		m.mu.Unlock()
	}
	if mode == portforward.ModeRemote {
		go launch()
	} else {
		launch()
	}
	return m.state(id)
}

func (m *forwardManager) setRuntimeError(id string, sessionID uint32, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.runtimes[id] != nil && (m.runtimes[id].forward != nil || m.runtimes[id].starting) {
		return
	}
	m.runtimes[id] = &forwardRuntime{sessionID: sessionID, err: err.Error()}
}

// stop ends one forward and forgets its runtime state.
func (m *forwardManager) stop(id string) {
	m.mu.Lock()
	runtime := m.runtimes[id]
	delete(m.runtimes, id)
	m.mu.Unlock()
	if runtime != nil && runtime.forward != nil {
		runtime.forward.CloseAsync()
	}
}

func (m *forwardManager) sessionFor(config *forwardConfig, sessionID uint32) (*session, error) {
	if sessionID != 0 {
		target, ok := m.sessions.get(sessionID)
		if !ok {
			return nil, errorf(CodeNotFound, "no session with id %d", sessionID)
		}
		if target.host.ID != config.HostID {
			return nil, errorf(CodeBadRequest, "the session is not connected to the forward's host")
		}
		return target, nil
	}
	for _, info := range m.sessions.list() {
		if info.HostID != config.HostID || info.Phase != "connected" {
			continue
		}
		if target, ok := m.sessions.get(info.ID); ok {
			return target, nil
		}
	}
	return nil, errorf(CodeUnavailable, "open the host before starting its forwards")
}

// sessionConnected starts every auto-start forward saved for the host.
func (m *forwardManager) sessionConnected(target *session) {
	if target == nil || m == nil {
		return
	}
	m.mu.Lock()
	ids := make([]string, 0, len(m.order))
	for _, id := range m.order {
		config := m.configs[id]
		if config == nil || !config.AutoStart || config.HostID != target.host.ID {
			continue
		}
		if runtime := m.runtimes[id]; runtime != nil && (runtime.forward != nil || runtime.starting) {
			continue
		}
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		_, _ = m.start(id, target.id)
	}
}

// sessionEnded stops every forward bound to a session that closed.
func (m *forwardManager) sessionEnded(id uint32) {
	if m == nil {
		return
	}
	m.mu.Lock()
	stopped := make([]*portforward.Forward, 0, len(m.runtimes))
	for forwardID, runtime := range m.runtimes {
		if runtime.sessionID != id {
			continue
		}
		delete(m.runtimes, forwardID)
		if runtime.forward != nil {
			stopped = append(stopped, runtime.forward)
		}
	}
	m.mu.Unlock()
	for _, forward := range stopped {
		forward.CloseAsync()
	}
}

func (m *forwardManager) closeAll() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.closed = true
	stopped := make([]*portforward.Forward, 0, len(m.runtimes))
	for id, runtime := range m.runtimes {
		delete(m.runtimes, id)
		if runtime.forward != nil {
			stopped = append(stopped, runtime.forward)
		}
	}
	m.mu.Unlock()
	for _, forward := range stopped {
		forward.CloseAsync()
	}
}

func (m *forwardManager) saveLocked() error {
	stored := struct {
		Forwards []forwardConfig `json:"forwards"`
	}{Forwards: make([]forwardConfig, 0, len(m.order))}
	for _, id := range m.order {
		if config := m.configs[id]; config != nil {
			stored.Forwards = append(stored.Forwards, *config)
		}
	}
	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return errorf(CodeInternal, "cannot encode forwards: %v", err)
	}
	temporary := m.path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return errorf(CodeInternal, "cannot write the forwards file: %v", err)
	}
	if err := os.Rename(temporary, m.path); err != nil {
		return errorf(CodeInternal, "cannot replace the forwards file: %v", err)
	}
	return nil
}

// ---- RPC ----

func (a *App) callForwards(method string, params json.RawMessage) (bool, any, error) {
	if !strings.HasPrefix(method, "forwards.") {
		return false, nil, nil
	}
	switch method {
	case "forwards.list":
		return true, map[string]any{"forwards": a.forwards.list()}, nil
	case "forwards.status":
		var request struct {
			ID string `json:"id"`
		}
		if err := decodeParams(params, &request); err != nil {
			return true, nil, err
		}
		if request.ID == "" {
			return true, map[string]any{"forwards": a.forwards.list()}, nil
		}
		state, err := a.forwards.state(request.ID)
		if err != nil {
			return true, nil, err
		}
		return true, map[string]any{"forward": state}, nil
	case "forwards.add":
		var config forwardConfig
		if err := decodeParams(params, &config); err != nil {
			return true, nil, err
		}
		state, err := a.forwards.add(config)
		return true, map[string]any{"forward": state}, err
	case "forwards.update":
		var request struct {
			ID string `json:"id"`
			forwardConfig
		}
		if err := decodeParams(params, &request); err != nil {
			return true, nil, err
		}
		state, err := a.forwards.update(request.ID, request.forwardConfig)
		return true, map[string]any{"forward": state}, err
	case "forwards.remove":
		var request struct {
			ID string `json:"id"`
		}
		if err := decodeParams(params, &request); err != nil {
			return true, nil, err
		}
		if err := a.forwards.remove(request.ID); err != nil {
			return true, nil, err
		}
		return true, map[string]bool{"removed": true}, nil
	case "forwards.start":
		var request struct {
			ID        string `json:"id"`
			SessionID uint32 `json:"sessionId"`
		}
		if err := decodeParams(params, &request); err != nil {
			return true, nil, err
		}
		state, err := a.forwards.start(request.ID, request.SessionID)
		return true, map[string]any{"forward": state}, err
	case "forwards.stop":
		var request struct {
			ID string `json:"id"`
		}
		if err := decodeParams(params, &request); err != nil {
			return true, nil, err
		}
		if _, err := a.forwards.state(request.ID); err != nil {
			return true, nil, err
		}
		a.forwards.stop(request.ID)
		state, _ := a.forwards.state(request.ID)
		return true, map[string]any{"forward": state}, nil
	}
	return true, nil, errorf(CodeBadRequest, "unknown method %q", method)
}

// connection returns the session's authenticated connection.
func (s *session) connection() (*sshconn.Conn, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil || s.phase != sshconn.PhaseConnected {
		return nil, false
	}
	return s.conn, true
}
