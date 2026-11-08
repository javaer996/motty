package mobilecore

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"motty/internal/ai/capture"
	"motty/internal/ai/gate"
	"motty/internal/ai/tools"
	"motty/internal/cmdhistory"
	"motty/internal/sessionlog"
	"motty/internal/shellkind"
	"motty/internal/sshconn"
	"motty/internal/term"
)

const (
	defaultCols       = 80
	defaultRows       = 24
	defaultScrollback = 5000
	statusPoll        = 100 * time.Millisecond
	dialTimeout       = 15 * time.Second
	maxExecOutput     = 64 << 10
)

// SessionInfo is the front end's view of one terminal tab.
type SessionInfo struct {
	ID          uint32 `json:"id"`
	HostID      string `json:"hostId"`
	Title       string `json:"title"`
	Phase       string `json:"phase"`
	Address     string `json:"address"`
	User        string `json:"user"`
	Cols        int    `json:"cols"`
	Rows        int    `json:"rows"`
	ConnectedAt int64  `json:"connectedAt,omitempty"`
	Error       string `json:"error,omitempty"`
	Method      string `json:"method,omitempty"`
	Server      string `json:"serverVersion,omitempty"`
	// Local marks a shell running on this device instead of an SSH host.
	Local bool `json:"local,omitempty"`
}

// shellBackend is the part of a running shell the session drives. Both
// *sshconn.Shell and *localshell.Shell satisfy it; the terminal pump and the
// input path only need this much.
type shellBackend interface {
	Drain(dst []byte) []byte
	Write(p []byte) bool
	Resize(cols, rows int)
	Done() <-chan struct{}
	Err() error
	Close()
	Kind() shellkind.Kind
}

// session is one SSH shell and the terminal state it feeds. The terminal is
// the single source of truth for the screen: the pump goroutine writes server
// output into it, and the front end only reads snapshots.
type session struct {
	id      uint32
	host    Host
	manager *sessionManager

	mu          sync.Mutex
	terminal    *term.Terminal
	conn        *sshconn.Conn
	shell       shellBackend
	local       bool
	exec        *terminalExec
	phase       sshconn.Phase
	err         error
	cols, rows  int
	connectedAt time.Time
	lastStatus  uint64
	method      string
	server      string

	// recorder learns typed commands from keys and the screen; both are
	// protected by mu, like the terminal they read.
	recorder    cmdhistory.Recorder
	recordStart time.Time

	// The question currently waiting for the user, and the password the user
	// asked to keep once the server accepts it. Both belong to mu.
	promptQuestions    []sshconn.Question
	pendingSecretName  string
	pendingSecretValue string
	// filledSecret is the vault secret an automatic answer used; a refusal
	// removes it, so a stale password is not tried again on every reconnect.
	filledSecret string

	// run is the current connection generation. Reconnect replaces it, so a
	// stale watcher can only ever end the generation it was started for.
	run *sessionRun
	// wake carries connection-status changes to the watcher; outputWake
	// carries shell output to the terminal pump. They are separate on purpose:
	// one buffered channel shared by both consumers loses a wakeup whenever
	// the wrong goroutine drains it, leaving output stuck until the next
	// keystroke.
	wake       chan struct{}
	outputWake chan struct{}
	// sizeHint receives a signal whenever the front end resizes the session.
	// A local shell waits on it before starting, so the pty is created at the
	// size the view will keep instead of the default 80x24.
	sizeHint chan struct{}
}

// sessionRun is one connection generation. Its done channel ends the pump and
// the status watcher; once keeps a second close from panicking.
type sessionRun struct {
	done chan struct{}
	once sync.Once
}

func newSessionRun() *sessionRun {
	return &sessionRun{done: make(chan struct{})}
}

func (run *sessionRun) markDone() {
	if run != nil {
		run.once.Do(func() { close(run.done) })
	}
}

// terminalExec is one command the assistant typed into the shell, waiting for
// its end marker while the pump feeds output through the tap.
type terminalExec struct {
	tap  *capture.Tap
	done chan capture.Result
}

func phaseName(phase sshconn.Phase) string {
	switch phase {
	case sshconn.PhaseConnecting:
		return "connecting"
	case sshconn.PhaseHostKeyPrompt:
		return "hostkey"
	case sshconn.PhaseAuthenticating:
		return "authenticating"
	case sshconn.PhaseKeyPassphrasePrompt:
		return "passphrase"
	case sshconn.PhasePasswordPrompt:
		return "password"
	case sshconn.PhaseConnected:
		return "connected"
	case sshconn.PhaseFailed:
		return "failed"
	case sshconn.PhaseClosed:
		return "closed"
	default:
		return "unknown"
	}
}

// sessionManager owns every open tab. Sessions outlive their connection: a
// failed one stays readable (with its last screen) until the front end closes
// it.
type sessionManager struct {
	mu       sync.Mutex
	sessions map[uint32]*session
	nextID   uint32
	activeID uint32
	keys     *keyStore
	hosts    *hostStore
	data     *dataStore
	events   *eventQueue
	// changed asks for a sync after a secret saved at a prompt; nil in tests.
	changed  func()
	files    *filesRegistry
	forwards *forwardManager
	logs     *logManager
	history  *historyManager
	dataDir  string
}

func newSessionManager(hosts *hostStore, events *eventQueue, dataDir string) *sessionManager {
	return &sessionManager{
		sessions: map[uint32]*session{},
		hosts:    hosts,
		events:   events,
		dataDir:  dataDir,
	}
}

func (m *sessionManager) open(hostID string, cols, rows int) (SessionInfo, error) {
	host, ok := m.hosts.get(hostID)
	if !ok {
		return SessionInfo{}, errorf(CodeNotFound, "no host with id %q", hostID)
	}
	if err := host.Validate(); err != nil {
		return SessionInfo{}, err
	}
	s := m.startSession(host, cols, rows, false)
	m.logBegin(s)
	go s.connect()
	return s.info(), nil
}

// startSession registers a new tab. It is shared by SSH and local sessions;
// the caller starts the connection once the journal row exists.
func (m *sessionManager) startSession(host Host, cols, rows int, local bool) *session {
	if cols < 1 {
		cols = defaultCols
	}
	if rows < 1 {
		rows = defaultRows
	}
	s := &session{
		host:        host,
		manager:     m,
		local:       local,
		cols:        cols,
		rows:        rows,
		terminal:    term.New(cols, rows, defaultScrollback),
		recordStart: time.Now(),
		run:         newSessionRun(),
		wake:        make(chan struct{}, 1),
		outputWake:  make(chan struct{}, 1),
		sizeHint:    make(chan struct{}, 1),
	}
	m.mu.Lock()
	m.nextID++
	if m.nextID == 0 {
		m.nextID++
	}
	s.id = m.nextID
	m.sessions[s.id] = s
	m.activeID = s.id
	m.mu.Unlock()
	return s
}

func (m *sessionManager) close(id uint32) error {
	m.mu.Lock()
	s, ok := m.sessions[id]
	if ok {
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	if !ok {
		return errorf(CodeNotFound, "no session with id %d", id)
	}
	s.close()
	return nil
}

// closeFiles releases the SFTP worker bound to an ending connection. The
// worker cannot survive a reconnect: its remote belongs to the old SSH client.
func (m *sessionManager) closeFiles(id uint32) {
	if m.files != nil {
		m.files.close(id)
	}
}

// forwardConnected starts the auto-start tunnels saved for this host.
func (m *sessionManager) forwardConnected(s *session) {
	if m.forwards != nil {
		m.forwards.sessionConnected(s)
	}
}

// forwardEnded stops tunnels bound to a session that closed or reconnected.
func (m *sessionManager) forwardEnded(id uint32) {
	if m.forwards != nil {
		m.forwards.sessionEnded(id)
	}
}

// ---- session journal hooks ----

func (m *sessionManager) logBegin(s *session) {
	if m.logs != nil {
		m.logs.begin(s)
	}
}

func (m *sessionManager) logEvent(id uint32, kind sessionlog.EventKind) {
	if m.logs != nil {
		m.logs.event(id, kind)
	}
}

func (m *sessionManager) logConnected(id uint32) {
	if m.logs != nil {
		m.logs.connected(id)
	}
}

func (m *sessionManager) logFinish(id uint32, status sessionlog.Status, reason sessionlog.Reason) {
	if m.logs != nil {
		m.logs.finish(id, status, reason)
	}
}

func (m *sessionManager) historyRecord(host, command string) {
	if m.history != nil {
		m.history.record(host, command)
	}
}

// byHost returns the most recently opened tab for a saved host. A desktop
// reached through SSH uses it to find the tunnel it must travel through.
func (m *sessionManager) byHost(hostID string) (*session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var found *session
	for _, s := range m.sessions {
		if s.host.ID != hostID {
			continue
		}
		if found == nil || s.id > found.id {
			found = s
		}
	}
	return found, found != nil
}

func (m *sessionManager) get(id uint32) (*session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	return s, ok
}

func (m *sessionManager) list() []SessionInfo {
	m.mu.Lock()
	all := make([]*session, 0, len(m.sessions))
	for _, s := range m.sessions {
		all = append(all, s)
	}
	m.mu.Unlock()
	out := make([]SessionInfo, 0, len(all))
	for _, s := range all {
		out = append(out, s.info())
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].ID < out[j-1].ID; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func (m *sessionManager) shutdown() {
	m.mu.Lock()
	all := make([]*session, 0, len(m.sessions))
	for _, s := range m.sessions {
		all = append(all, s)
	}
	m.sessions = map[uint32]*session{}
	m.mu.Unlock()
	for _, s := range all {
		s.close()
	}
}

// ---- one session ----

func (s *session) wakeup() {
	// Reconnect replaces the channel while the old connection may still be
	// notifying; read it under the same lock that swaps it.
	s.mu.Lock()
	wake := s.wake
	s.mu.Unlock()
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (s *session) outputWakeup() {
	s.mu.Lock()
	wake := s.outputWake
	s.mu.Unlock()
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (s *session) connect() {
	s.mu.Lock()
	run := s.run
	s.mu.Unlock()
	config, err := s.dialConfig()
	if err != nil {
		s.fail(run, err)
		return
	}
	conn := sshconn.Dial(config)
	s.mu.Lock()
	s.conn = conn
	s.phase = sshconn.PhaseConnecting
	s.mu.Unlock()
	s.mu.Lock()
	wake := s.wake
	s.mu.Unlock()
	s.emitState()
	s.watch(conn, run, wake)
}

func (s *session) dialConfig() (sshconn.Config, error) {
	host := s.host
	jumps, err := parseProxyJumps(host.ProxyJump, host.User)
	if err != nil {
		return sshconn.Config{}, err
	}
	methods, agent := authPolicy(host.Auth)
	identities, additional := []string{}, []string{}
	if keys := s.manager.keys; keys != nil && host.Auth != AuthPassword && host.Auth != AuthAgent {
		selected := ""
		if host.Identity != "" {
			// The app itself stores a vault reference; a synced desktop host
			// stores a key fingerprint and an imported SSH config a file path.
			// resolveIdentity turns all three into something the key store can
			// hand to the SSH client.
			selected = keys.resolveIdentity(host.Identity)
			identities = []string{selected}
		}
		switch host.Auth {
		case AuthKey:
			// Key mode with no chosen key tries every saved key, like the
			// desktop; an empty (not nil) list also keeps the ~/.ssh defaults
			// that this app does not have from being probed.
			if selected == "" {
				identities = []string{}
				additional = keys.identityPaths()
			}
		default:
			// Automatic tries the chosen key first, then every other saved key,
			// so an imported config whose key was imported under another name
			// still connects.
			additional = keys.identityPaths()
		}
		if selected != "" {
			kept := additional[:0]
			for _, path := range additional {
				if path != selected {
					kept = append(kept, path)
				}
			}
			additional = kept
		}
	}
	authentication := sshconn.Authentication{
		Methods:                 methods,
		IdentityFiles:           identities,
		AdditionalIdentityFiles: additional,
		Agent:                   agent,
	}
	policies := map[sshconn.Endpoint]sshconn.Authentication{}
	target := sshconn.Endpoint{Host: host.Address, Port: host.Port, User: host.User}
	policies[target] = authentication
	for _, jump := range jumps {
		policies[jump] = authentication
	}
	proxy, err := hostProxyFor(s.manager.data, host)
	if err != nil {
		return sshconn.Config{}, err
	}
	config := sshconn.Config{
		Host:                    host.Address,
		Port:                    host.Port,
		User:                    host.User,
		Proxy:                   proxy,
		ProxyJumps:              jumps,
		KnownHostsPath:          filepath.Join(s.manager.dataDir, "known_hosts"),
		IdentityFiles:           identities,
		AdditionalIdentityFiles: additional,
		Agent:                   agent,
		Authentication:          policies,
		DialTimeout:             dialTimeout,
		KeepAlive: sshconn.KeepAliveConfig{
			Interval: 30 * time.Second,
			Timeout:  10 * time.Second,
		},
		Notify: s.wakeup,
	}
	if s.manager.keys != nil {
		// Keys are kept in the vault, so a host's identity travels with a
		// backup and a sync instead of naming a file only this device has.
		keys := s.manager.keys
		config.StoredKey = keys.keyData
	}
	return config, nil
}

func authPolicy(mode string) (sshconn.AuthMethods, bool) {
	switch mode {
	case AuthPassword:
		return sshconn.AuthPassword | sshconn.AuthKeyboardInteractive, false
	case AuthKey:
		return sshconn.AuthPublicKey, false
	case AuthAgent:
		// iOS has no local agent in practice; the attempt costs nothing and
		// lets a future in-app agent plug in here.
		return sshconn.AuthPublicKey, true
	default:
		return sshconn.AuthAll, true
	}
}

// parseProxyJumps reads "user@host:port,user@host" into endpoints ordered
// outermost first.
func parseProxyJumps(spec, defaultUser string) ([]sshconn.Endpoint, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" || strings.EqualFold(spec, "none") {
		return nil, nil
	}
	parts := strings.Split(spec, ",")
	out := make([]sshconn.Endpoint, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		endpoint := sshconn.Endpoint{Host: part, Port: 22, User: defaultUser}
		if at := strings.LastIndex(part, "@"); at >= 0 {
			endpoint.User = part[:at]
			endpoint.Host = part[at+1:]
		}
		hostPart := endpoint.Host
		if colon := strings.LastIndex(hostPart, ":"); colon > 0 && !strings.Contains(hostPart[colon+1:], "]") {
			if port, err := strconv.ParseUint(hostPart[colon+1:], 10, 16); err == nil {
				endpoint.Port = uint16(port)
				endpoint.Host = hostPart[:colon]
			}
		}
		endpoint.Host = strings.Trim(endpoint.Host, "[]")
		if endpoint.Host == "" || endpoint.User == "" {
			return nil, errorf(CodeBadRequest, "invalid proxy jump %q", part)
		}
		out = append(out, endpoint)
	}
	return out, nil
}

func (s *session) watch(conn *sshconn.Conn, run *sessionRun, wake <-chan struct{}) {
	ticker := time.NewTicker(statusPoll)
	defer ticker.Stop()
	for {
		select {
		case <-run.done:
			return
		case <-wake:
		case <-ticker.C:
		}
		status := conn.Status()
		s.mu.Lock()
		changed := status.Version != s.lastStatus
		if changed {
			s.lastStatus = status.Version
		}
		s.mu.Unlock()
		if !changed {
			continue
		}
		s.applyStatus(status, run)
		if status.Phase == sshconn.PhaseFailed || status.Phase == sshconn.PhaseClosed {
			run.markDone()
			return
		}
	}
}

func (s *session) applyStatus(status sshconn.Status, run *sessionRun) {
	s.mu.Lock()
	s.phase = status.Phase
	s.err = status.Err
	s.method = status.Method
	s.server = status.ServerVersion
	hasShell := s.shell != nil
	s.mu.Unlock()

	s.emitState()
	switch status.Phase {
	case sshconn.PhaseHostKeyPrompt:
		s.emitPrompt("hostkey", status)
		s.manager.logEvent(s.id, sessionlog.EventHostKeyPrompt)
	case sshconn.PhasePasswordPrompt:
		s.emitPrompt("password", status)
		s.manager.logEvent(s.id, sessionlog.EventAuthenticating)
	case sshconn.PhaseKeyPassphrasePrompt:
		s.emitPrompt("passphrase", status)
		s.manager.logEvent(s.id, sessionlog.EventAuthenticating)
	case sshconn.PhaseConnected:
		s.commitPendingSecret()
		s.mu.Lock()
		s.filledSecret = ""
		s.mu.Unlock()
		if !hasShell {
			s.openShell(run)
		}
		s.manager.logConnected(s.id)
		s.manager.forwardConnected(s)
	case sshconn.PhaseFailed:
		s.discardPendingSecret()
		s.manager.forwardEnded(s.id)
		s.manager.logFinish(s.id, sessionlog.StatusFailed, sessionlog.ReasonConnectFailed)
	case sshconn.PhaseClosed:
		s.discardPendingSecret()
		s.manager.forwardEnded(s.id)
		s.manager.logFinish(s.id, sessionlog.StatusEnded, sessionlog.ReasonNetworkLost)
	}
}

func (s *session) openShell(run *sessionRun) {
	s.mu.Lock()
	conn := s.conn
	cols, rows := s.cols, s.rows
	s.mu.Unlock()
	if conn == nil {
		return
	}
	shell, err := conn.OpenShell(sshconn.ShellConfig{
		Cols:   cols,
		Rows:   rows,
		Term:   "xterm-256color",
		Notify: s.outputWakeup,
	})
	if err != nil {
		s.mu.Lock()
		s.err = err
		s.phase = sshconn.PhaseFailed
		s.mu.Unlock()
		s.manager.logFinish(s.id, sessionlog.StatusFailed, sessionlog.ReasonShellFailed)
		s.emitState()
		run.markDone()
		return
	}
	s.mu.Lock()
	s.shell = shell
	s.connectedAt = time.Now()
	outputWake := s.outputWake
	s.mu.Unlock()
	s.emitState()
	go s.pump(shell, run.done, outputWake)
}

func (s *session) pump(shell shellBackend, done <-chan struct{}, wake <-chan struct{}) {
	buf := make([]byte, 0, 64*1024)
	drain := func() {
		for {
			buf = shell.Drain(buf[:0])
			if len(buf) == 0 {
				return
			}
			s.mu.Lock()
			display := buf
			if s.exec != nil {
				var finished bool
				display, finished = s.exec.tap.Feed(buf)
				if finished {
					exec := s.exec
					s.exec = nil
					exec.done <- exec.tap.Result()
				}
			}
			if len(display) > 0 {
				_, _ = s.terminal.Write(display)
				s.recorder.Output(time.Since(s.recordStart))
			}
			s.mu.Unlock()
		}
	}
	for {
		select {
		case <-done:
			// A shell prints its last line just before it ends; both backends
			// promise that Drain holds that output once Done has closed.
			drain()
			return
		case <-wake:
		}
		drain()
	}
}

// doneChannel is the end of the connection generation the caller is looking
// at. A reconnect starts a new generation and ends the old channel, so a
// desktop bound to this tunnel still sees its transport disappear.
func (s *session) doneChannel() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.run.done
}

func (s *session) close() {
	s.mu.Lock()
	run := s.run
	shell := s.shell
	conn := s.conn
	s.shell = nil
	s.mu.Unlock()
	run.markDone()
	s.manager.logFinish(s.id, sessionlog.StatusEnded, sessionlog.ReasonUserClosed)
	if shell != nil {
		shell.Close()
	}
	if conn != nil {
		conn.Close()
	}
	s.manager.closeFiles(s.id)
	s.manager.forwardEnded(s.id)
}

// reconnect starts a fresh connection in the same tab, keeping the screen the
// user already has. It is only allowed after the previous attempt ended, so a
// stray tap cannot tear down a working shell.
func (s *session) reconnect() (SessionInfo, error) {
	s.mu.Lock()
	if s.phase != sshconn.PhaseFailed && s.phase != sshconn.PhaseClosed {
		s.mu.Unlock()
		return SessionInfo{}, errorf(CodeConflict, "the session is not disconnected")
	}
	oldRun := s.run
	oldShell := s.shell
	oldConn := s.conn
	s.shell = nil
	s.conn = nil
	s.exec = nil
	s.phase = sshconn.PhaseConnecting
	s.err = nil
	s.lastStatus = 0
	s.method = ""
	s.server = ""
	s.connectedAt = time.Time{}
	s.run = newSessionRun()
	s.wake = make(chan struct{}, 1)
	s.outputWake = make(chan struct{}, 1)
	s.recorder.Reset()
	s.recordStart = time.Now()
	info := s.infoLocked()
	s.mu.Unlock()
	// Release the old pump and watcher even if the previous attempt had not
	// marked its generation done yet.
	oldRun.markDone()

	// A manual redial is a new logical session: the old row ends and a new
	// one starts, so the journal never claims one connection lived through it.
	s.manager.logFinish(s.id, sessionlog.StatusEnded, sessionlog.ReasonUserClosed)
	s.manager.logBegin(s)
	s.manager.closeFiles(s.id)
	s.manager.forwardEnded(s.id)
	if oldShell != nil {
		oldShell.Close()
	}
	if oldConn != nil {
		oldConn.Close()
	}
	s.emitState()
	if s.local {
		go s.connectLocal()
	} else {
		go s.connect()
	}
	return info, nil
}

func (s *session) fail(run *sessionRun, err error) {
	s.mu.Lock()
	s.err = err
	s.phase = sshconn.PhaseFailed
	s.mu.Unlock()
	s.manager.logFinish(s.id, sessionlog.StatusFailed, sessionlog.ReasonConnectFailed)
	s.emitState()
	run.markDone()
}

func (s *session) info() SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.infoLocked()
}

func (s *session) infoLocked() SessionInfo {
	title := ""
	if s.terminal != nil {
		title = s.terminal.Title()
	}
	if title == "" {
		title = s.host.Label
	}
	info := SessionInfo{
		ID:      s.id,
		HostID:  s.host.ID,
		Title:   title,
		Phase:   phaseName(s.phase),
		Address: s.host.Address,
		User:    s.host.User,
		Cols:    s.cols,
		Rows:    s.rows,
		Method:  s.method,
		Server:  s.server,
		Local:   s.local,
	}
	if !s.connectedAt.IsZero() {
		info.ConnectedAt = s.connectedAt.Unix()
	}
	if s.err != nil {
		info.Error = s.err.Error()
	}
	return info
}

func (s *session) emitState() {
	s.manager.events.push("session.state", map[string]any{"session": s.info()})
}

func (s *session) emitPrompt(kind string, status sshconn.Status) {
	s.mu.Lock()
	s.promptQuestions = append([]sshconn.Question(nil), status.Questions...)
	s.mu.Unlock()
	if kind == "password" {
		// A retry is a new question: what was staged for the refused attempt
		// must not survive into a later successful one.
		s.discardPendingSecret()
	}
	if status.Attempt > 1 {
		s.forgetRefusedSecret()
	}
	if s.answerSavedSecret(kind, status) {
		return
	}
	payload := map[string]any{"sessionID": s.id, "kind": kind}
	switch kind {
	case "hostkey":
		known := status.HostKey.Known
		if known == nil {
			known = []string{}
		}
		payload["hostKey"] = map[string]any{
			"address":     status.HostKey.Address,
			"type":        status.HostKey.Type,
			"fingerprint": status.HostKey.Fingerprint,
			"known":       known,
			"file":        status.HostKey.File,
			"changed":     len(known) > 0,
		}
	case "password":
		questions := make([]map[string]any, 0, len(status.Questions))
		for _, question := range status.Questions {
			questions = append(questions, map[string]any{
				"text":   question.Text,
				"secret": question.Secret,
			})
		}
		payload["questions"] = questions
		payload["attempt"] = status.Attempt
	case "passphrase":
		payload["identityPath"] = status.IdentityPath
	}
	s.manager.events.push("session.prompt", payload)
}

// answer drives one connection phase that waits for the user.
type answerRequest struct {
	Kind    string   `json:"kind"`
	Value   string   `json:"value"`
	Save    bool     `json:"save"`
	Answers []string `json:"answers"`
	Reject  bool     `json:"reject"`
	Skip    bool     `json:"skip"`
}

func (s *session) answer(request answerRequest) error {
	s.mu.Lock()
	conn := s.conn
	s.mu.Unlock()
	if conn == nil {
		return errorf(CodeUnavailable, "the session has no connection")
	}
	switch request.Kind {
	case "hostkey":
		if request.Reject {
			conn.RejectHostKey()
		} else {
			conn.TrustHostKey(request.Save)
		}
	case "password":
		if request.Save {
			s.stagePasswordSave(request.Value)
		}
		conn.SubmitPassword(request.Value)
	case "passphrase":
		conn.SubmitKeyPassphrase(request.Value)
	case "questions":
		conn.SubmitAnswers(request.Answers)
	case "skip":
		conn.SkipIdentity()
	default:
		return errorf(CodeBadRequest, "unknown prompt kind %q", request.Kind)
	}
	return nil
}

// execCommand types one command into the shell and waits for its end marker.
// The terminal shows the command while the model receives only its output.
func (s *session) execCommand(ctx context.Context, args tools.TerminalExecArgs) (gate.ExecResult, error) {
	s.mu.Lock()
	if s.shell == nil {
		s.mu.Unlock()
		return gate.ExecResult{}, errorf(CodeUnavailable, "the session has no open shell yet")
	}
	if s.exec != nil {
		s.mu.Unlock()
		return gate.ExecResult{}, errorf(CodeUnavailable, "the terminal is already running another assistant command")
	}
	kind := s.shell.Kind()
	nonce := capture.NewNonce()
	wrapped, err := capture.WrapFor(kind, args.Command, nonce)
	if err != nil {
		s.mu.Unlock()
		return gate.ExecResult{}, errorf(CodeUnavailable, "%v", err)
	}
	tap := capture.NewTap(nonce, maxExecOutput)
	tap.SetEcho(wrapped)
	// PowerShell's line editor asks the terminal for its cursor while echoing;
	// hiding the echo would withhold that question, so show what it sends.
	if kind != shellkind.PowerShell {
		tap.SetDisplay(capture.DisplayCommandFor(kind, args.Command))
	}
	exec := &terminalExec{tap: tap, done: make(chan capture.Result, 1)}
	// The pump must see the tap before any output can arrive, so publish it
	// first and retract it if the shell refuses the input.
	s.exec = exec
	runDone := s.run.done
	if !s.shell.Write(wrapped) {
		s.exec = nil
		s.mu.Unlock()
		return gate.ExecResult{}, errorf(CodeUnavailable, "the shell is no longer accepting input")
	}
	s.mu.Unlock()

	timeout := time.Duration(args.TimeoutSeconds) * time.Second
	if args.TimeoutSeconds <= 0 {
		timeout = time.Duration(tools.DefaultTimeoutSeconds) * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-exec.done:
		return gate.ExecResult{
			Output:    result.Output,
			ExitCode:  result.ExitCode,
			Truncated: result.Truncated,
		}, nil
	case <-timer.C:
		return s.abortExec(exec, true), nil
	case <-ctx.Done():
		return s.abortExec(exec, true), nil
	case <-runDone:
		return s.abortExec(exec, true), nil
	}
}

// abortExec ends an in-flight command, interrupting the remote one when it is
// still running. A command that finished on its own is not interrupted.
func (s *session) abortExec(target *terminalExec, timedOut bool) gate.ExecResult {
	s.mu.Lock()
	aborted := false
	if s.exec == target {
		s.exec = nil
		aborted = true
		if rest := target.tap.Abort(); len(rest) > 0 && s.terminal != nil {
			_, _ = s.terminal.Write(rest)
		}
	}
	shell := s.shell
	s.mu.Unlock()
	if aborted && shell != nil {
		shell.Write([]byte{3})
	}
	result := target.tap.Result()
	return gate.ExecResult{
		Output:    result.Output,
		ExitCode:  result.ExitCode,
		Truncated: result.Truncated,
		TimedOut:  timedOut,
	}
}

// readText returns the visible screen or the retained scrollback as plain
// text, limited to the final lines the caller asked for.
func (s *session) readText(scrollback bool, lines int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminal == nil {
		return ""
	}
	var text string
	if scrollback {
		text = string(s.terminal.AppendRetainedText(nil))
	} else {
		text = s.terminal.Text()
	}
	return lastLines(text, min(lines, maxScreenLines))
}

func (s *session) ready() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shell != nil && s.phase == sshconn.PhaseConnected
}

func (s *session) input(data []byte) error {
	s.mu.Lock()
	shell := s.shell
	command, record := "", false
	if shell != nil && s.terminal != nil {
		command, record = s.recorder.Typed(
			data,
			cmdhistory.TermScreen{Term: s.terminal},
			time.Since(s.recordStart),
		)
	}
	s.mu.Unlock()
	if shell == nil {
		return errorf(CodeUnavailable, "the session is not connected")
	}
	if !shell.Write(data) {
		return errorf(CodeUnavailable, "the shell is no longer accepting input")
	}
	if record && command != "" && !s.host.NoHistory {
		s.manager.historyRecord(s.historyKey(), command)
	}
	return nil
}

func (s *session) resize(cols, rows int) error {
	if cols < 1 || rows < 1 || cols > 1000 || rows > 1000 {
		return errorf(CodeBadRequest, "invalid terminal size %dx%d", cols, rows)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cols, s.rows = cols, rows
	if s.terminal != nil {
		s.terminal.Resize(cols, rows)
	}
	if s.shell != nil {
		s.shell.Resize(cols, rows)
	}
	select {
	case s.sizeHint <- struct{}{}:
	default:
	}
	return nil
}

func (s *session) scroll(lines int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminal == nil {
		return 0
	}
	s.terminal.Scroll(lines)
	return s.terminal.ViewOffset()
}

func (s *session) scrollTo(offset int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminal == nil {
		return 0
	}
	s.terminal.ScrollTo(offset)
	return s.terminal.ViewOffset()
}

// snapshot writes a screen snapshot into dst. A zero sinceVersion always
// writes; otherwise the terminal version must differ. It returns the byte
// count, 0 when nothing changed, or -needed when dst is too small.
func (s *session) snapshot(dst []byte, sinceVersion uint64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminal == nil {
		return 0
	}
	if sinceVersion != 0 && s.terminal.Version() == sinceVersion {
		return 0
	}
	return encodeScreen(s.terminal, dst)
}
