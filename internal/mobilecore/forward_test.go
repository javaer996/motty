package mobilecore

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"motty/internal/portforward"
)

func TestValidateForwardNormalizesNames(t *testing.T) {
	config, mode, err := validateForward(forwardConfig{
		HostID:      " host-1 ",
		Mode:        "LOCAL",
		LocalPort:   8080,
		Destination: " LocalHost:5432 ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if mode != portforward.ModeLocal || config.Mode != "local" {
		t.Fatalf("mode = %v/%q", mode, config.Mode)
	}
	if config.HostID != "host-1" || config.Destination != "LocalHost:5432" {
		t.Fatalf("fields were not trimmed: %+v", config)
	}
	if config.Label != "local 8080" {
		t.Fatalf("default label = %q", config.Label)
	}

	socks, socksMode, err := validateForward(forwardConfig{
		HostID:      "host-1",
		Mode:        "socks5",
		LocalPort:   1080,
		Destination: "ignored:1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if socksMode != portforward.ModeSOCKS5 || socks.Destination != "" {
		t.Fatalf("socks5 = %+v", socks)
	}

	http, httpMode, err := validateForward(forwardConfig{
		HostID:    "host-1",
		Mode:      "http-connect",
		LocalPort: 8888,
	})
	if err != nil {
		t.Fatal(err)
	}
	if httpMode != portforward.ModeHTTPConnect || http.Mode != "http-connect" {
		t.Fatalf("http-connect = %+v", http)
	}
}

func TestValidateForwardRejectsBadInput(t *testing.T) {
	cases := []forwardConfig{
		{Mode: "local", LocalPort: 8080, Destination: "db:5432"},
		{HostID: "h", Mode: "local", LocalPort: 0, Destination: "db:5432"},
		{HostID: "h", Mode: "local", LocalPort: 8080, Destination: "db"},
		{HostID: "h", Mode: "local", LocalPort: 8080, Destination: ":5432"},
		{HostID: "h", Mode: "local", LocalPort: 8080, Destination: "db:0"},
		{HostID: "h", Mode: "remote", LocalPort: 8080},
		{HostID: "h", Mode: "udp", LocalPort: 8080},
	}
	for index, config := range cases {
		if _, _, err := validateForward(config); err == nil {
			t.Fatalf("case %d was accepted: %+v", index, config)
		}
	}
}

func TestForwardPersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	app, err := Start(Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	added := callApp(t, app, "forwards.add",
		`{"label":"database","hostId":"host-1","mode":"local","localPort":15432,"destination":"localhost:5432","autoStart":true}`)
	if !added.OK {
		t.Fatalf("add failed: %+v", added)
	}
	var addedResult struct {
		Forward forwardState `json:"forward"`
	}
	if err := json.Unmarshal(added.Result, &addedResult); err != nil {
		t.Fatal(err)
	}
	if addedResult.Forward.ID == "" || addedResult.Forward.State != "stopped" {
		t.Fatalf("added = %+v", addedResult.Forward)
	}
	if addedResult.Forward.ListenAddress != "127.0.0.1:15432" {
		t.Fatalf("listen address = %q", addedResult.Forward.ListenAddress)
	}

	data, err := os.ReadFile(filepath.Join(dir, "forwards.json"))
	if err != nil {
		t.Fatal(err)
	}
	var stored struct {
		Forwards []forwardConfig `json:"forwards"`
	}
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored.Forwards) != 1 || stored.Forwards[0].Label != "database" || !stored.Forwards[0].AutoStart {
		t.Fatalf("stored = %+v", stored.Forwards)
	}

	reloaded, err := newForwardManager(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	list := reloaded.list()
	if len(list) != 1 || list[0].ID != addedResult.Forward.ID || list[0].HostID != "host-1" {
		t.Fatalf("reloaded = %+v", list)
	}
}

func TestForwardStartWithoutSessionIsReported(t *testing.T) {
	app, err := Start(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	added := callApp(t, app, "forwards.add",
		`{"hostId":"host-1","mode":"local","localPort":15432,"destination":"localhost:5432"}`)
	if !added.OK {
		t.Fatalf("add failed: %+v", added)
	}
	var addedResult struct {
		Forward forwardState `json:"forward"`
	}
	if err := json.Unmarshal(added.Result, &addedResult); err != nil {
		t.Fatal(err)
	}

	started := callApp(t, app, "forwards.start", `{"id":`+quoteJSON(addedResult.Forward.ID)+`}`)
	if !started.OK {
		t.Fatalf("start returned an RPC error: %+v", started)
	}
	var startResult struct {
		Forward forwardState `json:"forward"`
	}
	if err := json.Unmarshal(started.Result, &startResult); err != nil {
		t.Fatal(err)
	}
	if startResult.Forward.State != "failed" || startResult.Forward.Error == "" {
		t.Fatalf("start without a session = %+v", startResult.Forward)
	}
}

func TestForwardUpdateRemoveAndUnknownIDs(t *testing.T) {
	app, err := Start(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	added := callApp(t, app, "forwards.add",
		`{"hostId":"host-1","mode":"local","localPort":15432,"destination":"localhost:5432"}`)
	var addedResult struct {
		Forward forwardState `json:"forward"`
	}
	if err := json.Unmarshal(added.Result, &addedResult); err != nil {
		t.Fatal(err)
	}
	id := addedResult.Forward.ID

	updated := callApp(t, app, "forwards.update",
		`{"id":`+quoteJSON(id)+`,"label":"renamed","hostId":"host-1","mode":"local","localPort":15433,"destination":"db:5432"}`)
	if !updated.OK {
		t.Fatalf("update failed: %+v", updated)
	}
	var updateResult struct {
		Forward forwardState `json:"forward"`
	}
	if err := json.Unmarshal(updated.Result, &updateResult); err != nil {
		t.Fatal(err)
	}
	if updateResult.Forward.Label != "renamed" || updateResult.Forward.ListenAddress != "127.0.0.1:15433" {
		t.Fatalf("updated = %+v", updateResult.Forward)
	}

	removed := callApp(t, app, "forwards.remove", `{"id":`+quoteJSON(id)+`}`)
	if !removed.OK {
		t.Fatalf("remove failed: %+v", removed)
	}
	missing := callApp(t, app, "forwards.remove", `{"id":`+quoteJSON(id)+`}`)
	if missing.OK || missing.Error == nil || missing.Error.Code != CodeNotFound {
		t.Fatalf("removing twice = %+v", missing)
	}
	if status := callApp(t, app, "forwards.status", `{"id":`+quoteJSON(id)+`}`); status.OK || status.Error == nil || status.Error.Code != CodeNotFound {
		t.Fatalf("status of a removed forward = %+v", status)
	}
	if unknown := callApp(t, app, "forwards.start", `{"id":"nope"}`); unknown.OK || unknown.Error == nil || unknown.Error.Code != CodeNotFound {
		t.Fatalf("starting an unknown forward = %+v", unknown)
	}
}

// ---- live loopback proxy ----

func newTestSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

// startForwardServer runs a minimal SSH server that accepts password auth,
// opens direct-tcpip channels to the destination they name, and keeps a
// session channel alive so the core can start its shell.
func startForwardServer(t *testing.T, password string) (string, uint16) {
	t.Helper()
	config := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, given []byte) (*ssh.Permissions, error) {
			if string(given) == password {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
	}
	config.AddHostKey(newTestSigner(t))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				server, channels, requests, err := ssh.NewServerConn(raw, config)
				if err != nil {
					raw.Close()
					return
				}
				defer server.Close()
				go ssh.DiscardRequests(requests)
				for channel := range channels {
					go serveForwardChannel(channel)
				}
			}()
		}
	}()
	host, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	return host, uint16(port)
}

func serveForwardChannel(channel ssh.NewChannel) {
	switch channel.ChannelType() {
	case "direct-tcpip":
		var target struct {
			Host       string
			Port       uint32
			OriginHost string
			OriginPort uint32
		}
		if err := ssh.Unmarshal(channel.ExtraData(), &target); err != nil {
			channel.Reject(ssh.Prohibited, "bad payload")
			return
		}
		stream, requests, err := channel.Accept()
		if err != nil {
			return
		}
		go ssh.DiscardRequests(requests)
		go func() {
			defer stream.Close()
			remote, err := net.DialTimeout("tcp",
				net.JoinHostPort(target.Host, strconv.Itoa(int(target.Port))), 5*time.Second)
			if err != nil {
				return
			}
			defer remote.Close()
			done := make(chan struct{}, 1)
			go func() {
				io.Copy(remote, stream)
				done <- struct{}{}
			}()
			io.Copy(stream, remote)
			<-done
		}()
	case "session":
		stream, requests, err := channel.Accept()
		if err != nil {
			return
		}
		go func() {
			defer stream.Close()
			started := make(chan struct{}, 1)
			go func() {
				for request := range requests {
					if request.Type == "shell" {
						select {
						case started <- struct{}{}:
						default:
						}
					}
					if request.WantReply {
						request.Reply(true, nil)
					}
				}
			}()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				return
			}
			// A minimal echoing shell: a prompt, then every key back. The
			// command recorder needs both to trust what it reads.
			stream.Write([]byte("$ "))
			buffer := make([]byte, 512)
			for {
				count, readErr := stream.Read(buffer)
				if count > 0 {
					stream.Write(buffer[:count])
					for _, b := range buffer[:count] {
						if b == '\r' {
							stream.Write([]byte("\r\n$ "))
						}
					}
				}
				if readErr != nil {
					return
				}
			}
		}()
	default:
		channel.Reject(ssh.UnknownChannelType, "unsupported")
	}
}

// freePort reserves and releases a loopback port, then returns it.
func freePort(t *testing.T) uint16 {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	return uint16(port)
}

// openConnectedSession opens a session for hostID and answers its prompts
// until the shell is up.
func openConnectedSession(t *testing.T, app *App, hostID string) uint32 {
	t.Helper()
	opened := callApp(t, app, "sessions.open", `{"hostId":`+quoteJSON(hostID)+`,"cols":80,"rows":24}`)
	if !opened.OK {
		t.Fatalf("sessions.open failed: %+v", opened.Error)
	}
	var info SessionInfo
	if err := json.Unmarshal(opened.Result, &info); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		env := callApp(t, app, "sessions.list", "")
		var listed struct {
			Sessions []SessionInfo `json:"sessions"`
		}
		if err := json.Unmarshal(env.Result, &listed); err != nil {
			t.Fatal(err)
		}
		for _, current := range listed.Sessions {
			if current.ID != info.ID {
				continue
			}
			switch current.Phase {
			case "connected":
				return info.ID
			case "hostkey":
				callApp(t, app, "sessions.answer",
					`{"id":`+strconv.Itoa(int(info.ID))+`,"kind":"hostkey","save":true}`)
			case "password":
				callApp(t, app, "sessions.answer",
					`{"id":`+strconv.Itoa(int(info.ID))+`,"kind":"password","value":"secret"}`)
			case "failed", "closed":
				t.Fatalf("session ended before connecting: %+v", current)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the session never connected")
	return 0
}

func waitForwardState(t *testing.T, app *App, id, want string) forwardState {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last forwardState
	for time.Now().Before(deadline) {
		env := callApp(t, app, "forwards.status", `{"id":`+quoteJSON(id)+`}`)
		if !env.OK {
			t.Fatalf("forwards.status failed: %+v", env.Error)
		}
		var result struct {
			Forward forwardState `json:"forward"`
		}
		if err := json.Unmarshal(env.Result, &result); err != nil {
			t.Fatal(err)
		}
		last = result.Forward
		if last.State == want && want != "running" {
			return last
		}
		if last.State == want && last.Active >= 0 {
			return last
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("forward %s never reached %q: %+v", id, want, last)
	return last
}

func echoServer(t *testing.T) (string, uint16) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				io.Copy(conn, conn)
			}()
		}
	}()
	host, portText, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.ParseUint(portText, 10, 16)
	return host, uint16(port)
}

func TestForwardProxiesLoopbackThroughSSH(t *testing.T) {
	serverHost, serverPort := startForwardServer(t, "secret")
	echoHost, echoPort := echoServer(t)

	app, err := Start(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	saved := callApp(t, app, "hosts.save", `{"label":"test","address":`+quoteJSON(serverHost)+
		`,"user":"me","port":`+strconv.Itoa(int(serverPort))+`,"auth":"password"}`)
	if !saved.OK {
		t.Fatalf("hosts.save failed: %+v", saved.Error)
	}
	var host Host
	if err := json.Unmarshal(saved.Result, &host); err != nil {
		t.Fatal(err)
	}

	localPort := freePort(t)
	destination := net.JoinHostPort(echoHost, strconv.Itoa(int(echoPort)))
	added := callApp(t, app, "forwards.add", `{"label":"echo","hostId":`+quoteJSON(host.ID)+
		`,"mode":"local","localPort":`+strconv.Itoa(int(localPort))+
		`,"destination":`+quoteJSON(destination)+`,"autoStart":true}`)
	if !added.OK {
		t.Fatalf("forwards.add failed: %+v", added.Error)
	}
	var addedResult struct {
		Forward forwardState `json:"forward"`
	}
	if err := json.Unmarshal(added.Result, &addedResult); err != nil {
		t.Fatal(err)
	}

	// The tunnel starts on its own once the host connects.
	sessionID := openConnectedSession(t, app, host.ID)
	running := waitForwardState(t, app, addedResult.Forward.ID, "running")
	if running.SessionID != sessionID || running.ListenAddress != "127.0.0.1:"+strconv.Itoa(int(localPort)) {
		t.Fatalf("auto-started forward = %+v", running)
	}

	conn, err := net.DialTimeout("tcp", running.ListenAddress, 5*time.Second)
	if err != nil {
		t.Fatalf("dialing the forward: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("ping\n")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 16)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	count, err := conn.Read(buffer)
	if err != nil {
		t.Fatalf("reading the echo: %v", err)
	}
	if string(buffer[:count]) != "ping\n" {
		t.Fatalf("echo = %q", buffer[:count])
	}
	conn.Close()

	deadline := time.Now().Add(10 * time.Second)
	for {
		env := callApp(t, app, "forwards.status", `{"id":`+quoteJSON(addedResult.Forward.ID)+`}`)
		var result struct {
			Forward forwardState `json:"forward"`
		}
		if err := json.Unmarshal(env.Result, &result); err != nil {
			t.Fatal(err)
		}
		if result.Forward.Accepted >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the forward never counted its stream: %+v", result.Forward)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Stopping closes the listener; a second dial must fail.
	stopped := callApp(t, app, "forwards.stop", `{"id":`+quoteJSON(addedResult.Forward.ID)+`}`)
	if !stopped.OK {
		t.Fatalf("forwards.stop failed: %+v", stopped.Error)
	}
	if _, err := net.DialTimeout("tcp", running.ListenAddress, 300*time.Millisecond); err == nil {
		t.Fatal("the listener still accepts after stop")
	}
}

func TestForwardSessionEndStopsTunnel(t *testing.T) {
	serverHost, serverPort := startForwardServer(t, "secret")
	app, err := Start(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	saved := callApp(t, app, "hosts.save", `{"label":"test","address":`+quoteJSON(serverHost)+
		`,"user":"me","port":`+strconv.Itoa(int(serverPort))+`,"auth":"password"}`)
	var host Host
	if err := json.Unmarshal(saved.Result, &host); err != nil {
		t.Fatal(err)
	}

	localPort := freePort(t)
	added := callApp(t, app, "forwards.add", `{"hostId":`+quoteJSON(host.ID)+
		`,"mode":"socks5","localPort":`+strconv.Itoa(int(localPort))+`,"autoStart":true}`)
	var addedResult struct {
		Forward forwardState `json:"forward"`
	}
	if err := json.Unmarshal(added.Result, &addedResult); err != nil {
		t.Fatal(err)
	}

	openConnectedSession(t, app, host.ID)
	running := waitForwardState(t, app, addedResult.Forward.ID, "running")
	if running.Destination != "" || running.Mode != "socks5" {
		t.Fatalf("socks5 forward = %+v", running)
	}

	closed := callApp(t, app, "sessions.close", `{"id":`+strconv.Itoa(int(running.SessionID))+`}`)
	if !closed.OK {
		t.Fatalf("sessions.close failed: %+v", closed.Error)
	}
	waitForwardState(t, app, addedResult.Forward.ID, "stopped")
	if _, err := net.DialTimeout("tcp", running.ListenAddress, 300*time.Millisecond); err == nil {
		t.Fatal("the socks listener survived its session")
	}
}
