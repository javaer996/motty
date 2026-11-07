package mobilecore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"motty/internal/sftpclient"
	"motty/internal/sshconn"
)

// filesTimeout bounds one directory listing or one small file operation. A
// transfer is queued instead of waited for, so slow links never hold the
// control plane for long.
const filesTimeout = 30 * time.Second

// filesHistoryLimit is how many transfer rows the front end receives. The
// worker keeps more; a phone only ever shows the tail.
const filesHistoryLimit = 100

// filesSession owns the SFTP worker of one SSH session. The worker is created
// lazily on first use because most sessions never open a file browser.
type filesSession struct {
	session *session
	worker  *sftpclient.Worker

	// opMu serializes the blocking operations: the worker reports them on a
	// shared channel, so two callers waiting at once could take each other's
	// result.
	opMu sync.Mutex

	mu       sync.Mutex
	lists    map[uint64]chan sftpclient.Result
	nextList uint64
	ops      chan sftpclient.Result
	closed   bool
	stopOnce sync.Once
}

func newFilesSession(s *session) *filesSession {
	files := &filesSession{
		session: s,
		lists:   map[uint64]chan sftpclient.Result{},
		ops:     make(chan sftpclient.Result, 8),
	}
	files.worker = sftpclient.NewWorker(files.openRemote, func() {})
	files.worker.SetParallel(2)
	go files.pump()
	return files
}

// openRemote dials the SFTP subsystem over the session's authenticated SSH
// connection, falling back to SCP when the server refuses the subsystem.
func (f *filesSession) openRemote() (sftpclient.Remote, error) {
	f.session.mu.Lock()
	conn := f.session.conn
	phase := f.session.phase
	f.session.mu.Unlock()
	if conn == nil || phase != sshconn.PhaseConnected {
		return nil, errorf(CodeUnavailable, "the session is not connected")
	}
	client := conn.Client()
	if client == nil {
		return nil, errorf(CodeUnavailable, "the SSH connection is not ready")
	}
	remote, err := sftpclient.Open(client)
	if err != nil && sftpclient.IsSubsystemRefused(err) {
		return sftpclient.OpenSCP(client)
	}
	return remote, err
}

// pump is the only reader of the worker's result channel. It routes listings
// to their waiter, drops transfer progress (the front end polls the worker)
// and hands small operations to the current caller.
func (f *filesSession) pump() {
	for {
		select {
		case <-f.worker.Done():
			f.stop()
			return
		case result, ok := <-f.worker.Results():
			if !ok {
				f.stop()
				return
			}
			f.dispatch(result)
		}
	}
}

func (f *filesSession) dispatch(result sftpclient.Result) {
	if result.ListID != 0 {
		f.mu.Lock()
		waiter := f.lists[result.ListID]
		if waiter != nil && !result.FollowUp {
			delete(f.lists, result.ListID)
		}
		f.mu.Unlock()
		if waiter != nil {
			select {
			case waiter <- result:
			default:
			}
		}
		return
	}
	if result.TransferID != 0 || result.Operation == sftpclient.OperationList {
		return
	}
	f.mu.Lock()
	if !f.closed {
		select {
		case f.ops <- result:
		default:
		}
	}
	f.mu.Unlock()
}

func (f *filesSession) stop() {
	f.stopOnce.Do(func() {
		f.mu.Lock()
		f.closed = true
		for id, waiter := range f.lists {
			delete(f.lists, id)
			select {
			case waiter <- sftpclient.Result{Err: sftpclient.ErrCanceled}:
			default:
			}
		}
		f.mu.Unlock()
	})
}

// Close releases the worker and every pending wait.
func (f *filesSession) Close() {
	f.stop()
	f.worker.Close()
}

func (f *filesSession) list(remotePath string) ([]fileEntry, error) {
	if strings.TrimSpace(remotePath) == "" {
		remotePath = "."
	}
	waiter := make(chan sftpclient.Result, 1)
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return nil, errorf(CodeUnavailable, "the file session is closed")
	}
	f.nextList++
	id := f.nextList
	f.lists[id] = waiter
	f.mu.Unlock()

	if !f.worker.ListWithID(remotePath, id) {
		f.mu.Lock()
		delete(f.lists, id)
		f.mu.Unlock()
		return nil, errorf(CodeUnavailable, "the file session is not accepting requests")
	}
	select {
	case result := <-waiter:
		if result.Err != nil {
			return nil, errorf(CodeUnavailable, "cannot list %s: %v", remotePath, result.Err)
		}
		return fileEntries(result.Entries), nil
	case <-time.After(filesTimeout):
		f.mu.Lock()
		delete(f.lists, id)
		f.mu.Unlock()
		return nil, errorf(CodeUnavailable, "listing %s timed out", remotePath)
	}
}

// simple waits for one non-transfer operation. Callers are serialized; a late
// result from a timed-out request is dropped before submitting the next one.
func (f *filesSession) simple(description string, submit func() bool) error {
	f.opMu.Lock()
	defer f.opMu.Unlock()
	select {
	case <-f.ops:
	default:
	}
	f.mu.Lock()
	closed := f.closed
	f.mu.Unlock()
	if closed {
		return errorf(CodeUnavailable, "the file session is closed")
	}
	if !submit() {
		return errorf(CodeUnavailable, "the file session is not accepting requests")
	}
	select {
	case result := <-f.ops:
		if result.Err != nil {
			return errorf(CodeUnavailable, "%s: %v", description, result.Err)
		}
		return nil
	case <-time.After(filesTimeout):
		return errorf(CodeUnavailable, "%s timed out", description)
	}
}

func (f *filesSession) transfers() []fileTransfer {
	snapshots := f.worker.Transfers(nil)
	if len(snapshots) > filesHistoryLimit {
		snapshots = snapshots[len(snapshots)-filesHistoryLimit:]
	}
	out := make([]fileTransfer, len(snapshots))
	for index, snapshot := range snapshots {
		out[index] = exportTransfer(snapshot)
	}
	return out
}

// filesRegistry keeps one worker per live session and owns the local-path
// allowlist. Everything the front end gives us must stay inside the app data
// directory: external files are copied in by Swift first.
type filesRegistry struct {
	dataDir string

	mu       sync.Mutex
	sessions map[uint32]*filesSession
}

func newFilesRegistry(dataDir string) *filesRegistry {
	return &filesRegistry{
		dataDir:  filepath.Clean(dataDir),
		sessions: map[uint32]*filesSession{},
	}
}

func (r *filesRegistry) forSession(s *session) (*filesSession, error) {
	s.mu.Lock()
	connected := s.phase == sshconn.PhaseConnected && s.conn != nil
	s.mu.Unlock()
	if !connected {
		return nil, errorf(CodeUnavailable, "the session is not connected")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if files := r.sessions[s.id]; files != nil {
		return files, nil
	}
	files := newFilesSession(s)
	r.sessions[s.id] = files
	return files, nil
}

func (r *filesRegistry) close(sessionID uint32) {
	r.mu.Lock()
	files := r.sessions[sessionID]
	delete(r.sessions, sessionID)
	r.mu.Unlock()
	if files != nil {
		files.Close()
	}
}

func (r *filesRegistry) closeAll() {
	r.mu.Lock()
	all := r.sessions
	r.sessions = map[uint32]*filesSession{}
	r.mu.Unlock()
	for _, files := range all {
		files.Close()
	}
}

func (r *filesRegistry) localPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errorf(CodeBadRequest, "a local path is required")
	}
	clean := filepath.Clean(path)
	if clean != r.dataDir && !strings.HasPrefix(clean, r.dataDir+string(os.PathSeparator)) {
		return "", errorf(CodeBadRequest, "local paths must stay inside the app data directory")
	}
	return clean, nil
}

// fileEntry is the front end's view of one remote directory item.
type fileEntry struct {
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	Mode       uint32 `json:"mode"`
	ModTime    int64  `json:"modTime,omitempty"`
	IsDir      bool   `json:"isDir"`
	IsLink     bool   `json:"isLink"`
	LinkTarget string `json:"linkTarget,omitempty"`
	LinkDir    bool   `json:"linkDir,omitempty"`
	LinkBroken bool   `json:"linkBroken,omitempty"`
	Owner      string `json:"owner,omitempty"`
	Group      string `json:"group,omitempty"`
}

func fileEntries(entries []sftpclient.Entry) []fileEntry {
	out := make([]fileEntry, len(entries))
	for index, entry := range entries {
		out[index] = fileEntry{
			Name:       entry.Name,
			Size:       entry.Size,
			Mode:       uint32(entry.Mode),
			ModTime:    entry.ModTime.Unix(),
			IsDir:      entry.IsDir(),
			IsLink:     entry.IsLink(),
			LinkTarget: entry.LinkTarget,
			LinkDir:    entry.LinkDir,
			LinkBroken: entry.LinkBroken,
			Owner:      entry.Owner,
			Group:      entry.Group,
		}
	}
	return out
}

// fileTransfer is the stable JSON shape of a transfer; the worker's own enum
// values are internal and must not leak into the Swift models.
type fileTransfer struct {
	ID             uint64 `json:"id"`
	Operation      string `json:"operation"`
	Source         string `json:"source"`
	Destination    string `json:"destination"`
	Recursive      bool   `json:"recursive"`
	State          string `json:"state"`
	FilesDone      int    `json:"filesDone"`
	FilesTotal     int    `json:"filesTotal"`
	BytesDone      int64  `json:"bytesDone"`
	BytesTotal     int64  `json:"bytesTotal"`
	BytesPerSecond int64  `json:"bytesPerSecond"`
	Resumed        int64  `json:"resumed"`
	Error          string `json:"error,omitempty"`
	Retryable      bool   `json:"retryable"`
	QueuedAt       int64  `json:"queuedAt,omitempty"`
	StartedAt      int64  `json:"startedAt,omitempty"`
	FinishedAt     int64  `json:"finishedAt,omitempty"`
}

func exportTransfer(snapshot sftpclient.TransferSnapshot) fileTransfer {
	out := fileTransfer{
		ID:             snapshot.ID,
		Operation:      operationName(snapshot.Operation),
		Source:         snapshot.Source,
		Destination:    snapshot.Destination,
		Recursive:      snapshot.Recursive,
		State:          transferStateName(snapshot.State),
		FilesDone:      snapshot.FilesDone,
		FilesTotal:     snapshot.FilesTotal,
		BytesDone:      snapshot.BytesDone,
		BytesTotal:     snapshot.BytesTotal,
		BytesPerSecond: snapshot.BytesPerSecond,
		Resumed:        snapshot.Resumed,
		Error:          snapshot.Error,
		Retryable:      snapshot.Retryable,
	}
	if !snapshot.QueuedAt.IsZero() {
		out.QueuedAt = snapshot.QueuedAt.Unix()
	}
	if !snapshot.StartedAt.IsZero() {
		out.StartedAt = snapshot.StartedAt.Unix()
	}
	if !snapshot.FinishedAt.IsZero() {
		out.FinishedAt = snapshot.FinishedAt.Unix()
	}
	return out
}

func operationName(operation sftpclient.Operation) string {
	switch operation {
	case sftpclient.OperationDownload:
		return "download"
	case sftpclient.OperationUpload:
		return "upload"
	case sftpclient.OperationCreateDirectory:
		return "mkdir"
	case sftpclient.OperationRename:
		return "rename"
	case sftpclient.OperationRemoveFile:
		return "removeFile"
	case sftpclient.OperationRemoveDirectory:
		return "removeDirectory"
	case sftpclient.OperationChmod:
		return "chmod"
	case sftpclient.OperationSymlink:
		return "symlink"
	default:
		return "other"
	}
}

func transferStateName(state sftpclient.TransferState) string {
	switch state {
	case sftpclient.TransferQueued:
		return "queued"
	case sftpclient.TransferRunning:
		return "running"
	case sftpclient.TransferCompleted:
		return "completed"
	case sftpclient.TransferFailed:
		return "failed"
	case sftpclient.TransferCanceled:
		return "canceled"
	case sftpclient.TransferPaused:
		return "paused"
	default:
		return "unknown"
	}
}

func existingPolicy(name string) (sftpclient.ExistingPolicy, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "replace":
		return sftpclient.ExistingReplace, nil
	case "fail":
		return sftpclient.ExistingFail, nil
	case "resume":
		return sftpclient.ExistingResume, nil
	case "skip":
		return sftpclient.ExistingSkip, nil
	default:
		return sftpclient.ExistingReplace, errorf(CodeBadRequest, "unknown overwrite policy %q", name)
	}
}

// callFiles handles the files.* namespace. It reports handled=false for every
// other method so app.call can keep its single switch readable.
func (a *App) callFiles(method string, params json.RawMessage) (bool, any, error) {
	switch method {
	case "files.list":
		var request struct {
			SessionID uint32 `json:"sessionId"`
			Path      string `json:"path"`
		}
		if err := decodeParams(params, &request); err != nil {
			return true, nil, err
		}
		files, err := a.filesFor(request.SessionID)
		if err != nil {
			return true, nil, err
		}
		entries, err := files.list(request.Path)
		if err != nil {
			return true, nil, err
		}
		path := request.Path
		if strings.TrimSpace(path) == "" {
			path = "."
		}
		return true, map[string]any{"path": path, "entries": entries}, nil
	case "files.download":
		var request struct {
			SessionID uint32 `json:"sessionId"`
			Remote    string `json:"remote"`
			Local     string `json:"local"`
			Recursive bool   `json:"recursive"`
			Policy    string `json:"policy"`
		}
		if err := decodeParams(params, &request); err != nil {
			return true, nil, err
		}
		if strings.TrimSpace(request.Remote) == "" {
			return true, nil, errorf(CodeBadRequest, "a remote path is required")
		}
		local, err := a.files.localPath(request.Local)
		if err != nil {
			return true, nil, err
		}
		policy, err := existingPolicy(request.Policy)
		if err != nil {
			return true, nil, err
		}
		files, err := a.filesFor(request.SessionID)
		if err != nil {
			return true, nil, err
		}
		id, queued := files.worker.QueueDownload(request.Remote, local, request.Recursive, policy)
		if !queued {
			return true, nil, errorf(CodeUnavailable, "the file session is not accepting transfers")
		}
		return true, map[string]any{"transferId": id}, nil
	case "files.upload":
		var request struct {
			SessionID uint32 `json:"sessionId"`
			Local     string `json:"local"`
			Remote    string `json:"remote"`
			Recursive bool   `json:"recursive"`
			Policy    string `json:"policy"`
		}
		if err := decodeParams(params, &request); err != nil {
			return true, nil, err
		}
		if strings.TrimSpace(request.Remote) == "" {
			return true, nil, errorf(CodeBadRequest, "a remote path is required")
		}
		local, err := a.files.localPath(request.Local)
		if err != nil {
			return true, nil, err
		}
		policy, err := existingPolicy(request.Policy)
		if err != nil {
			return true, nil, err
		}
		files, err := a.filesFor(request.SessionID)
		if err != nil {
			return true, nil, err
		}
		id, queued := files.worker.QueueUpload(local, request.Remote, request.Recursive, policy)
		if !queued {
			return true, nil, errorf(CodeUnavailable, "the file session is not accepting transfers")
		}
		return true, map[string]any{"transferId": id}, nil
	case "files.mkdir":
		var request struct {
			SessionID uint32 `json:"sessionId"`
			Path      string `json:"path"`
			All       bool   `json:"all"`
		}
		if err := decodeParams(params, &request); err != nil {
			return true, nil, err
		}
		if strings.TrimSpace(request.Path) == "" {
			return true, nil, errorf(CodeBadRequest, "a path is required")
		}
		files, err := a.filesFor(request.SessionID)
		if err != nil {
			return true, nil, err
		}
		err = files.simple("cannot create the directory", func() bool {
			if request.All {
				return files.worker.CreateDirectories(request.Path)
			}
			return files.worker.CreateDirectory(request.Path)
		})
		return true, map[string]bool{"created": err == nil}, err
	case "files.remove":
		var request struct {
			SessionID uint32 `json:"sessionId"`
			Path      string `json:"path"`
			Dir       bool   `json:"dir"`
		}
		if err := decodeParams(params, &request); err != nil {
			return true, nil, err
		}
		if strings.TrimSpace(request.Path) == "" {
			return true, nil, errorf(CodeBadRequest, "a path is required")
		}
		files, err := a.filesFor(request.SessionID)
		if err != nil {
			return true, nil, err
		}
		err = files.simple("cannot remove", func() bool {
			if request.Dir {
				return files.worker.RemoveDirectory(request.Path)
			}
			return files.worker.RemoveFile(request.Path)
		})
		return true, map[string]bool{"removed": err == nil}, err
	case "files.rename":
		var request struct {
			SessionID uint32 `json:"sessionId"`
			OldPath   string `json:"oldPath"`
			NewPath   string `json:"newPath"`
		}
		if err := decodeParams(params, &request); err != nil {
			return true, nil, err
		}
		if strings.TrimSpace(request.OldPath) == "" || strings.TrimSpace(request.NewPath) == "" {
			return true, nil, errorf(CodeBadRequest, "the old and new paths are required")
		}
		files, err := a.filesFor(request.SessionID)
		if err != nil {
			return true, nil, err
		}
		err = files.simple("cannot rename", func() bool {
			return files.worker.Rename(request.OldPath, request.NewPath)
		})
		return true, map[string]bool{"renamed": err == nil}, err
	case "files.chmod":
		var request struct {
			SessionID uint32 `json:"sessionId"`
			Path      string `json:"path"`
			Mode      uint32 `json:"mode"`
		}
		if err := decodeParams(params, &request); err != nil {
			return true, nil, err
		}
		if strings.TrimSpace(request.Path) == "" {
			return true, nil, errorf(CodeBadRequest, "a path is required")
		}
		files, err := a.filesFor(request.SessionID)
		if err != nil {
			return true, nil, err
		}
		err = files.simple("cannot change permissions", func() bool {
			return files.worker.Chmod(request.Path, os.FileMode(request.Mode).Perm())
		})
		return true, map[string]bool{"changed": err == nil}, err
	case "files.symlink":
		var request struct {
			SessionID uint32 `json:"sessionId"`
			LinkPath  string `json:"linkPath"`
			Target    string `json:"target"`
		}
		if err := decodeParams(params, &request); err != nil {
			return true, nil, err
		}
		if strings.TrimSpace(request.LinkPath) == "" || strings.TrimSpace(request.Target) == "" {
			return true, nil, errorf(CodeBadRequest, "the link path and target are required")
		}
		files, err := a.filesFor(request.SessionID)
		if err != nil {
			return true, nil, err
		}
		err = files.simple("cannot create the link", func() bool {
			return files.worker.Symlink(request.LinkPath, request.Target)
		})
		return true, map[string]bool{"created": err == nil}, err
	case "files.transfers":
		var request struct {
			SessionID uint32 `json:"sessionId"`
		}
		if err := decodeParams(params, &request); err != nil {
			return true, nil, err
		}
		files, err := a.filesFor(request.SessionID)
		if err != nil {
			return true, nil, err
		}
		return true, map[string]any{"transfers": files.transfers()}, nil
	case "files.cancel":
		return a.callTransferControl(method, params)
	case "files.pause":
		return a.callTransferControl(method, params)
	case "files.resume":
		return a.callTransferControl(method, params)
	case "files.retry":
		return a.callTransferControl(method, params)
	case "files.forget":
		return a.callTransferControl(method, params)
	case "files.release":
		var request struct {
			SessionID uint32 `json:"sessionId"`
		}
		if err := decodeParams(params, &request); err != nil {
			return true, nil, err
		}
		a.files.close(request.SessionID)
		return true, map[string]bool{"closed": true}, nil
	}
	return false, nil, nil
}

func (a *App) callTransferControl(method string, params json.RawMessage) (bool, any, error) {
	var request struct {
		SessionID uint32 `json:"sessionId"`
		ID        uint64 `json:"id"`
		Policy    string `json:"policy"`
	}
	if err := decodeParams(params, &request); err != nil {
		return true, nil, err
	}
	files, err := a.filesFor(request.SessionID)
	if err != nil {
		return true, nil, err
	}
	ok := false
	switch method {
	case "files.cancel":
		ok = files.worker.CancelTransfer(request.ID)
	case "files.pause":
		ok = files.worker.Pause(request.ID)
	case "files.resume":
		ok = files.worker.Resume(request.ID)
	case "files.retry":
		policy, err := existingPolicy(request.Policy)
		if err != nil {
			return true, nil, err
		}
		ok = files.worker.Retry(request.ID, policy)
	case "files.forget":
		ok = files.worker.ForgetTransfer(request.ID)
	}
	if !ok {
		return true, nil, errorf(CodeConflict, "the transfer cannot be changed now")
	}
	return true, map[string]bool{"accepted": true}, nil
}

func (a *App) filesFor(sessionID uint32) (*filesSession, error) {
	s, ok := a.sessions.get(sessionID)
	if !ok {
		return nil, errorf(CodeNotFound, "no session with id %d", sessionID)
	}
	return a.files.forSession(s)
}
