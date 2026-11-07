package mobilecore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

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
		done:    make(chan struct{}),
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
		done:    make(chan struct{}),
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
