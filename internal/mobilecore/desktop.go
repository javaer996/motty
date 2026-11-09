package mobilecore

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"motty/internal/desktop"
	"motty/internal/desktop/rdp"
	"motty/internal/desktop/vnc"
	"motty/internal/rfb"
	"motty/internal/sessionlog"
	"motty/internal/sshconn"
)

// Remote-desktop sessions. The protocol backends in internal/desktop do the
// protocol work; this file gives them an iOS lifecycle: an id the front end
// names, asynchronous password and certificate questions delivered as events,
// a frame buffer the C ABI can copy, and journal records.

const (
	desktopConnecting  = "connecting"
	desktopPassword    = "password"
	desktopCertificate = "certificate"
	desktopConnected   = "connected"
	desktopFailed      = "failed"
	desktopClosed      = "closed"
)

const (
	// maxDesktopWidth/Height bound what one frame buffer may hold. A VNC
	// server picks its own picture size, so this has to cover real desktops,
	// not just the largest iPad: 4K, 5K and ultrawide setups all fit. Past
	// this the session ends with a clear error instead of letting one server
	// allocate hundreds of megabytes.
	maxDesktopWidth     = 5120
	maxDesktopHeight    = 3456
	maxDesktopPixels    = 5120 * 2880 // 5K, 59 MB of BGRA
	desktopDialTimeout  = 20 * time.Second
	desktopFramePeriod  = 25 * time.Millisecond
	desktopDirtyLimit   = 32
	desktopMaxTextBytes = 1 << 20
)

// rdpAccount splits "CORP\alice" into the RDP user and domain. A plain name,
// or a user principal name, has no separate domain.
func rdpAccount(account string) (user, domain string) {
	if index := strings.IndexByte(account, '\\'); index > 0 {
		return account[index+1:], account[:index]
	}
	return account, ""
}

func sessionlogProtocol(protocol string) sessionlog.Protocol {
	if protocol == ProtocolRDP {
		return sessionlog.ProtocolRDP
	}
	return sessionlog.ProtocolVNC
}

// desktopState is the JSON shape the front end polls and receives in events.
type desktopState struct {
	ID        uint32 `json:"id"`
	HostID    string `json:"hostId"`
	Label     string `json:"label"`
	Address   string `json:"address"`
	Protocol  string `json:"protocol"`
	State     string `json:"state"`
	Error     string `json:"error,omitempty"`
	Name      string `json:"name,omitempty"`
	Encrypted bool   `json:"encrypted"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Attempt   int    `json:"attempt,omitempty"`
}

// desktopAnswer carries one reply to a question asked mid-handshake.
type desktopAnswer struct {
	kind   string
	value  string
	accept bool
	save   bool
}

// desktopManager owns every remote desktop. It is created once in Start and
// closed with the app.
type desktopManager struct {
	events   *eventQueue
	sessions *sessionManager
	logs     *logManager
	pins     *rdp.Pins
	// data holds the vault the desktop passwords live in, under the same
	// names the desktop application uses.
	data *dataStore
	// changed asks for a debounced sync after a secret was written or dropped.
	changed func()

	mu     sync.Mutex
	nextID uint32
	items  map[uint32]*desktopSession
	closed bool
}

func newDesktopManager(dataDir string, data *dataStore, sessions *sessionManager, logs *logManager, events *eventQueue) *desktopManager {
	return &desktopManager{
		events:   events,
		sessions: sessions,
		logs:     logs,
		pins:     rdp.NewPins(filepath.Join(dataDir, "rdp_certificates")),
		data:     data,
		items:    map[uint32]*desktopSession{},
	}
}

// open creates a desktop session and returns at once; the handshake and the
// password questions happen in the background. opening the same host again
// while it is live returns the running session instead of a second one.
func (m *desktopManager) open(host Host, width, height int, scale float64) (desktopState, error) {
	if !host.IsDesktop() {
		return desktopState{}, errorf(CodeBadRequest, "this host opens a terminal, not a remote desktop")
	}
	if err := host.Validate(); err != nil {
		return desktopState{}, err
	}
	if width <= 0 || height <= 0 {
		width, height = 1280, 720
	}
	width = min(width, maxDesktopWidth)
	height = min(height, maxDesktopHeight)
	if width%2 != 0 {
		width--
	}
	if height%2 != 0 {
		height--
	}
	if scale < 0.5 || scale > 4 {
		scale = 1
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return desktopState{}, errorf(CodeUnavailable, "the core is shutting down")
	}
	for id, existing := range m.items {
		if existing.host.ID == "" || existing.host.ID != host.ID {
			continue
		}
		if existing.live() {
			state := existing.state()
			m.mu.Unlock()
			return state, nil
		}
		delete(m.items, id)
	}
	m.nextID++
	id := m.nextID
	ctx, cancel := context.WithCancel(context.Background())
	d := &desktopSession{
		id: id, host: host, manager: m,
		ctx: ctx, cancel: cancel,
		reqWidth: width, reqHeight: height, scale: float32(scale),
		phase:   desktopConnecting,
		answers: make(chan desktopAnswer, 1),
		trusted: map[string]bool{},
		frame:   &desktopFrame{},
		done:    make(chan struct{}),
	}
	m.items[id] = d
	m.mu.Unlock()

	m.logs.beginDesktop(desktopLogKey(id), host, sessionlogProtocol(host.Protocol))
	d.emit()
	go d.run()
	return d.state(), nil
}

// busy reports whether any desktop is still connecting or connected. A closed
// or failed session left in the map does not count: it holds no connection.
func (m *desktopManager) busy() bool {
	m.mu.Lock()
	items := make([]*desktopSession, 0, len(m.items))
	for _, item := range m.items {
		items = append(items, item)
	}
	m.mu.Unlock()
	for _, item := range items {
		switch item.state().State {
		case desktopConnecting, desktopPassword, desktopCertificate, desktopConnected:
			return true
		}
	}
	return false
}

func (m *desktopManager) get(id uint32) (*desktopSession, error) {
	m.mu.Lock()
	d := m.items[id]
	m.mu.Unlock()
	if d == nil {
		return nil, errorf(CodeNotFound, "no remote desktop with id %d", id)
	}
	return d, nil
}

func (m *desktopManager) list(id uint32) []desktopState {
	m.mu.Lock()
	items := make([]*desktopSession, 0, len(m.items))
	for _, d := range m.items {
		if id == 0 || d.id == id {
			items = append(items, d)
		}
	}
	m.mu.Unlock()
	out := make([]desktopState, 0, len(items))
	for _, d := range items {
		out = append(out, d.state())
	}
	return out
}

// closeAll stops every desktop and waits briefly for their final journal
// records to be submitted before the log queue is closed.
func (m *desktopManager) closeAll() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.closed = true
	items := make([]*desktopSession, 0, len(m.items))
	for _, d := range m.items {
		items = append(items, d)
	}
	m.mu.Unlock()
	for _, d := range items {
		d.close()
	}
	for _, d := range items {
		select {
		case <-d.done:
		case <-time.After(3 * time.Second):
		}
	}
}

// tunnel resolves the SSH connection a desktop travels through. The SSH host
// must already have an open, connected tab.
func (m *desktopManager) tunnel(hostID string) (*session, *sshconn.Conn, error) {
	if m.sessions == nil {
		return nil, nil, errorf(CodeUnavailable, "SSH tunnelling is not available")
	}
	tunnel, ok := m.sessions.byHost(hostID)
	if !ok {
		return nil, nil, errorf(CodeNotFound, "the SSH tunnel host is not open")
	}
	conn, connected := tunnel.connection()
	if !connected {
		label := tunnel.host.Label
		if label == "" {
			label = tunnel.host.Address
		}
		return nil, nil, errorf(CodeUnavailable, "connect %q before opening the remote desktop", label)
	}
	return tunnel, conn, nil
}

// desktopSession is one VNC or RDP connection in the background.
type desktopSession struct {
	id      uint32
	host    Host
	manager *desktopManager

	ctx    context.Context
	cancel context.CancelFunc

	reqWidth, reqHeight int
	scale               float32

	mu        sync.Mutex
	phase     string
	errText   string
	protocol  string
	name      string
	encrypted bool
	attempt   int
	asking    string
	trusted   map[string]bool
	sess      desktop.Session
	// tunnelDone ends the desktop when the SSH connection carrying it closes.
	tunnelDone      <-chan struct{}
	remoteClipboard string
	closed          bool
	// pumpErr is set when the picture cannot be held; run reports it instead
	// of treating the cancelled context as a user close.
	pumpErr error
	// filledSecret is the vault name whose password was answered without
	// asking, so a refusal can drop the stale value.
	filledSecret string
	// pendingSecret is the password the user asked to keep, written only once
	// the server accepts it.
	pendingSecret string

	answers  chan desktopAnswer
	frame    *desktopFrame
	done     chan struct{}
	pumpDone chan struct{}
}

func (d *desktopSession) logKey() uint32 { return desktopLogKey(d.id) }

func (d *desktopSession) live() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	// closed is set the moment desktop.close arrives, while the goroutine is
	// still winding the connection down: a reopen after a close must not be
	// handed the session that is leaving.
	return !d.closed && d.phase != desktopFailed && d.phase != desktopClosed
}

func (d *desktopSession) state() desktopState {
	d.mu.Lock()
	state := desktopState{
		ID:        d.id,
		HostID:    d.host.ID,
		Label:     d.host.Label,
		Address:   d.host.Address,
		Protocol:  d.host.Protocol,
		State:     d.phase,
		Error:     d.errText,
		Name:      d.name,
		Encrypted: d.encrypted,
		Attempt:   d.attempt,
	}
	if d.protocol != "" {
		state.Protocol = d.protocol
	}
	d.mu.Unlock()
	state.Width, state.Height = d.frame.size()
	return state
}

func (d *desktopSession) emit() {
	if d.manager == nil || d.manager.events == nil {
		return
	}
	d.manager.events.push("desktop.state", map[string]any{"desktop": d.state()})
}

func (d *desktopSession) emitPrompt(kind string, fields map[string]any) {
	if d.manager == nil || d.manager.events == nil {
		return
	}
	payload := map[string]any{"desktopId": d.id, "kind": kind}
	for key, value := range fields {
		payload[key] = value
	}
	d.manager.events.push("desktop.prompt", payload)
}

// close ends the session. run finalises the state and the journal record.
func (d *desktopSession) close() {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.closed = true
	sess := d.sess
	d.mu.Unlock()
	d.cancel()
	if sess != nil {
		_ = sess.Close()
	}
}

func (d *desktopSession) run() {
	defer close(d.done)
	defer d.cancel()
	var backoff time.Duration
	for {
		sess, err := d.connect()
		if err != nil {
			d.failConnect(err)
			return
		}
		if d.serve(sess) {
			return
		}
		// The connection broke or its stream went out of step: only a new
		// connection can bring the picture back. Wait a little longer after
		// each failure, and give up when the user closes the tab.
		select {
		case <-d.tunnelDone:
			// The SSH connection carrying this desktop is gone, and this
			// session cannot build another one.
			err := errors.New("the SSH tunnel closed")
			d.setState(desktopFailed, err)
			d.manager.logs.finish(d.logKey(), sessionlog.StatusFailed, sessionlog.ReasonNetworkLost)
			return
		default:
		}
		backoff = min(max(backoff*2, time.Second), 8*time.Second)
		d.setState(desktopConnecting, nil)
		select {
		case <-d.ctx.Done():
			d.setState(desktopClosed, nil)
			d.manager.logs.finish(d.logKey(), sessionlog.StatusEnded, sessionlog.ReasonCanceled)
			return
		case <-time.After(backoff):
		}
	}
}

// serve runs one connection until it ends and reports whether the session is
// over for good: the user closed it, or its picture cannot be held at all. A
// broken connection returns false, and run replaces it with a new one.
func (d *desktopSession) serve(sess desktop.Session) bool {
	d.mu.Lock()
	d.sess = sess
	d.phase = desktopConnected
	d.attempt = 0
	info := sess.Info()
	if info.Protocol != "" {
		d.protocol = strings.ToLower(info.Protocol)
	}
	d.name = info.Name
	d.encrypted = info.Encrypted
	tunnelDone := d.tunnelDone
	d.mu.Unlock()
	d.manager.logs.connectedDesktop(d.logKey())
	d.emit()
	sess.SetActive(true)

	pumpDone := make(chan struct{})
	d.mu.Lock()
	d.pumpDone = pumpDone
	d.mu.Unlock()
	go d.pump(sess, pumpDone)

	var reason error
	userClosed := false
	select {
	case <-d.ctx.Done():
		d.mu.Lock()
		userClosed = d.closed
		reason = d.pumpErr
		d.mu.Unlock()
	case <-sess.Done():
		reason = sess.Err()
	case <-tunnelDone:
		reason = errors.New("the SSH tunnel closed")
	}
	_ = sess.Close()
	<-pumpDone

	d.mu.Lock()
	closed := d.closed || userClosed
	pumpFailed := d.pumpErr != nil
	if pumpFailed {
		reason = d.pumpErr
	}
	d.sess = nil
	d.mu.Unlock()

	switch {
	case closed && reason == nil:
		d.setState(desktopClosed, nil)
		d.manager.logs.finish(d.logKey(), sessionlog.StatusEnded, sessionlog.ReasonUserClosed)
		return true
	case pumpFailed:
		// The screen is too large for this device: another connection would
		// only fail the same way.
		d.setState(desktopFailed, reason)
		d.manager.logs.finish(d.logKey(), sessionlog.StatusFailed, desktopReason(reason))
		return true
	default:
		if reason == nil {
			reason = errors.New("the server closed the connection")
		}
		// The journal keeps the broken attempt; the retry opens a new record.
		d.manager.logs.finish(d.logKey(), sessionlog.StatusFailed, desktopReason(reason))
		return false
	}
}

// connect opens the protocol session, asking for the password again after a
// refusal, and gives up on every other failure.
func (d *desktopSession) connect() (desktop.Session, error) {
	for {
		sess, err := d.openSession()
		if err == nil {
			d.commitPassword()
			return sess, nil
		}
		if d.ctx.Err() != nil {
			return nil, d.ctx.Err()
		}
		if !isDesktopAuthFailure(err) {
			return nil, err
		}
		d.forgetRefusedPassword()
	}
}

func (d *desktopSession) openSession() (desktop.Session, error) {
	host := d.host
	// Certificates are pinned under the address the user typed, not the
	// loopback a tunneled connection dials.
	pinAddress := net.JoinHostPort(host.Address, strconv.Itoa(int(host.Port)))
	address := pinAddress
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialer := net.Dialer{Timeout: desktopDialTimeout}
		return dialer.DialContext(ctx, network, addr)
	}
	options := desktop.Options{
		AskPassword: d.askPassword,
		Notify:      d.wakeup,
		Clipboard:   host.ClipboardEnabled(),
		// A phone is often on a slow link: 30 frames a second is smooth enough
		// for a desktop and caps what the server can send.
		RequestInterval: 33 * time.Millisecond,
	}
	if host.ViaSSH == "" {
		// Without a tunnel the desktop can leave through the host's own proxy,
		// the same one its terminal would use.
		proxy, err := hostProxyFor(d.manager.data, host)
		if err != nil {
			return nil, err
		}
		if proxy != nil && proxy.Configured() {
			dial = proxyDialer(proxy)
		}
	}
	if host.ViaSSH != "" {
		tunnel, conn, err := d.manager.tunnel(host.ViaSSH)
		if err != nil {
			return nil, err
		}
		// The VNC/RDP server is reached from the SSH server's own loopback.
		address = net.JoinHostPort("127.0.0.1", strconv.Itoa(int(host.Port)))
		dial = conn.DialContext
		options.Encrypted = true
		d.mu.Lock()
		d.tunnelDone = tunnel.done
		d.mu.Unlock()
	}
	if host.Protocol == ProtocolRDP {
		options.Username, options.Domain = rdpAccount(host.User)
		options.Console = host.Console
		options.Width, options.Height = d.reqWidth, d.reqHeight
		options.Scale = d.scale
		options.VerifyCertificate = func(_ string, der []byte) error {
			return d.verifyCertificate(pinAddress, der)
		}
		return rdp.Open(d.ctx, dial, address, options)
	}
	return vnc.Open(d.ctx, dial, address, options)
}

func (d *desktopSession) wakeup() {}

// askPassword is the secret question a VNC or RDP handshake raises. It blocks
// until the front end answers through desktop.answer or the session closes.
func (d *desktopSession) askPassword() (string, error) {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return "", context.Canceled
	}
	d.attempt++
	attempt := d.attempt
	d.asking = desktopPassword
	d.phase = desktopPassword
	d.errText = ""
	d.mu.Unlock()
	d.manager.logs.event(d.logKey(), sessionlog.EventAuthenticating)

	// The first attempt answers from the vault, so a password saved here (or
	// arriving through a backup or a sync) needs no typing. A refused one comes
	// back as a second attempt, which is always left to the user.
	if attempt == 1 {
		if value, name, ok := d.savedPassword(); ok {
			d.mu.Lock()
			d.asking = ""
			d.phase = desktopConnecting
			d.filledSecret = name
			d.mu.Unlock()
			d.emit()
			return value, nil
		}
	}

	d.emit()
	d.emitPrompt("password", map[string]any{"attempt": attempt})
	select {
	case answer := <-d.answers:
		d.mu.Lock()
		d.asking = ""
		d.phase = desktopConnecting
		d.mu.Unlock()
		d.emit()
		if answer.save && answer.value != "" && d.host.ID != "" {
			d.mu.Lock()
			d.pendingSecret = answer.value
			d.mu.Unlock()
		}
		return answer.value, nil
	case <-d.ctx.Done():
		return "", d.ctx.Err()
	}
}

// savedPassword is the password the vault holds for this host, under the
// desktop's name so both versions share one.
func (d *desktopSession) savedPassword() (string, string, bool) {
	if d.host.ID == "" || d.manager == nil || d.manager.data == nil {
		return "", "", false
	}
	name := hostSecretName(d.host.ID, hostPasswordPart)
	value, found, err := d.manager.data.secretGet(name)
	if err != nil || !found || value == "" {
		return "", "", false
	}
	return value, name, true
}

// commitPassword writes a password the server just accepted.
func (d *desktopSession) commitPassword() {
	d.mu.Lock()
	value := d.pendingSecret
	d.pendingSecret = ""
	d.filledSecret = ""
	d.mu.Unlock()
	if value == "" || d.host.ID == "" || d.manager == nil || d.manager.data == nil {
		return
	}
	if err := d.manager.data.secretSet(hostSecretName(d.host.ID, hostPasswordPart), value); err != nil {
		return
	}
	if d.manager.changed != nil {
		d.manager.changed()
	}
}

// forgetRefusedPassword drops a saved password the server has just refused, so
// the next connection asks instead of repeating the stale answer.
func (d *desktopSession) forgetRefusedPassword() {
	d.mu.Lock()
	name := d.filledSecret
	d.filledSecret = ""
	d.mu.Unlock()
	if name == "" || d.manager == nil || d.manager.data == nil {
		return
	}
	if err := d.manager.data.secretDelete(name); err != nil {
		return
	}
	if d.manager.changed != nil {
		d.manager.changed()
	}
}

// verifyCertificate decides on the X.509 certificate an RDP server presents.
// A pinned certificate passes; a new or changed one waits for the user, and a
// refusal ends the attempt.
func (d *desktopSession) verifyCertificate(address string, der []byte) error {
	fingerprint := rdp.Fingerprint(der)
	d.mu.Lock()
	trusted := d.trusted[fingerprint]
	d.mu.Unlock()
	if trusted {
		return nil
	}
	previous := ""
	if d.manager.pins != nil {
		pinned, err := d.manager.pins.Lookup(address)
		if err != nil {
			return fmt.Errorf("reading the trusted certificates: %w", err)
		}
		if pinned == fingerprint {
			d.mu.Lock()
			d.trusted[fingerprint] = true
			d.mu.Unlock()
			return nil
		}
		previous = pinned
	}

	d.mu.Lock()
	d.asking = desktopCertificate
	d.phase = desktopCertificate
	d.errText = ""
	d.attempt++
	attempt := d.attempt
	d.mu.Unlock()
	d.emit()
	fields := map[string]any{
		"attempt":     attempt,
		"address":     address,
		"fingerprint": fingerprint,
		"changed":     previous != "",
	}
	if previous != "" {
		fields["known"] = []string{previous}
	}
	d.emitPrompt("certificate", fields)
	d.manager.logs.event(d.logKey(), sessionlog.EventHostKeyPrompt)

	select {
	case answer := <-d.answers:
		d.mu.Lock()
		d.asking = ""
		d.phase = desktopConnecting
		d.mu.Unlock()
		d.emit()
		if !answer.accept {
			return errors.New("the certificate was rejected")
		}
		if answer.save && d.manager.pins != nil {
			if err := d.manager.pins.Remember(address, fingerprint); err != nil {
				return fmt.Errorf("remembering the certificate: %w", err)
			}
		}
		d.mu.Lock()
		d.trusted[fingerprint] = true
		d.mu.Unlock()
		return nil
	case <-d.ctx.Done():
		return d.ctx.Err()
	}
}

// answer hands one reply to the waiting handshake.
func (d *desktopSession) answer(kind, value string, accept, save bool) error {
	d.mu.Lock()
	asking := d.asking
	d.mu.Unlock()
	if asking == "" {
		return errorf(CodeConflict, "the remote desktop is not waiting for an answer")
	}
	if asking != kind {
		return errorf(CodeConflict, "the remote desktop is waiting for %s, not %s", asking, kind)
	}
	select {
	case d.answers <- desktopAnswer{kind: kind, value: value, accept: accept, save: save}:
		return nil
	default:
		return errorf(CodeConflict, "an answer is already pending")
	}
}

// pump copies the protocol's picture into the frame buffer and forwards the
// clipboard and bell. It is the only goroutine that calls Present.
func (d *desktopSession) pump(sess desktop.Session, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(desktopFramePeriod)
	defer ticker.Stop()
	bells := 0
	for {
		select {
		case <-d.ctx.Done():
			return
		case <-sess.Done():
			sess.Present(d.frame) // keep the last picture
			return
		case <-ticker.C:
		}
		sess.Present(d.frame)
		if width, height := d.frame.oversized(); width > 0 {
			d.mu.Lock()
			d.pumpErr = fmt.Errorf(
				"the remote picture is %d×%d, larger than this device can show; "+
					"lower the server's display resolution (at most %d megapixels)",
				width, height, maxDesktopPixels/1_000_000,
			)
			d.mu.Unlock()
			d.cancel()
			return
		}
		if text, ok := sess.RemoteClipboard(); ok && text != "" {
			d.mu.Lock()
			d.remoteClipboard = text
			d.mu.Unlock()
			d.manager.events.push("desktop.clipboard", map[string]any{"desktopId": d.id, "text": text})
		}
		if count := sess.Bells(); count > bells {
			bells = count
			d.manager.events.push("desktop.bell", map[string]any{"desktopId": d.id})
		}
	}
}

func (d *desktopSession) failConnect(err error) {
	d.mu.Lock()
	closed := d.closed
	if d.ctx.Err() != nil && !closed && d.pumpErr != nil {
		err = d.pumpErr
	}
	d.sess = nil
	d.mu.Unlock()
	switch {
	case closed || errors.Is(err, context.Canceled):
		d.setState(desktopClosed, nil)
		d.manager.logs.finish(d.logKey(), sessionlog.StatusEnded, sessionlog.ReasonCanceled)
	default:
		d.setState(desktopFailed, err)
		d.manager.logs.finish(d.logKey(), sessionlog.StatusFailed, desktopReason(err))
	}
}

func (d *desktopSession) setState(state string, err error) {
	d.mu.Lock()
	d.phase = state
	d.errText = ""
	if err != nil {
		d.errText = err.Error()
	}
	d.mu.Unlock()
	d.emit()
}

func (d *desktopSession) setActive(active bool) {
	d.mu.Lock()
	sess := d.sess
	d.mu.Unlock()
	if sess != nil {
		sess.SetActive(active)
	}
}

func (d *desktopSession) resize(width, height int, scale float64) error {
	d.mu.Lock()
	sess := d.sess
	d.mu.Unlock()
	if sess == nil {
		return errorf(CodeConflict, "the remote desktop is not connected")
	}
	resizer, ok := sess.(desktop.Resizer)
	if !ok || !resizer.CanResize() {
		return errorf(CodeUnavailable, "the server cannot resize the desktop")
	}
	width = min(max(width, 320), maxDesktopWidth)
	height = min(max(height, 240), maxDesktopHeight)
	if width%2 != 0 {
		width--
	}
	if height%2 != 0 {
		height--
	}
	if scale < 0.5 || scale > 4 {
		scale = 1
	}
	if err := resizer.Resize(width, height, float32(scale)); err != nil {
		return errorf(CodeUnavailable, "cannot resize the desktop: %v", err)
	}
	d.mu.Lock()
	d.reqWidth, d.reqHeight, d.scale = width, height, float32(scale)
	d.mu.Unlock()
	return nil
}

// refresh asks the server to draw the whole picture again. Servers sometimes
// stop sending updates after their screen layout changes (switching a macOS
// Space does); a full request is what wakes them up.
func (d *desktopSession) refresh() error {
	sess := d.session()
	if sess == nil {
		return errorf(CodeConflict, "the remote desktop is not connected")
	}
	if refresher, ok := sess.(interface{ Refresh() error }); ok {
		if err := refresher.Refresh(); err != nil {
			return errorf(CodeUnavailable, "cannot ask the server to redraw: %v", err)
		}
		return nil
	}
	// RDP: suppressing the output and resuming it makes the server repaint.
	sess.SetActive(false)
	sess.SetActive(true)
	return nil
}

// isDesktopAuthFailure reports whether the server refused the password, after
// which the front end is asked again.
func isDesktopAuthFailure(err error) bool {
	return errors.Is(err, rfb.ErrAuthFailed) || errors.Is(err, rdp.ErrAuthFailed)
}

// desktopReason maps a handshake failure to the journal's vocabulary.
func desktopReason(err error) sessionlog.Reason {
	switch {
	case isDesktopAuthFailure(err), errors.Is(err, rfb.ErrAuthRequired), errors.Is(err, rdp.ErrAuthRequired):
		return sessionlog.ReasonAuthFailed
	case strings.Contains(err.Error(), "certificate was rejected"):
		return sessionlog.ReasonHostKeyRejected
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return sessionlog.ReasonCanceled
	default:
		return sessionlog.ReasonConnectFailed
	}
}

// ---- frame transfer ----

// The binary frame layout MottyDesktopFrame writes, also documented in
// docs/ios/architecture.md:
//
//	0   u64 version
//	8   i32 width
//	12  i32 height
//	16  u32 rectCount
//	20  u32 reserved
//	24  rectCount × (i32 x, y, width, height)
//	    then each rectangle's BGRA pixels, rows tightly packed (width×4)
const (
	desktopFrameHeader  = 24
	desktopFrameRectLen = 16
)

// desktopFrame is the client-side copy of a remote picture. It mirrors what
// Present sends and keeps the rectangles not yet handed to the front end.
type desktopFrame struct {
	mu            sync.Mutex
	width, height int
	pix           []byte
	dirty         []desktop.Rect
	version       uint64
	overW, overH  int // set when a Resize asked for a picture too large to hold
}

func (f *desktopFrame) size() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.width, f.height
}

func (f *desktopFrame) oversized() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.overW, f.overH
}

// Resize implements desktop.Presenter.
func (f *desktopFrame) Resize(width, height int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if width <= 0 || height <= 0 {
		return
	}
	if width > maxDesktopWidth || height > maxDesktopHeight || width*height > maxDesktopPixels {
		f.overW, f.overH = width, height
		return
	}
	f.overW, f.overH = 0, 0
	if width == f.width && height == f.height && f.pix != nil {
		return
	}
	f.width, f.height = width, height
	f.pix = make([]byte, width*height*4)
	f.dirty = append(f.dirty[:0], desktop.Rect{Width: width, Height: height})
	f.version++
}

// Update implements desktop.Presenter.
func (f *desktopFrame) Update(x, y, width, height int, pix []byte, stride int) {
	if width <= 0 || height <= 0 || stride < width*4 {
		return
	}
	if len(pix) < (height-1)*stride+width*4 {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.width == 0 || f.pix == nil {
		return
	}
	left, top := max(x, 0), max(y, 0)
	right, bottom := min(x+width, f.width), min(y+height, f.height)
	if right <= left || bottom <= top {
		return
	}
	rect := desktop.Rect{X: left, Y: top, Width: right - left, Height: bottom - top}
	for row := top; row < bottom; row++ {
		source := pix[(row-y)*stride+(left-x)*4:]
		copy(f.pix[(row*f.width+left)*4:], source[:rect.Width*4])
	}
	f.markLocked(rect)
	f.version++
}

// markLocked records one changed rectangle. Beyond desktopDirtyLimit of them
// they collapse into their bounding box, so a flood of small updates costs
// one larger copy instead of an unbounded list.
func (f *desktopFrame) markLocked(rect desktop.Rect) {
	if rect.Empty() {
		return
	}
	if len(f.dirty) >= desktopDirtyLimit {
		union := rect
		for _, other := range f.dirty {
			union = union.Union(other)
		}
		f.dirty = append(f.dirty[:0], union)
		return
	}
	f.dirty = append(f.dirty, rect)
}

// snapshot writes the changes not yet handed out. It returns the byte count,
// 0 when the front end already has this version, or -needed when dst is too
// small. The dirty list is only cleared once the bytes fit, so a caller that
// retries with a bigger buffer loses nothing.
func (f *desktopFrame) snapshot(sinceVersion uint64, dst []byte) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.width <= 0 || f.height <= 0 || f.pix == nil || sinceVersion == f.version {
		return 0
	}
	rects := f.dirty
	full := sinceVersion == 0 || len(rects) == 0
	if full {
		rects = []desktop.Rect{{Width: f.width, Height: f.height}}
	}
	needed := desktopFrameHeader + len(rects)*desktopFrameRectLen
	for _, rect := range rects {
		needed += rect.Width * rect.Height * 4
	}
	if len(dst) < needed {
		return -needed
	}
	binary.LittleEndian.PutUint64(dst[0:], f.version)
	binary.LittleEndian.PutUint32(dst[8:], uint32(f.width))
	binary.LittleEndian.PutUint32(dst[12:], uint32(f.height))
	binary.LittleEndian.PutUint32(dst[16:], uint32(len(rects)))
	binary.LittleEndian.PutUint32(dst[20:], 0)
	offset := desktopFrameHeader
	for _, rect := range rects {
		binary.LittleEndian.PutUint32(dst[offset:], uint32(rect.X))
		binary.LittleEndian.PutUint32(dst[offset+4:], uint32(rect.Y))
		binary.LittleEndian.PutUint32(dst[offset+8:], uint32(rect.Width))
		binary.LittleEndian.PutUint32(dst[offset+12:], uint32(rect.Height))
		offset += desktopFrameRectLen
	}
	for _, rect := range rects {
		for row := rect.Y; row < rect.Y+rect.Height; row++ {
			source := f.pix[(row*f.width+rect.X)*4:]
			copy(dst[offset:], source[:rect.Width*4])
			offset += rect.Width * 4
		}
	}
	f.dirty = f.dirty[:0]
	return needed
}

// ---- RPC ----

func (a *App) callDesktop(method string, params json.RawMessage) (bool, any, error) {
	if !strings.HasPrefix(method, "desktop.") {
		return false, nil, nil
	}
	switch method {
	case "desktop.open":
		var request struct {
			HostID string  `json:"hostId"`
			Width  int     `json:"width"`
			Height int     `json:"height"`
			Scale  float64 `json:"scale"`
		}
		if err := decodeParams(params, &request); err != nil {
			return true, nil, err
		}
		host, ok := a.hosts.get(request.HostID)
		if !ok {
			return true, nil, errorf(CodeNotFound, "no host with id %q", request.HostID)
		}
		state, err := a.desktops.open(host, request.Width, request.Height, request.Scale)
		return true, map[string]any{"desktop": state}, err
	case "desktop.close":
		var request struct {
			ID uint32 `json:"id"`
		}
		if err := decodeParams(params, &request); err != nil {
			return true, nil, err
		}
		d, err := a.desktops.get(request.ID)
		if err != nil {
			return true, nil, err
		}
		d.close()
		return true, map[string]bool{"closed": true}, nil
	case "desktop.status":
		var request struct {
			ID uint32 `json:"id"`
		}
		if err := decodeParams(params, &request); err != nil {
			return true, nil, err
		}
		return true, map[string]any{"desktops": a.desktops.list(request.ID)}, nil
	case "desktop.refresh":
		var request struct {
			ID uint32 `json:"id"`
		}
		if err := decodeParams(params, &request); err != nil {
			return true, nil, err
		}
		d, err := a.desktops.get(request.ID)
		if err != nil {
			return true, nil, err
		}
		return true, map[string]bool{"sent": true}, d.refresh()
	case "desktop.pointer":
		var request struct {
			ID      uint32 `json:"id"`
			X       int    `json:"x"`
			Y       int    `json:"y"`
			Buttons uint8  `json:"buttons"`
		}
		if err := decodeParams(params, &request); err != nil {
			return true, nil, err
		}
		d, err := a.desktops.get(request.ID)
		if err != nil {
			return true, nil, err
		}
		sess := d.session()
		if sess == nil {
			return true, nil, errorf(CodeConflict, "the remote desktop is not connected")
		}
		return true, map[string]bool{"sent": true}, sess.SendPointer(request.X, request.Y, desktop.Buttons(request.Buttons&7))
	case "desktop.wheel":
		var request struct {
			ID      uint32 `json:"id"`
			X       int    `json:"x"`
			Y       int    `json:"y"`
			DX      int    `json:"dx"`
			DY      int    `json:"dy"`
			Buttons uint8  `json:"buttons"`
		}
		if err := decodeParams(params, &request); err != nil {
			return true, nil, err
		}
		d, err := a.desktops.get(request.ID)
		if err != nil {
			return true, nil, err
		}
		sess := d.session()
		if sess == nil {
			return true, nil, errorf(CodeConflict, "the remote desktop is not connected")
		}
		wheel := desktop.Wheel{X: request.DX, Y: request.DY}
		return true, map[string]bool{"sent": true}, sess.SendWheel(request.X, request.Y, wheel, desktop.Buttons(request.Buttons&7))
	case "desktop.key":
		var request struct {
			ID     uint32 `json:"id"`
			Key    string `json:"key"`
			Rune   string `json:"rune"`
			Typed  string `json:"typed"`
			Down   bool   `json:"down"`
			Repeat bool   `json:"repeat"`
		}
		if err := decodeParams(params, &request); err != nil {
			return true, nil, err
		}
		d, err := a.desktops.get(request.ID)
		if err != nil {
			return true, nil, err
		}
		sess := d.session()
		if sess == nil {
			return true, nil, errorf(CodeConflict, "the remote desktop is not connected")
		}
		event := desktop.KeyEvent{Down: request.Down, Repeat: request.Repeat}
		switch {
		case strings.TrimSpace(request.Rune) != "":
			r, _ := firstRune(request.Rune)
			event.Rune = r
		case strings.TrimSpace(request.Key) != "":
			event.Key = desktopKey(request.Key)
			if event.Key == desktop.KeyNone {
				return true, nil, errorf(CodeBadRequest, "unknown key %q", request.Key)
			}
		default:
			return true, nil, errorf(CodeBadRequest, "a key or a rune is required")
		}
		if typed, ok := firstRune(request.Typed); ok {
			event.Typed = typed
		}
		return true, map[string]bool{"sent": true}, sess.SendKey(event)
	case "desktop.text":
		var request struct {
			ID   uint32 `json:"id"`
			Text string `json:"text"`
		}
		if err := decodeParams(params, &request); err != nil {
			return true, nil, err
		}
		if len(request.Text) > desktopMaxTextBytes {
			return true, nil, errorf(CodeBadRequest, "the text is too long")
		}
		d, err := a.desktops.get(request.ID)
		if err != nil {
			return true, nil, err
		}
		sess := d.session()
		if sess == nil {
			return true, nil, errorf(CodeConflict, "the remote desktop is not connected")
		}
		return true, map[string]bool{"sent": true}, sess.SendText(request.Text)
	case "desktop.clipboard.set":
		var request struct {
			ID   uint32 `json:"id"`
			Text string `json:"text"`
		}
		if err := decodeParams(params, &request); err != nil {
			return true, nil, err
		}
		if len(request.Text) > desktopMaxTextBytes {
			return true, nil, errorf(CodeBadRequest, "the text is too long")
		}
		d, err := a.desktops.get(request.ID)
		if err != nil {
			return true, nil, err
		}
		sess := d.session()
		if sess == nil {
			return true, nil, errorf(CodeConflict, "the remote desktop is not connected")
		}
		return true, map[string]bool{"sent": true}, sess.SetClipboard(request.Text)
	case "desktop.clipboard.get":
		var request struct {
			ID uint32 `json:"id"`
		}
		if err := decodeParams(params, &request); err != nil {
			return true, nil, err
		}
		d, err := a.desktops.get(request.ID)
		if err != nil {
			return true, nil, err
		}
		d.mu.Lock()
		text := d.remoteClipboard
		d.mu.Unlock()
		return true, map[string]any{"text": text}, nil
	case "desktop.answer":
		var request struct {
			ID     uint32 `json:"id"`
			Kind   string `json:"kind"`
			Value  string `json:"value"`
			Accept bool   `json:"accept"`
			Save   bool   `json:"save"`
		}
		if err := decodeParams(params, &request); err != nil {
			return true, nil, err
		}
		d, err := a.desktops.get(request.ID)
		if err != nil {
			return true, nil, err
		}
		return true, map[string]bool{"answered": true}, d.answer(request.Kind, request.Value, request.Accept, request.Save)
	case "desktop.resize":
		var request struct {
			ID     uint32  `json:"id"`
			Width  int     `json:"width"`
			Height int     `json:"height"`
			Scale  float64 `json:"scale"`
		}
		if err := decodeParams(params, &request); err != nil {
			return true, nil, err
		}
		d, err := a.desktops.get(request.ID)
		if err != nil {
			return true, nil, err
		}
		return true, map[string]bool{"resized": true}, d.resize(request.Width, request.Height, request.Scale)
	case "desktop.active":
		var request struct {
			ID     uint32 `json:"id"`
			Active bool   `json:"active"`
		}
		if err := decodeParams(params, &request); err != nil {
			return true, nil, err
		}
		d, err := a.desktops.get(request.ID)
		if err != nil {
			return true, nil, err
		}
		d.setActive(request.Active)
		return true, map[string]bool{"updated": true}, nil
	}
	return true, nil, errorf(CodeBadRequest, "unknown method %q", method)
}

func (d *desktopSession) session() desktop.Session {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.sess
}

func firstRune(text string) (rune, bool) {
	for _, r := range text {
		return r, true
	}
	return 0, false
}

// desktopKey maps the key names the front end sends to the backend's keys.
func desktopKey(name string) desktop.Key {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "enter", "return":
		return desktop.KeyEnter
	case "tab":
		return desktop.KeyTab
	case "backspace":
		return desktop.KeyBackspace
	case "escape", "esc":
		return desktop.KeyEscape
	case "delete", "forwarddelete":
		return desktop.KeyDelete
	case "insert":
		return desktop.KeyInsert
	case "home":
		return desktop.KeyHome
	case "end":
		return desktop.KeyEnd
	case "pageup", "prior":
		return desktop.KeyPageUp
	case "pagedown", "next":
		return desktop.KeyPageDown
	case "left", "arrowleft":
		return desktop.KeyLeft
	case "right", "arrowright":
		return desktop.KeyRight
	case "up", "arrowup":
		return desktop.KeyUp
	case "down", "arrowdown":
		return desktop.KeyDown
	case "shift":
		return desktop.KeyShift
	case "control", "ctrl":
		return desktop.KeyControl
	case "alt", "option":
		return desktop.KeyAlt
	case "meta", "command", "cmd", "win", "super":
		return desktop.KeyMeta
	case "capslock":
		return desktop.KeyCapsLock
	case "menu", "contextmenu":
		return desktop.KeyMenu
	}
	if len(name) >= 2 && (name[0] == 'f' || name[0] == 'F') {
		if number, err := strconv.Atoi(name[1:]); err == nil && number >= 1 && number <= 12 {
			return desktop.KeyF1 + desktop.Key(number-1)
		}
	}
	return desktop.KeyNone
}
