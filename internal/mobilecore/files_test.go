package mobilecore

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"motty/internal/sftpclient"
	"motty/internal/sshconn"
)

func quoteJSON(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func TestFilesLocalPathStaysInDataDir(t *testing.T) {
	dir := t.TempDir()
	registry := newFilesRegistry(dir)

	inside := filepath.Join(dir, "downloads", "report.txt")
	got, err := registry.localPath(inside)
	if err != nil {
		t.Fatalf("inside path rejected: %v", err)
	}
	if got != filepath.Clean(inside) {
		t.Fatalf("localPath = %q, want %q", got, filepath.Clean(inside))
	}
	if _, err := registry.localPath(dir); err != nil {
		t.Fatalf("the root itself must be allowed: %v", err)
	}

	for _, bad := range []string{
		"",
		"/etc/passwd",
		filepath.Join(dir, "..", "escape"),
		"../escape",
		dir + "-suffix/file",
	} {
		if _, err := registry.localPath(bad); err == nil {
			t.Fatalf("localPath(%q) was accepted", bad)
		}
	}
}

func TestExistingPolicyNames(t *testing.T) {
	cases := map[string]sftpclient.ExistingPolicy{
		"":        sftpclient.ExistingReplace,
		"replace": sftpclient.ExistingReplace,
		"fail":    sftpclient.ExistingFail,
		"resume":  sftpclient.ExistingResume,
		"skip":    sftpclient.ExistingSkip,
	}
	for name, want := range cases {
		got, err := existingPolicy(name)
		if err != nil {
			t.Fatalf("existingPolicy(%q): %v", name, err)
		}
		if got != want {
			t.Fatalf("existingPolicy(%q) = %v, want %v", name, got, want)
		}
	}
	if _, err := existingPolicy("overwrite"); err == nil {
		t.Fatal("an unknown policy was accepted")
	}
}

func TestExportTransferShapesJSON(t *testing.T) {
	queued := time.Unix(1_700_000_000, 0)
	snapshot := sftpclient.TransferSnapshot{
		ID:             7,
		Operation:      sftpclient.OperationDownload,
		Source:         "/var/log/syslog",
		Destination:    "/tmp/syslog",
		Recursive:      false,
		State:          sftpclient.TransferRunning,
		FilesDone:      1,
		FilesTotal:     1,
		BytesDone:      512,
		BytesTotal:     1024,
		BytesPerSecond: 256,
		Resumed:        128,
		Retryable:      true,
		QueuedAt:       queued,
	}
	got := exportTransfer(snapshot)
	if got.Operation != "download" || got.State != "running" {
		t.Fatalf("unexpected names: %+v", got)
	}
	if got.ID != 7 || got.BytesDone != 512 || got.Resumed != 128 || !got.Retryable {
		t.Fatalf("fields were not copied: %+v", got)
	}
	if got.QueuedAt != queued.Unix() || got.StartedAt != 0 || got.FinishedAt != 0 {
		t.Fatalf("zero timestamps must stay zero: %+v", got)
	}
}

func TestFileEntryKeepsModeBits(t *testing.T) {
	when := time.Unix(1_700_000_000, 0)
	entries := fileEntries([]sftpclient.Entry{
		{Name: "logs", Size: 4096, Mode: os.ModeDir | 0o755, ModTime: when, Owner: "root", Group: "wheel"},
		{Name: "link", Mode: os.ModeSymlink | 0o777, LinkTarget: "/srv", LinkDir: true},
	})
	if !entries[0].IsDir || entries[0].Owner != "root" || entries[0].ModTime != when.Unix() {
		t.Fatalf("directory entry = %+v", entries[0])
	}
	if entries[0].Mode&uint32(os.ModeDir) == 0 {
		t.Fatalf("mode lost the directory bit: %o", entries[0].Mode)
	}
	if !entries[1].IsLink || !entries[1].LinkDir || entries[1].LinkTarget != "/srv" {
		t.Fatalf("link entry = %+v", entries[1])
	}
}

func TestFilesRejectsUnknownOrDisconnectedSessions(t *testing.T) {
	app, err := Start(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	unknown := callApp(t, app, "files.list", `{"sessionId":42,"path":"."}`)
	if unknown.OK || unknown.Error == nil || unknown.Error.Code != CodeNotFound {
		t.Fatalf("unknown session = %+v", unknown)
	}

	s := &session{
		id:      3,
		manager: app.sessions,
		phase:   sshconn.PhaseFailed,
		run:     newSessionRun(),
		wake:    make(chan struct{}, 1),
	}
	app.sessions.mu.Lock()
	app.sessions.sessions[3] = s
	app.sessions.mu.Unlock()

	disconnected := callApp(t, app, "files.list", `{"sessionId":3,"path":"."}`)
	if disconnected.OK || disconnected.Error == nil || disconnected.Error.Code != CodeUnavailable {
		t.Fatalf("disconnected session = %+v", disconnected)
	}

	local := filepath.Join(app.cfg.DataDir, "downloads", "hosts")
	download := callApp(t, app, "files.download",
		`{"sessionId":3,"remote":"/etc/hosts","local":`+quoteJSON(local)+`}`)
	if download.OK || download.Error == nil || download.Error.Code != CodeUnavailable {
		t.Fatalf("download on a disconnected session = %+v", download)
	}
}

func TestFilesDownloadRejectsOutsideLocalPaths(t *testing.T) {
	app, err := Start(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	s := &session{
		id:      4,
		manager: app.sessions,
		phase:   sshconn.PhaseConnected,
		run:     newSessionRun(),
		wake:    make(chan struct{}, 1),
	}
	app.sessions.mu.Lock()
	app.sessions.sessions[4] = s
	app.sessions.mu.Unlock()

	result := callApp(t, app, "files.download",
		`{"sessionId":4,"remote":"/etc/hosts","local":"/etc/evil"}`)
	if result.OK || result.Error == nil || result.Error.Code != CodeBadRequest {
		t.Fatalf("outside path = %+v", result)
	}
}

func TestTextDocumentErrorsUseFrontendCodes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code string
	}{
		{"binary", sftpclient.ErrUnsupportedEncoding, CodeNotText},
		{"large", sftpclient.ErrEditFileTooLarge, CodeTooLarge},
		{"scp", sftpclient.ErrSCPNoEdit, CodeUnsupported},
		{"conflict", sftpclient.ErrRemoteChanged, CodeConflict},
		{"other", errors.New("disk on fire"), CodeUnavailable},
	}
	for _, test := range cases {
		got, ok := textDocumentError("cannot save the file", test.err).(*RPCError)
		if !ok {
			t.Fatalf("%s: not an RPC error", test.name)
		}
		if got.Code != test.code {
			t.Fatalf("%s: code = %q, want %q (%s)", test.name, got.Code, test.code, got.Message)
		}
	}
}

func TestExportTextDocumentKeepsRevisionAsHex(t *testing.T) {
	var revision sftpclient.Revision
	revision[0], revision[len(revision)-1] = 0xab, 0xcd
	got := exportTextDocument(sftpclient.TextDocument{
		Text:     "hello",
		Revision: revision,
		UTF8BOM:  true,
	})
	if got.Text != "hello" || !got.BOM || got.Size != 5 {
		t.Fatalf("document = %+v", got)
	}
	want := "ab" + strings.Repeat("00", len(revision)-2) + "cd"
	if got.Revision != want {
		t.Fatalf("revision = %q, want %q", got.Revision, want)
	}
}

func TestFilesTextRPCValidatesInput(t *testing.T) {
	app, err := Start(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	missingPath := callApp(t, app, "files.openText", `{"sessionId":42,"path":""}`)
	if missingPath.OK || missingPath.Error == nil || missingPath.Error.Code != CodeBadRequest {
		t.Fatalf("empty path = %+v", missingPath)
	}

	unknown := callApp(t, app, "files.openText", `{"sessionId":42,"path":"/etc/hosts"}`)
	if unknown.OK || unknown.Error == nil || unknown.Error.Code != CodeNotFound {
		t.Fatalf("unknown session = %+v", unknown)
	}

	missingCreatePath := callApp(t, app, "files.create", `{"sessionId":42,"path":""}`)
	if missingCreatePath.OK || missingCreatePath.Error == nil || missingCreatePath.Error.Code != CodeBadRequest {
		t.Fatalf("empty create path = %+v", missingCreatePath)
	}

	unknownCreate := callApp(t, app, "files.create", `{"sessionId":42,"path":"new.txt"}`)
	if unknownCreate.OK || unknownCreate.Error == nil || unknownCreate.Error.Code != CodeNotFound {
		t.Fatalf("create on an unknown session = %+v", unknownCreate)
	}

	badRevision := callApp(t, app, "files.saveText",
		`{"sessionId":42,"path":"/etc/hosts","text":"x","revision":"nope"}`)
	if badRevision.OK || badRevision.Error == nil || badRevision.Error.Code != CodeBadRequest {
		t.Fatalf("bad revision = %+v", badRevision)
	}

	validRevision := callApp(t, app, "files.saveText",
		`{"sessionId":42,"path":"/etc/hosts","text":"x","revision":"`+strings.Repeat("00", 32)+`"}`)
	if validRevision.OK || validRevision.Error == nil || validRevision.Error.Code != CodeNotFound {
		t.Fatalf("valid revision should reach session lookup = %+v", validRevision)
	}
}

func TestFilesTextRoundTripAndGuards(t *testing.T) {
	address, port, root := startFilesServer(t)
	notes := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(notes, []byte("hello\n世界\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	app, err := Start(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	saved := callApp(t, app, "hosts.save", `{"label":"files","address":`+quoteJSON(address)+
		`,"user":"me","port":`+strconv.Itoa(int(port))+`,"auth":"password"}`)
	if !saved.OK {
		t.Fatalf("hosts.save failed: %+v", saved.Error)
	}
	var host Host
	if err := json.Unmarshal(saved.Result, &host); err != nil {
		t.Fatal(err)
	}
	sessionID := openConnectedSession(t, app, host.ID)

	opened := callApp(t, app, "files.openText",
		`{"sessionId":`+strconv.Itoa(int(sessionID))+`,"path":"notes.txt"}`)
	if !opened.OK {
		t.Fatalf("files.openText failed: %+v", opened.Error)
	}
	var document remoteTextDocument
	if err := json.Unmarshal(opened.Result, &document); err != nil {
		t.Fatal(err)
	}
	if document.Text != "hello\n世界\n" || document.Size != len(document.Text) || document.BOM {
		t.Fatalf("opened document = %+v", document)
	}
	if len(document.Revision) != 64 {
		t.Fatalf("revision = %q", document.Revision)
	}

	changed := "changed on iOS\nsecond line\n"
	savedText := callApp(t, app, "files.saveText",
		`{"sessionId":`+strconv.Itoa(int(sessionID))+`,"path":"notes.txt","text":`+
			quoteJSON(changed)+`,"revision":`+quoteJSON(document.Revision)+`}`)
	if !savedText.OK {
		t.Fatalf("files.saveText failed: %+v", savedText.Error)
	}
	var savedDocument remoteTextDocument
	if err := json.Unmarshal(savedText.Result, &savedDocument); err != nil {
		t.Fatal(err)
	}
	if savedDocument.Text != changed || savedDocument.Revision == document.Revision {
		t.Fatalf("saved document = %+v", savedDocument)
	}
	content, err := os.ReadFile(notes)
	if err != nil || string(content) != changed {
		t.Fatalf("remote content = %q, %v", content, err)
	}
	if info, err := os.Stat(notes); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("saved mode = %v, %v", info, err)
	}

	// Saving with the revision from before the first write must refuse to
	// overwrite the newer content.
	conflict := callApp(t, app, "files.saveText",
		`{"sessionId":`+strconv.Itoa(int(sessionID))+`,"path":"notes.txt","text":"stale","revision":`+
			quoteJSON(document.Revision)+`}`)
	if conflict.OK || conflict.Error == nil || conflict.Error.Code != CodeConflict {
		t.Fatalf("stale save = %+v", conflict)
	}
	if content, _ := os.ReadFile(notes); string(content) != changed {
		t.Fatalf("a rejected save changed the file: %q", content)
	}

	created := callApp(t, app, "files.create",
		`{"sessionId":`+strconv.Itoa(int(sessionID))+`,"path":"created.txt"}`)
	if !created.OK {
		t.Fatalf("files.create failed: %+v", created.Error)
	}
	if content, err := os.ReadFile(filepath.Join(root, "created.txt")); err != nil || len(content) != 0 {
		t.Fatalf("created file = %q, %v", content, err)
	}
	duplicate := callApp(t, app, "files.create",
		`{"sessionId":`+strconv.Itoa(int(sessionID))+`,"path":"created.txt"}`)
	if duplicate.OK || duplicate.Error == nil || duplicate.Error.Code != CodeConflict {
		t.Fatalf("duplicate create = %+v", duplicate)
	}
	// The new empty document must open and save through the same revision
	// path as an existing file.
	emptyOpened := callApp(t, app, "files.openText",
		`{"sessionId":`+strconv.Itoa(int(sessionID))+`,"path":"created.txt"}`)
	if !emptyOpened.OK {
		t.Fatalf("open created file failed: %+v", emptyOpened.Error)
	}
	var emptyDocument remoteTextDocument
	if err := json.Unmarshal(emptyOpened.Result, &emptyDocument); err != nil {
		t.Fatal(err)
	}
	if emptyDocument.Text != "" || emptyDocument.Size != 0 || len(emptyDocument.Revision) != 64 {
		t.Fatalf("created document = %+v", emptyDocument)
	}
	written := callApp(t, app, "files.saveText",
		`{"sessionId":`+strconv.Itoa(int(sessionID))+`,"path":"created.txt","text":"first draft\n","revision":`+
			quoteJSON(emptyDocument.Revision)+`}`)
	if !written.OK {
		t.Fatalf("save created file failed: %+v", written.Error)
	}

	if err := os.WriteFile(filepath.Join(root, "binary.bin"), []byte{0, 1, 2}, 0o600); err != nil {
		t.Fatal(err)
	}
	binary := callApp(t, app, "files.openText",
		`{"sessionId":`+strconv.Itoa(int(sessionID))+`,"path":"binary.bin"}`)
	if binary.OK || binary.Error == nil || binary.Error.Code != CodeNotText {
		t.Fatalf("binary file = %+v", binary)
	}

	if err := os.WriteFile(filepath.Join(root, "large.txt"), make([]byte, sftpclient.MaxEditFileBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	large := callApp(t, app, "files.openText",
		`{"sessionId":`+strconv.Itoa(int(sessionID))+`,"path":"large.txt"}`)
	if large.OK || large.Error == nil || large.Error.Code != CodeTooLarge {
		t.Fatalf("large file = %+v", large)
	}
}

// startFilesServer is a minimal SSH server with a shell and a real SFTP
// subsystem rooted in a temporary directory. It lets the file-editor RPC be
// exercised through the same protocol path a phone uses.
func startFilesServer(t *testing.T) (string, uint16, string) {
	t.Helper()
	root := t.TempDir()
	config := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, given []byte) (*ssh.Permissions, error) {
			if string(given) == "secret" {
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
					if channel.ChannelType() != "session" {
						channel.Reject(ssh.UnknownChannelType, "session only")
						continue
					}
					stream, channelRequests, err := channel.Accept()
					if err != nil {
						continue
					}
					go serveFilesSession(stream, channelRequests, root)
				}
			}()
		}
	}()
	address, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	return address, uint16(port), root
}

func serveFilesSession(stream ssh.Channel, requests <-chan *ssh.Request, root string) {
	defer stream.Close()
	shellStarted := false
	for request := range requests {
		switch request.Type {
		case "subsystem":
			var payload struct{ Name string }
			if err := ssh.Unmarshal(request.Payload, &payload); err != nil || payload.Name != "sftp" {
				request.Reply(false, nil)
				continue
			}
			request.Reply(true, nil)
			server, err := sftp.NewServer(stream, sftp.WithServerWorkingDirectory(root))
			if err != nil {
				return
			}
			_ = server.Serve()
			_ = server.Close()
			return
		case "shell":
			request.Reply(true, nil)
			if !shellStarted {
				shellStarted = true
				go echoTestShell(stream)
			}
		default:
			request.Reply(true, nil)
		}
	}
}

func echoTestShell(stream ssh.Channel) {
	stream.Write([]byte("$ "))
	buffer := make([]byte, 512)
	for {
		count, err := stream.Read(buffer)
		if count > 0 {
			stream.Write(buffer[:count])
			for _, b := range buffer[:count] {
				if b == '\r' {
					stream.Write([]byte("\r\n$ "))
				}
			}
		}
		if err != nil {
			return
		}
	}
}
