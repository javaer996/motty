package mobilecore

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"strconv"
	"testing"
	"time"
)

func TestDesktopFrameSnapshotDelivery(t *testing.T) {
	frame := &desktopFrame{}
	frame.Resize(4, 2)
	pixels := make([]byte, 4*2*4)
	for index := range pixels {
		pixels[index] = byte(index)
	}
	frame.Update(0, 0, 4, 2, pixels, 4*4)

	buffer := make([]byte, 1024)
	written := frame.snapshot(0, buffer)
	if written <= 0 {
		t.Fatalf("first snapshot returned %d", written)
	}
	if version := binary.LittleEndian.Uint64(buffer); version == 0 {
		t.Fatal("snapshot has no version")
	}
	if width, height := binary.LittleEndian.Uint32(buffer[8:]), binary.LittleEndian.Uint32(buffer[12:]); width != 4 || height != 2 {
		t.Fatalf("size = %d×%d", width, height)
	}
	if count := binary.LittleEndian.Uint32(buffer[16:]); count != 1 {
		t.Fatalf("rect count = %d", count)
	}
	if x, y, w, h := binary.LittleEndian.Uint32(buffer[24:]), binary.LittleEndian.Uint32(buffer[28:]),
		binary.LittleEndian.Uint32(buffer[32:]), binary.LittleEndian.Uint32(buffer[36:]); x != 0 || y != 0 || w != 4 || h != 2 {
		t.Fatalf("rect = %d,%d %d×%d", x, y, w, h)
	}
	if !bytes.Equal(buffer[40:40+len(pixels)], pixels) {
		t.Fatal("the pixels did not survive the snapshot")
	}

	version := binary.LittleEndian.Uint64(buffer)
	if again := frame.snapshot(version, buffer); again != 0 {
		t.Fatalf("a repeated snapshot returned %d", again)
	}
	if small := frame.snapshot(0, buffer[:10]); small >= 0 {
		t.Fatalf("a tiny buffer returned %d, want -needed", small)
	}

	frame.Update(1, 0, 1, 1, []byte{9, 8, 7, 0}, 4)
	written = frame.snapshot(version, buffer)
	if written <= 0 {
		t.Fatalf("incremental snapshot returned %d", written)
	}
	if count := binary.LittleEndian.Uint32(buffer[16:]); count != 1 {
		t.Fatalf("incremental rect count = %d", count)
	}
	if x, y := binary.LittleEndian.Uint32(buffer[24:]), binary.LittleEndian.Uint32(buffer[28:]); x != 1 || y != 0 {
		t.Fatalf("incremental rect at %d,%d", x, y)
	}
	if !bytes.Equal(buffer[40:44], []byte{9, 8, 7, 0}) {
		t.Fatalf("incremental pixel = %v", buffer[40:44])
	}

	frame.Resize(20000, 20000)
	if width, height := frame.oversized(); width != 20000 || height != 20000 {
		t.Fatalf("oversized = %d×%d", width, height)
	}

	// Real desktops beyond the largest iPad must connect: a 3306×2100 VNC
	// server is ordinary, not an attack.
	frame.Resize(3306, 2100)
	if width, height := frame.oversized(); width != 0 || height != 0 {
		t.Fatalf("a 3306×2100 desktop was refused as %d×%d", width, height)
	}
	if width, height := frame.size(); width != 3306 || height != 2100 {
		t.Fatalf("size = %d×%d, want 3306×2100", width, height)
	}

	frame.Resize(5121, 100)
	if width, height := frame.oversized(); width != 5121 || height != 100 {
		t.Fatalf("oversized = %d×%d", width, height)
	}
}

func TestDesktopSessionBeingClosedIsNotLive(t *testing.T) {
	// desktop.open must not hand a reopened host the session that desktop.close
	// is still winding down.
	d := &desktopSession{phase: desktopConnected}
	if !d.live() {
		t.Fatal("a connected session is not live")
	}
	d.closed = true
	if d.live() {
		t.Fatal("a session being closed is still reported live")
	}
}

func TestHostProtocolDefaults(t *testing.T) {
	vnc := Host{Label: "desk", Address: "example.com", Protocol: "VNC"}.Normalized()
	if vnc.Protocol != ProtocolVNC || vnc.Port != 5900 {
		t.Fatalf("vnc host = protocol %q port %d", vnc.Protocol, vnc.Port)
	}
	if vnc.Clipboard == nil || !*vnc.Clipboard {
		t.Fatal("a VNC host should share the clipboard by default")
	}
	if err := vnc.Validate(); err != nil {
		t.Fatalf("a VNC host needs no account: %v", err)
	}

	rdp := Host{Label: "win", Address: "example.com", Protocol: "rdp"}.Normalized()
	if rdp.Port != 3389 {
		t.Fatalf("rdp port = %d", rdp.Port)
	}
	if err := rdp.Validate(); err != nil {
		t.Fatalf("an RDP host may leave the account empty: %v", err)
	}

	ssh := Host{Label: "shell", Address: "example.com"}.Normalized()
	if ssh.Protocol != ProtocolSSH || ssh.Port != 22 || ssh.Clipboard != nil {
		t.Fatalf("ssh host = %+v", ssh)
	}
	if err := ssh.Validate(); err == nil {
		t.Fatal("an SSH host without an account must be rejected")
	}

	bad := Host{Label: "bad", Address: "example.com", Protocol: "telnet"}.Normalized()
	if err := bad.Validate(); err == nil {
		t.Fatal("an unknown protocol must be rejected")
	}
}

// ---- fake RFB server ----

type vncMessage struct {
	kind byte
	body []byte
}

type vncTestServer struct {
	listener net.Listener
	port     uint16
	password bool

	messages chan vncMessage
	update   chan []byte
	accepted chan struct{}
}

func startVNCTestServer(t *testing.T, password bool) *vncTestServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &vncTestServer{
		listener: listener,
		port:     uint16(listener.Addr().(*net.TCPAddr).Port),
		password: password,
		messages: make(chan vncMessage, 256),
		update:   make(chan []byte, 4),
		accepted: make(chan struct{}, 4),
	}
	t.Cleanup(func() { listener.Close() })
	go server.serve()
	return server
}

func (s *vncTestServer) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		select {
		case s.accepted <- struct{}{}:
		default:
		}
		s.serveConnection(conn)
	}
}

func (s *vncTestServer) serveConnection(conn net.Conn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	conn.Write([]byte("RFB 003.008\n"))
	if _, err := io.ReadFull(conn, make([]byte, 12)); err != nil {
		return
	}
	if s.password {
		conn.Write([]byte{1, 2}) // one security type: VNC authentication
		if _, err := io.ReadFull(conn, make([]byte, 1)); err != nil {
			return
		}
		conn.Write(bytes.Repeat([]byte{7}, 16)) // challenge
		if _, err := io.ReadFull(conn, make([]byte, 16)); err != nil {
			return
		}
		conn.Write([]byte{0, 0, 0, 0}) // any password is accepted
	} else {
		conn.Write([]byte{1, 1}) // one security type: none
		if _, err := io.ReadFull(conn, make([]byte, 1)); err != nil {
			return
		}
		conn.Write([]byte{0, 0, 0, 0}) // SecurityResult: OK (RFB 3.8)
	}
	if _, err := io.ReadFull(conn, make([]byte, 1)); err != nil { // ClientInit
		return
	}
	init := []byte{0, 4, 0, 2} // 4×2
	init = append(init, 32, 24, 0, 1, 0, 255, 0, 255, 0, 255, 16, 8, 0, 0, 0, 0)
	init = append(init, 0, 0, 0, 4)
	if _, err := conn.Write(append(init, "fake"...)); err != nil {
		return
	}

	// Updates may be written at any time: the client's reader accepts an
	// unsolicited update, and this keeps the input loop free to read.
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			select {
			case update := <-s.update:
				if _, err := conn.Write(update); err != nil {
					return
				}
			case <-done:
				return
			}
		}
	}()

	for {
		kind, body, err := readVNCClientMessage(conn)
		if err != nil {
			return
		}
		select {
		case s.messages <- vncMessage{kind: kind, body: body}:
		default:
		}
	}
}

func readVNCClientMessage(conn net.Conn) (byte, []byte, error) {
	var kind [1]byte
	if _, err := io.ReadFull(conn, kind[:]); err != nil {
		return 0, nil, err
	}
	var length int
	switch kind[0] {
	case 0: // SetPixelFormat: padding (3) + pixel format (16)
		length = 19
	case 2: // SetEncodings: padding (1) + count (2) + encodings
		var count [3]byte
		if _, err := io.ReadFull(conn, count[:]); err != nil {
			return 0, nil, err
		}
		length = int(binary.BigEndian.Uint16(count[1:])) * 4
	case 3: // FramebufferUpdateRequest
		length = 9
	case 4: // KeyEvent
		length = 7
	case 5: // PointerEvent
		length = 5
	case 6: // ClientCutText: padding (3) + length (4) + text
		var header [7]byte
		if _, err := io.ReadFull(conn, header[:]); err != nil {
			return 0, nil, err
		}
		text := make([]byte, binary.BigEndian.Uint32(header[3:]))
		if _, err := io.ReadFull(conn, text); err != nil {
			return 0, nil, err
		}
		return kind[0], append(header[:], text...), nil
	default:
		return 0, nil, io.ErrUnexpectedEOF
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(conn, body); err != nil {
		return 0, nil, err
	}
	return kind[0], body, nil
}

func saveDesktopTestHost(t *testing.T, app *App, port uint16, protocol string) Host {
	t.Helper()
	body := `{"label":"desk","address":"127.0.0.1","protocol":` + quoteJSON(protocol) +
		`,"port":` + strconv.Itoa(int(port)) + `}`
	envelope := callApp(t, app, "hosts.save", body)
	if !envelope.OK {
		t.Fatalf("hosts.save failed: %+v", envelope.Error)
	}
	var host Host
	if err := json.Unmarshal(envelope.Result, &host); err != nil {
		t.Fatal(err)
	}
	return host
}

type desktopListResult struct {
	Desktops []desktopState `json:"desktops"`
}

func desktopList(t *testing.T, app *App, id uint32) []desktopState {
	t.Helper()
	envelope := callApp(t, app, "desktop.status", `{"id":`+strconv.Itoa(int(id))+`}`)
	if !envelope.OK {
		t.Fatalf("desktop.status failed: %+v", envelope.Error)
	}
	var result desktopListResult
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		t.Fatal(err)
	}
	return result.Desktops
}

func waitDesktopState(t *testing.T, app *App, id uint32, wanted string) desktopState {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		list := desktopList(t, app, id)
		if len(list) == 1 && list[0].State == wanted {
			return list[0]
		}
		if len(list) == 1 && list[0].State == desktopFailed && wanted != desktopFailed {
			t.Fatalf("desktop failed while waiting for %q: %s", wanted, list[0].Error)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the desktop never reached %q", wanted)
	return desktopState{}
}

func pollEvent(t *testing.T, app *App, wanted string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	buffer := make([]byte, 64*1024)
	for time.Now().Before(deadline) {
		n := app.PollEvent(buffer)
		if n <= 0 {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		var event map[string]any
		if err := json.Unmarshal(buffer[:n], &event); err != nil {
			t.Fatalf("malformed event: %v", err)
		}
		if event["type"] == wanted {
			return event
		}
	}
	t.Fatalf("no %q event", wanted)
	return nil
}

func TestDesktopVNCConnectsDeliversFramesAndInput(t *testing.T) {
	server := startVNCTestServer(t, false)
	app, err := Start(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	host := saveDesktopTestHost(t, app, server.port, ProtocolVNC)

	opened := callApp(t, app, "desktop.open",
		`{"hostId":`+quoteJSON(host.ID)+`,"width":800,"height":600,"scale":2}`)
	if !opened.OK {
		t.Fatalf("desktop.open failed: %+v", opened.Error)
	}
	var result struct {
		Desktop desktopState `json:"desktop"`
	}
	if err := json.Unmarshal(opened.Result, &result); err != nil {
		t.Fatal(err)
	}
	connected := waitDesktopState(t, app, result.Desktop.ID, desktopConnected)
	if connected.Protocol != "vnc" || connected.Name != "fake" {
		t.Fatalf("connected state = %+v", connected)
	}

	// A full 4×2 raw update, one color plus a per-pixel pattern.
	pixels := make([]byte, 4*2*4)
	for index := 0; index < 8; index++ {
		pixels[index*4+0] = 10
		pixels[index*4+1] = 20
		pixels[index*4+2] = 30
	}
	update := []byte{0, 0, 0, 1, 0, 0, 0, 0, 0, 4, 0, 2, 0, 0, 0, 0}
	server.update <- append(update, pixels...)

	buffer := make([]byte, 4096)
	deadline := time.Now().Add(10 * time.Second)
	var version uint64
	for time.Now().Before(deadline) {
		n := app.DesktopFrame(result.Desktop.ID, version, buffer)
		if n > 0 {
			version = binary.LittleEndian.Uint64(buffer)
			if width, height := binary.LittleEndian.Uint32(buffer[8:]), binary.LittleEndian.Uint32(buffer[12:]); width != 4 || height != 2 {
				t.Fatalf("frame size = %d×%d", width, height)
			}
			if !bytes.Equal(buffer[40:40+len(pixels)], pixels) {
				t.Fatalf("frame pixels = %v", buffer[40:40+len(pixels)])
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if version == 0 {
		t.Fatal("no frame arrived")
	}

	if envelope := callApp(t, app, "desktop.pointer",
		`{"id":`+strconv.Itoa(int(result.Desktop.ID))+`,"x":3,"y":1,"buttons":1}`); !envelope.OK {
		t.Fatalf("desktop.pointer failed: %+v", envelope.Error)
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case message := <-server.messages:
			if message.kind == 5 {
				x := int(binary.BigEndian.Uint16(message.body[1:]))
				y := int(binary.BigEndian.Uint16(message.body[3:]))
				if message.body[0] != 1 || x != 3 || y != 1 {
					t.Fatalf("pointer message = %v", message.body)
				}
				goto closed
			}
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatal("the pointer event never reached the server")

closed:
	if envelope := callApp(t, app, "desktop.close", `{"id":`+strconv.Itoa(int(result.Desktop.ID))+`}`); !envelope.OK {
		t.Fatalf("desktop.close failed: %+v", envelope.Error)
	}
	waitDesktopState(t, app, result.Desktop.ID, desktopClosed)
}

func TestDesktopVNCRecoversThePictureAfterAPartialFrameStalls(t *testing.T) {
	server := startVNCTestServer(t, false)
	app, err := Start(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	host := saveDesktopTestHost(t, app, server.port, ProtocolVNC)
	opened := callApp(t, app, "desktop.open", `{"hostId":`+quoteJSON(host.ID)+`}`)
	if !opened.OK {
		t.Fatalf("desktop.open failed: %+v", opened.Error)
	}
	var result struct {
		Desktop desktopState `json:"desktop"`
	}
	if err := json.Unmarshal(opened.Result, &result); err != nil {
		t.Fatal(err)
	}
	id := result.Desktop.ID
	waitDesktopState(t, app, id, desktopConnected)
	<-server.accepted

	frame := func(color byte) []byte {
		// One 4×2 raw rectangle, with every byte equal for easy verification.
		return append([]byte{0, 0, 0, 1, 0, 0, 0, 0, 0, 4, 0, 2, 0, 0, 0, 0}, bytes.Repeat([]byte{color}, 32)...)
	}
	version := uint64(0)
	waitPicture := func(color byte) {
		t.Helper()
		buffer := make([]byte, 256)
		for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
			// Read a complete snapshot: reconnecting can accumulate several
			// overlapping dirty rectangles before the display polls them.
			if n := app.DesktopFrame(id, 0, buffer); n >= 72 {
				next := binary.LittleEndian.Uint64(buffer)
				if next > version && binary.LittleEndian.Uint32(buffer[16:]) == 1 && bytes.Equal(buffer[40:72], bytes.Repeat([]byte{color}, 32)) {
					version = next
					return
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("the phone never received picture %d", color)
	}
	server.update <- frame(1)
	waitPicture(1)

	params := `{"id":` + strconv.Itoa(int(id)) + `,"x":1,"y":1,"dy":2}`
	if sent := callApp(t, app, "desktop.wheel", params); !sent.OK {
		t.Fatalf("desktop.wheel failed: %+v", sent.Error)
	}
	// The terminal starts answering the scroll but stops one byte short. A
	// manual refresh still cannot complete this old message; the watchdog
	// must tear down the connection and the mobile session must replace it.
	partial := frame(2)
	server.update <- partial[:len(partial)-1]
	if refreshed := callApp(t, app, "desktop.refresh", `{"id":`+strconv.Itoa(int(id))+`}`); !refreshed.OK {
		t.Fatalf("desktop.refresh failed: %+v", refreshed.Error)
	}
	select {
	case <-server.accepted:
	case <-time.After(15 * time.Second):
		t.Fatal("a stalled picture did not automatically reconnect")
	}
	waitDesktopState(t, app, id, desktopConnected)
	server.update <- frame(3)
	waitPicture(3)
	server.update <- frame(4)
	waitPicture(4)
}

func TestDesktopVNCAsksForPasswordAndRecordsTheSession(t *testing.T) {
	server := startVNCTestServer(t, true)
	app, err := Start(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	host := saveDesktopTestHost(t, app, server.port, ProtocolVNC)

	opened := callApp(t, app, "desktop.open", `{"hostId":`+quoteJSON(host.ID)+`}`)
	if !opened.OK {
		t.Fatalf("desktop.open failed: %+v", opened.Error)
	}
	var result struct {
		Desktop desktopState `json:"desktop"`
	}
	if err := json.Unmarshal(opened.Result, &result); err != nil {
		t.Fatal(err)
	}
	event := pollEvent(t, app, "desktop.prompt")
	if event["kind"] != "password" {
		t.Fatalf("prompt = %+v", event)
	}
	waitDesktopState(t, app, result.Desktop.ID, desktopPassword)

	answered := callApp(t, app, "desktop.answer",
		`{"id":`+strconv.Itoa(int(result.Desktop.ID))+`,"kind":"password","value":"secret"}`)
	if !answered.OK {
		t.Fatalf("desktop.answer failed: %+v", answered.Error)
	}
	waitDesktopState(t, app, result.Desktop.ID, desktopConnected)

	// The journal records the connection as a VNC session, active while it runs.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		listed := callApp(t, app, "logs.list", "")
		if !listed.OK {
			t.Fatalf("logs.list failed: %+v", listed.Error)
		}
		var logs struct {
			Records []logRecordJSON `json:"records"`
		}
		if err := json.Unmarshal(listed.Result, &logs); err != nil {
			t.Fatal(err)
		}
		if len(logs.Records) == 1 {
			if logs.Records[0].Protocol != "vnc" || logs.Records[0].Status != "active" {
				t.Fatalf("journal record = %+v", logs.Records[0])
			}
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
}

func TestDesktopVNCLoginSurvivesStreamFailureAndImmediateReopen(t *testing.T) {
	server := startVNCTestServer(t, true)
	app, err := Start(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	host := saveDesktopTestHost(t, app, server.port, ProtocolVNC)
	open := func() uint32 {
		t.Helper()
		envelope := callApp(t, app, "desktop.open", `{"hostId":`+quoteJSON(host.ID)+`}`)
		if !envelope.OK {
			t.Fatalf("open failed: %+v", envelope.Error)
		}
		var result struct {
			Desktop desktopState `json:"desktop"`
		}
		if err := json.Unmarshal(envelope.Result, &result); err != nil {
			t.Fatal(err)
		}
		return result.Desktop.ID
	}
	id := open()
	for attempt := 0; attempt < 3; attempt++ {
		state := waitDesktopState(t, app, id, desktopPassword)
		if state.Attempt != 1 {
			t.Fatalf("new connection reused an old login attempt: %+v", state)
		}
		event := pollEvent(t, app, "desktop.prompt")
		if event["desktopId"] != float64(id) || event["kind"] != "password" {
			t.Fatalf("wrong reconnect prompt: %+v", event)
		}
		answer := callApp(t, app, "desktop.answer", `{"id":`+strconv.Itoa(int(id))+`,"kind":"password","value":"secret","save":false}`)
		if !answer.OK {
			t.Fatalf("reconnect login failed: %+v", answer.Error)
		}
		waitDesktopState(t, app, id, desktopConnected)
		switch attempt {
		case 0:
			// A broken decoder stream must reconnect and ask again rather
			// than reusing an already-answered login prompt.
			server.update <- []byte{127}
		case 1:
			closed := callApp(t, app, "desktop.close", `{"id":`+strconv.Itoa(int(id))+`}`)
			if !closed.OK {
				t.Fatalf("close failed: %+v", closed.Error)
			}
			previous := id
			id = open() // immediately, before the old state finishes closing
			if id == previous {
				t.Fatal("reopen returned the broken connection")
			}
		}
	}
	server.update <- append([]byte{0, 0, 0, 1, 0, 0, 0, 0, 0, 4, 0, 2, 0, 0, 0, 0}, bytes.Repeat([]byte{9}, 32)...)
	buffer := make([]byte, 256)
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		if n := app.DesktopFrame(id, 0, buffer); n >= 72 && bytes.Equal(buffer[40:72], bytes.Repeat([]byte{9}, 32)) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("login succeeded but the new desktop picture never arrived")
}

func TestDesktopVNCUsesASavedPasswordWithoutAsking(t *testing.T) {
	server := startVNCTestServer(t, true)
	app, err := Start(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	host := saveDesktopTestHost(t, app, server.port, ProtocolVNC)

	saved := callApp(t, app, "hosts.password", `{"id":`+quoteJSON(host.ID)+`,"password":"secret"}`)
	if !saved.OK {
		t.Fatalf("hosts.password failed: %+v", saved.Error)
	}

	opened := callApp(t, app, "desktop.open", `{"hostId":`+quoteJSON(host.ID)+`}`)
	if !opened.OK {
		t.Fatalf("desktop.open failed: %+v", opened.Error)
	}
	var result struct {
		Desktop desktopState `json:"desktop"`
	}
	if err := json.Unmarshal(opened.Result, &result); err != nil {
		t.Fatal(err)
	}
	// A prompt would leave the session waiting for an answer; reaching
	// connected proves the vault answered instead.
	waitDesktopState(t, app, result.Desktop.ID, desktopConnected)
}

func TestDesktopVNCWritesThePasswordOnlyAfterTheServerAcceptsIt(t *testing.T) {
	server := startVNCTestServer(t, true)
	app, err := Start(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	host := saveDesktopTestHost(t, app, server.port, ProtocolVNC)
	name := hostSecretName(host.ID, hostPasswordPart)

	opened := callApp(t, app, "desktop.open", `{"hostId":`+quoteJSON(host.ID)+`}`)
	if !opened.OK {
		t.Fatalf("desktop.open failed: %+v", opened.Error)
	}
	var result struct {
		Desktop desktopState `json:"desktop"`
	}
	if err := json.Unmarshal(opened.Result, &result); err != nil {
		t.Fatal(err)
	}
	event := pollEvent(t, app, "desktop.prompt")
	if event["kind"] != "password" {
		t.Fatalf("prompt = %+v", event)
	}
	waitDesktopState(t, app, result.Desktop.ID, desktopPassword)
	if _, found, _ := app.data.secretGet(name); found {
		t.Fatal("the password was written before the server accepted it")
	}

	answered := callApp(t, app, "desktop.answer",
		`{"id":`+strconv.Itoa(int(result.Desktop.ID))+`,"kind":"password","value":"secret","save":true}`)
	if !answered.OK {
		t.Fatalf("desktop.answer failed: %+v", answered.Error)
	}
	waitDesktopState(t, app, result.Desktop.ID, desktopConnected)
	if value, found, err := app.data.secretGet(name); err != nil || !found || value != "secret" {
		t.Fatalf("vault password = %q, %v, %v", value, found, err)
	}
}
