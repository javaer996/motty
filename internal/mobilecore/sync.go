package mobilecore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"motty/internal/backup"
	"motty/internal/cloudsync"
	"motty/internal/configstore"
	"motty/internal/gist"
	"motty/internal/s3"
	"motty/internal/vault"
	"motty/internal/webdav"
)

// Syncing and backup. The server keeps one sealed backup.Document: every
// configstore record, every vault secret and the known_hosts text, each with the
// version stamp that orders it against other copies. A sync downloads it, takes
// in what is newer here and uploads the union; a restore replaces the shared data
// with what the document holds and keeps a copy of the old database first.
//
// The file format is the desktop application's, so the same WebDAV folder, S3
// bucket, Gist or .mottybak file can be used from either.

const (
	syncFileName = "sync.json"
	syncTimeout  = 90 * time.Second
	testTimeout  = 30 * time.Second
	syncAttempts = 4
	// tombstoneKeep is how long a deletion is remembered so a device that was
	// away still hears of it.
	tombstoneKeep = 90 * 24 * time.Hour
	// syncDebounce delays an automatic sync after a local change, so a burst
	// of edits makes one request.
	syncDebounce = 5 * time.Second
	// syncRetryFirst and syncRetryMax bound the wait between failed automatic
	// syncs.
	syncRetryFirst = 30 * time.Second
	syncRetryMax   = 30 * time.Minute

	defaultSyncPath     = "motty/backup.mottybak"
	beforeRestoreSuffix = ".before-restore"

	webdavPasswordSecret = "webdav:password"
	s3SecretName         = "s3:secret"
	gistTokenSecret      = "gist:token"
	backupPasswordSecret = "backup:password"
)

// syncSettings is sync.json: where the backup is and what this device last saw
// of it. The passwords and tokens it needs live in the vault, never here.
type syncSettings struct {
	Kind              string          `json:"kind,omitempty"` // "", "s3" or "gist"
	URL               string          `json:"url,omitempty"`
	User              string          `json:"user,omitempty"`
	Path              string          `json:"path,omitempty"`
	S3                *syncS3Config   `json:"s3,omitempty"`
	Gist              *syncGistConfig `json:"gist,omitempty"`
	ETag              string          `json:"etag,omitempty"`
	LastSync          string          `json:"lastSync,omitempty"`
	Manual            bool            `json:"manual,omitempty"`
	AllowInsecureHTTP bool            `json:"allowInsecureHTTP,omitempty"`
}

type syncS3Config struct {
	Endpoint  string `json:"endpoint"`
	Region    string `json:"region,omitempty"`
	Bucket    string `json:"bucket"`
	Key       string `json:"key,omitempty"`
	AccessKey string `json:"accessKey,omitempty"`
	PathStyle bool   `json:"pathStyle,omitempty"`
}

type syncGistConfig struct {
	ID   string `json:"id,omitempty"`
	File string `json:"file,omitempty"`
	API  string `json:"api,omitempty"`
}

// syncManager owns one sync at a time and the timers that start it.
type syncManager struct {
	mu       sync.Mutex
	app      *App
	settings syncSettings
	running  bool
	testing  bool
	errText  string
	retries  int
	again    bool
	closed   bool
	cancel   context.CancelFunc
	timer    *time.Timer
	retry    *time.Timer
	work     sync.WaitGroup

	// newStore builds the cloud client; tests replace it with a fake.
	newStore func(settings syncSettings, secret string) (cloudsync.Store, error)
}

func newSyncManager(app *App) *syncManager {
	manager := &syncManager{app: app, newStore: cloudStore}
	if raw, err := os.ReadFile(filepath.Join(app.data.dir, syncFileName)); err == nil {
		var settings syncSettings
		if json.Unmarshal(raw, &settings) == nil {
			manager.settings = settings.normalized()
		}
	}
	return manager
}

func (s syncSettings) normalized() syncSettings {
	s.Kind = strings.ToLower(strings.TrimSpace(s.Kind))
	s.URL = strings.TrimSpace(s.URL)
	s.User = strings.TrimSpace(s.User)
	s.Path = strings.TrimSpace(s.Path)
	if s.Kind == "" && s.URL != "" {
		s.Kind = "webdav"
	}
	if s.Path == "" && s.Kind == "webdav" {
		s.Path = defaultSyncPath
	}
	if s.S3 != nil {
		s.S3.Endpoint = strings.TrimSpace(s.S3.Endpoint)
		s.S3.Region = strings.TrimSpace(s.S3.Region)
		s.S3.Bucket = strings.TrimSpace(s.S3.Bucket)
		s.S3.Key = strings.TrimSpace(s.S3.Key)
		s.S3.AccessKey = strings.TrimSpace(s.S3.AccessKey)
		if s.S3.Key == "" {
			s.S3.Key = s3.DefaultKey
		}
	}
	if s.Gist != nil {
		s.Gist.ID = strings.TrimSpace(s.Gist.ID)
		s.Gist.File = strings.TrimSpace(s.Gist.File)
		s.Gist.API = strings.TrimRight(strings.TrimSpace(s.Gist.API), "/")
		if s.Gist.File == "" {
			s.Gist.File = gist.DefaultFile
		}
	}
	return s
}

func (s syncSettings) configured() bool {
	switch s.Kind {
	case "s3":
		return s.S3 != nil && s.S3.Endpoint != "" && s.S3.Bucket != ""
	case "gist":
		return s.Gist != nil
	default:
		return s.URL != ""
	}
}

func (s syncSettings) secretName() string {
	switch s.Kind {
	case "s3":
		return s3SecretName
	case "gist":
		return gistTokenSecret
	default:
		return webdavPasswordSecret
	}
}

// saveLocked writes sync.json. The caller holds the manager's lock.
func (m *syncManager) saveLocked() error {
	data, err := json.MarshalIndent(m.settings, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(m.app.data.dir, syncFileName), data)
}

// status is what the settings screen shows. It never carries a secret.
func (m *syncManager) status() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statusLocked()
}

func (m *syncManager) statusLocked() map[string]any {
	settings := m.settings
	status := map[string]any{
		"kind":              settings.Kind,
		"url":               settings.URL,
		"user":              settings.User,
		"path":              settings.Path,
		"etag":              settings.ETag,
		"lastSync":          settings.LastSync,
		"manual":            settings.Manual,
		"allowInsecureHTTP": settings.AllowInsecureHTTP,
		"configured":        settings.configured(),
		"running":           m.running,
		"error":             m.errText,
		"hasSecret":         m.hasSecretLocked(settings.secretName()),
		"hasBackupPassword": m.hasSecretLocked(backupPasswordSecret),
	}
	if settings.S3 != nil {
		status["s3"] = map[string]any{
			"endpoint": settings.S3.Endpoint, "region": settings.S3.Region,
			"bucket": settings.S3.Bucket, "key": settings.S3.Key,
			"accessKey": settings.S3.AccessKey, "pathStyle": settings.S3.PathStyle,
		}
	}
	if settings.Gist != nil {
		status["gist"] = map[string]any{
			"id": settings.Gist.ID, "file": settings.Gist.File, "api": settings.Gist.API,
		}
	}
	return status
}

func (m *syncManager) hasSecretLocked(name string) bool {
	_, found, err := m.app.data.secretGet(name)
	return err == nil && found
}

// busy reports whether a round is running. Replacing the whole data set waits
// for it to finish instead of racing its merge.
func (m *syncManager) busy() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

// configure replaces the target. An empty secret keeps the saved one, so the
// settings screen does not have to show a password back.
func (m *syncManager) configure(request syncConfigureRequest) (map[string]any, error) {
	if m.busy() {
		return nil, errorf(CodeConflict, "a sync is in progress; try again when it finishes")
	}
	settings := syncSettings{
		Kind:              request.Kind,
		URL:               request.URL,
		User:              request.User,
		Path:              request.Path,
		S3:                request.S3,
		Gist:              request.Gist,
		AllowInsecureHTTP: request.AllowInsecureHTTP,
	}.normalized()
	if !settings.configured() {
		return nil, errorf(CodeBadRequest, "the service address is required")
	}
	if settings.Kind == "webdav" {
		if settings.User == "" {
			return nil, errorf(CodeBadRequest, "a user name is required for WebDAV")
		}
		if _, err := webdav.New(webdav.Config{
			URL: settings.URL, User: settings.User, Password: "x", Path: settings.Path,
			AllowInsecureHTTP: settings.AllowInsecureHTTP,
		}, nil); err != nil {
			return nil, errorf(CodeBadRequest, "the WebDAV address is not usable: %v", err)
		}
	}
	if settings.Kind == "s3" {
		if _, err := s3.New(s3.Config{
			Endpoint: settings.S3.Endpoint, Region: settings.S3.Region, Bucket: settings.S3.Bucket,
			Key: settings.S3.Key, AccessKey: settings.S3.AccessKey, Secret: "x",
			PathStyle: settings.S3.PathStyle, AllowInsecureHTTP: settings.AllowInsecureHTTP,
		}, nil); err != nil {
			return nil, errorf(CodeBadRequest, "the S3 settings are not usable: %v", err)
		}
	}
	secret := strings.TrimSpace(request.Secret)
	backupPassword := request.BackupPassword
	if backupPassword != "" && len(backupPassword) < backup.MinPasswordLength {
		return nil, errorf(CodeBadRequest, "the backup password must be at least %d characters", backup.MinPasswordLength)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return nil, errorf(CodeConflict, "a sync is in progress; try again when it finishes")
	}
	if secret != "" {
		if err := m.app.data.secretSet(settings.secretName(), secret); err != nil {
			return nil, errorf(CodeInternal, "cannot save the service password: %v", err)
		}
	}
	if backupPassword != "" {
		if err := m.app.data.secretSet(backupPasswordSecret, backupPassword); err != nil {
			return nil, errorf(CodeInternal, "cannot save the backup password: %v", err)
		}
	}
	if !m.hasSecretLocked(settings.secretName()) {
		return nil, errorf(CodeBadRequest, "the service password is required")
	}
	if !m.hasSecretLocked(backupPasswordSecret) {
		return nil, errorf(CodeBadRequest, "the backup password is required")
	}
	previous := m.settings
	settings.ETag = previous.ETag
	settings.LastSync = previous.LastSync
	if !sameSyncTarget(previous, settings) {
		// A different file has a different tag; a stale one would refuse the
		// first upload.
		settings.ETag, settings.LastSync = "", ""
	}
	if request.Manual != nil {
		settings.Manual = *request.Manual
	} else {
		settings.Manual = previous.Manual
	}
	m.settings = settings
	m.errText, m.retries = "", 0
	if err := m.saveLocked(); err != nil {
		return nil, errorf(CodeInternal, "cannot save the sync settings: %v", err)
	}
	return m.statusLocked(), nil
}

func sameSyncTarget(left, right syncSettings) bool {
	if left.Kind != right.Kind || left.URL != right.URL || left.Path != right.Path {
		return false
	}
	switch right.Kind {
	case "s3":
		if left.S3 == nil || right.S3 == nil {
			return left.S3 == nil && right.S3 == nil
		}
		return left.S3.Endpoint == right.S3.Endpoint && left.S3.Bucket == right.S3.Bucket &&
			left.S3.Key == right.S3.Key && left.S3.Region == right.S3.Region
	case "gist":
		if left.Gist == nil || right.Gist == nil {
			return left.Gist == nil && right.Gist == nil
		}
		return left.Gist.ID == right.Gist.ID && left.Gist.File == right.Gist.File
	}
	return true
}

// clear forgets the target and the secrets it used. The backup on the server is
// not touched.
func (m *syncManager) clear() (map[string]any, error) {
	if m.busy() {
		return nil, errorf(CodeConflict, "a sync is in progress; try again when it finishes")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return nil, errorf(CodeConflict, "a sync is in progress; try again when it finishes")
	}
	if m.timer != nil {
		m.timer.Stop()
		m.timer = nil
	}
	if m.retry != nil {
		m.retry.Stop()
		m.retry = nil
	}
	for _, name := range []string{webdavPasswordSecret, s3SecretName, gistTokenSecret, backupPasswordSecret} {
		if err := m.app.data.secretDelete(name); err != nil {
			return nil, errorf(CodeInternal, "cannot forget the saved sync secret: %v", err)
		}
	}
	m.settings = syncSettings{}
	m.errText, m.retries = "", 0
	if err := m.saveLocked(); err != nil {
		return nil, errorf(CodeInternal, "cannot save the sync settings: %v", err)
	}
	return m.statusLocked(), nil
}

// cloudStore builds the client for one target.
func cloudStore(settings syncSettings, secret string) (cloudsync.Store, error) {
	client := cloudsync.HTTPClient(&http.Client{Timeout: syncTimeout}, settings.AllowInsecureHTTP)
	switch settings.Kind {
	case "s3":
		return s3.New(s3.Config{
			Endpoint: settings.S3.Endpoint, Region: settings.S3.Region, Bucket: settings.S3.Bucket,
			Key: settings.S3.Key, AccessKey: settings.S3.AccessKey, Secret: secret,
			PathStyle: settings.S3.PathStyle, AllowInsecureHTTP: settings.AllowInsecureHTTP,
		}, client)
	case "gist":
		return gist.New(gist.Config{
			Token: secret, ID: settings.Gist.ID, File: settings.Gist.File, APIBase: settings.Gist.API,
		}, client)
	default:
		return webdav.New(webdav.Config{
			URL: settings.URL, User: settings.User, Password: secret, Path: settings.Path,
			AllowInsecureHTTP: settings.AllowInsecureHTTP,
		}, client)
	}
}

// test starts a connection test in the background; the result arrives as a
// sync.test event. It never runs two tests at once.
func (m *syncManager) test() (map[string]any, error) {
	m.mu.Lock()
	if m.testing {
		m.mu.Unlock()
		return map[string]any{"started": false}, nil
	}
	settings := m.settings
	secret, found, err := m.app.data.secretGet(settings.secretName())
	if !settings.configured() || err != nil || !found {
		m.mu.Unlock()
		return nil, errorf(CodeBadRequest, "fill in the sync settings first")
	}
	m.testing = true
	m.work.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.work.Done()
		store, err := m.newStore(settings, secret)
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
			err = store.Test(ctx)
			cancel()
		}
		m.mu.Lock()
		m.testing = false
		m.mu.Unlock()
		payload := map[string]any{"ok": err == nil}
		if err != nil {
			payload["error"] = err.Error()
		}
		m.app.events.push("sync.test", payload)
	}()
	return map[string]any{"started": true}, nil
}

// noteChange schedules an automatic sync after a local edit, when the user did
// not turn automatic syncing off.
func (m *syncManager) noteChange() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.settings.Manual || !m.settings.configured() || m.running {
		return
	}
	if m.timer != nil {
		m.timer.Stop()
	}
	m.timer = time.AfterFunc(syncDebounce, func() { m.start(true) })
}

// now starts a sync. The network work is on a goroutine; the front end follows
// sync.state, sync.done and sync.error events.
func (m *syncManager) now() (map[string]any, error) {
	m.mu.Lock()
	if !m.settings.configured() {
		m.mu.Unlock()
		return nil, errorf(CodeBadRequest, "sync is not configured")
	}
	if m.running {
		m.again = true
		m.mu.Unlock()
		return map[string]any{"started": false, "running": true}, nil
	}
	m.mu.Unlock()
	m.start(false)
	return map[string]any{"started": true}, nil
}

func (m *syncManager) start(quiet bool) {
	m.mu.Lock()
	if m.closed || m.running {
		m.mu.Unlock()
		return
	}
	settings := m.settings
	m.mu.Unlock()
	if !settings.configured() {
		return
	}
	secret, foundSecret, err := m.app.data.secretGet(settings.secretName())
	backupPassword, foundBackup, backupErr := m.app.data.secretGet(backupPasswordSecret)
	if err != nil || backupErr != nil {
		m.fail(fmt.Errorf("cannot read the saved sync secrets: %v", firstError(err, backupErr)))
		return
	}
	if !foundSecret || !foundBackup {
		m.fail(errors.New("the sync service password or backup password is missing"))
		return
	}
	store, err := m.newStore(settings, secret)
	if err != nil {
		m.fail(err)
		return
	}
	m.mu.Lock()
	if m.closed || m.running {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), syncTimeout)
	m.running, m.cancel, m.errText = true, cancel, ""
	m.work.Add(1)
	m.mu.Unlock()
	m.app.events.push("sync.state", map[string]any{"running": true, "phase": "download", "quiet": quiet})
	go func() {
		defer m.work.Done()
		m.round(ctx, store, backupPassword, "", false, 1)
	}()
}

func firstError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// round downloads the server's file and merges it in.
func (m *syncManager) round(ctx context.Context, store cloudsync.Store, backupPassword, tag string, missing bool, attempt int) {
	var remote backup.Document
	data, readTag, err := store.Get(ctx)
	if errors.Is(err, cloudsync.ErrNotFound) {
		missing, err = true, nil
	} else if err == nil {
		tag = readTag
		remote, err = backup.Open(data, backupPassword)
	}
	if err != nil {
		m.fail(err)
		return
	}
	m.app.events.push("sync.state", map[string]any{"running": true, "phase": "merge"})
	upload, err := m.mergeRemote(remote, missing)
	if err != nil {
		m.fail(err)
		return
	}
	if upload == nil {
		m.done(tag, "")
		return
	}
	m.app.events.push("sync.state", map[string]any{"running": true, "phase": "upload"})
	sealed, err := backup.Seal(*upload, backupPassword)
	if err != nil {
		m.fail(err)
		return
	}
	newTag, err := store.Put(ctx, sealed, tag)
	if err == nil && newTag == "" {
		// The service did not say which version it stored; reading it back does.
		if _, read, readErr := store.Get(ctx); readErr == nil {
			newTag = read
		}
	}
	created := ""
	if holder, ok := store.(interface{ ID() string }); ok {
		created = holder.ID() // a first upload makes the gist; remember where
	}
	if errors.Is(err, cloudsync.ErrChanged) && attempt < syncAttempts {
		m.round(ctx, store, backupPassword, tag, missing, attempt+1)
		return
	}
	if err != nil {
		m.fail(err)
		return
	}
	m.done(newTag, created)
}

// mergeRemote takes the server's document in and returns what to upload, or nil
// when the server already holds everything here.
func (m *syncManager) mergeRemote(remote backup.Document, missing bool) (*backup.Document, error) {
	app := m.app
	if remote.Format > configFormat {
		return nil, backup.ErrTooNew
	}
	if _, err := app.data.vault.Export(); err != nil {
		return nil, err
	}
	var received []vault.Secret
	var stamps []configstore.Stamp
	for _, secret := range remote.Secrets {
		if excludedFromBackup(secret.Name) || !app.data.config.Acceptable(configstore.Stamp(secret.Stamp)) {
			continue
		}
		received = append(received, secret)
		stamps = append(stamps, configstore.Stamp(secret.Stamp))
	}
	secretsChanged := false
	if len(received) > 0 {
		applied, err := app.data.vault.Merge(received)
		if err != nil {
			return nil, err
		}
		secretsChanged = len(applied) > 0
	}
	if err := app.data.config.Observe(stamps...); err != nil {
		return nil, err
	}
	result, err := app.data.config.Merge(remote.Records)
	if err != nil {
		return nil, err
	}
	if len(result.Applied) > 0 {
		if err := app.hosts.reload(); err != nil {
			return nil, err
		}
	}
	if secretsChanged {
		app.ai.settings.reloadKey()
	}
	if err := app.mergeKnownHosts(remote.KnownHosts); err != nil {
		return nil, err
	}
	_, _ = app.data.config.Prune(tombstoneKeep)
	_, _ = app.data.vault.Prune(string(configstore.CutoffStamp(time.Now().Add(-tombstoneKeep))))

	records, err := app.data.config.Records()
	if err != nil {
		return nil, err
	}
	local := backup.Document{
		Created: time.Now().UTC(), Device: app.data.config.Device(), Format: configFormat,
		Writer: "motty-ios", Records: records,
	}
	local.KnownHosts = unionLines(remote.KnownHosts, app.readKnownHosts())
	exported, err := app.data.vault.Export()
	if err != nil {
		return nil, err
	}
	for _, secret := range exported {
		if !excludedFromBackup(secret.Name) {
			local.Secrets = append(local.Secrets, secret)
		}
	}
	local.Secrets = unionSecrets(local.Secrets, received)

	newer, err := app.data.config.Newer(remote.Records)
	if err != nil {
		return nil, err
	}
	if !missing && len(newer) == 0 && !secretsNewer(local.Secrets, remote.Secrets) &&
		!hasNewLines(local.KnownHosts, remote.KnownHosts) {
		return nil, nil
	}
	return &local, nil
}

// done records a finished sync and tells the front end.
func (m *syncManager) done(tag, created string) {
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	if m.settings.Kind == "gist" && created != "" && m.settings.Gist != nil && m.settings.Gist.ID != created {
		m.settings.Gist.ID = created
	}
	m.settings.ETag = tag
	m.settings.LastSync = time.Now().Format(time.RFC3339)
	m.running, m.errText, m.retries = false, "", 0
	err := m.saveLocked()
	again := m.again
	m.again = false
	status := m.statusLocked()
	m.mu.Unlock()
	if err != nil {
		status["error"] = "the sync settings could not be saved: " + err.Error()
	}
	m.app.events.push("sync.done", status)
	if again {
		m.start(true)
	}
}

func (m *syncManager) fail(err error) {
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.running = false
	m.errText = err.Error()
	retry := m.retryable(err) && !m.closed && !m.settings.Manual && m.settings.configured()
	if retry {
		m.retries++
		delay := syncRetryFirst << min(m.retries-1, 6)
		if delay > syncRetryMax || delay <= 0 {
			delay = syncRetryMax
		}
		if m.retry != nil {
			m.retry.Stop()
		}
		m.retry = time.AfterFunc(delay, func() { m.start(true) })
	}
	status := m.statusLocked()
	m.mu.Unlock()
	m.app.events.push("sync.error", map[string]any{"message": err.Error(), "status": status})
}

// retryable says whether waiting could cure the failure. A refused login or a
// wrong backup password needs the user, not another attempt.
func (m *syncManager) retryable(err error) bool {
	switch {
	case errors.Is(err, cloudsync.ErrUnauthorized), errors.Is(err, cloudsync.ErrInsecure):
		return false
	case errors.Is(err, backup.ErrWrongPassword), errors.Is(err, backup.ErrNotBackup),
		errors.Is(err, backup.ErrTooNew), errors.Is(err, backup.ErrWeakPassword),
		errors.Is(err, cloudsync.ErrTooLarge):
		return false
	}
	return true
}

// close stops the timers and waits for a request on its way to end.
func (m *syncManager) close() {
	m.mu.Lock()
	m.closed = true
	if m.timer != nil {
		m.timer.Stop()
		m.timer = nil
	}
	if m.retry != nil {
		m.retry.Stop()
		m.retry = nil
	}
	cancel := m.cancel
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.work.Wait()
}

// restoreRemote replaces the shared data with the server's document. It is what
// the Restore button does; the sync itself only merges.
func (m *syncManager) restoreRemote() (map[string]any, error) {
	if err := m.app.ensureNoOpenConnections(); err != nil {
		return nil, err
	}
	if m.busy() {
		return nil, errorf(CodeConflict, "a sync is in progress; try again when it finishes")
	}
	m.mu.Lock()
	settings := m.settings
	m.mu.Unlock()
	if !settings.configured() {
		return nil, errorf(CodeBadRequest, "sync is not configured")
	}
	secret, foundSecret, err := m.app.data.secretGet(settings.secretName())
	backupPassword, foundBackup, backupErr := m.app.data.secretGet(backupPasswordSecret)
	if err != nil || backupErr != nil || !foundSecret || !foundBackup {
		return nil, errorf(CodeUnavailable, "the saved sync secrets cannot be read")
	}
	store, err := m.newStore(settings, secret)
	if err != nil {
		return nil, errorf(CodeBadRequest, "the sync settings are not usable: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), syncTimeout)
	defer cancel()
	data, tag, err := store.Get(ctx)
	if err != nil {
		return nil, syncError(err)
	}
	document, err := backup.Open(data, backupPassword)
	if err != nil {
		return nil, errorf(CodeBadRequest, "the backup password does not open the server's file: %v", err)
	}
	restored, err := m.app.applyBackupDocument(document)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.settings.ETag = tag
	m.settings.LastSync = time.Now().Format(time.RFC3339)
	m.errText, m.retries = "", 0
	_ = m.saveLocked()
	m.mu.Unlock()
	restored["tag"] = tag
	return restored, nil
}

// syncError names a network failure in words the settings screen can show.
func syncError(err error) error {
	switch {
	case errors.Is(err, cloudsync.ErrUnauthorized):
		return errorf(CodeUnavailable, "the service refused the login")
	case errors.Is(err, cloudsync.ErrNotFound):
		return errorf(CodeNotFound, "there is no backup on the server yet")
	default:
		return errorf(CodeUnavailable, "the server could not be reached: %v", err)
	}
}

// ensureNoOpenConnections refuses a restore while a terminal or a desktop is
// open: those tabs hold the hosts a restore would replace.
func (a *App) ensureNoOpenConnections() error {
	if len(a.sessions.list()) > 0 {
		return errorf(CodeConflict, "close every terminal tab before restoring")
	}
	if a.desktops.busy() {
		return errorf(CodeConflict, "close every remote desktop before restoring")
	}
	return nil
}

// ---- backup files ----

// exportBackup seals the shared data into <DataDir>/exports and returns where it
// is. The front end shares the file from there.
func (a *App) exportBackup(password string) (map[string]any, error) {
	if len(password) < backup.MinPasswordLength {
		return nil, errorf(CodeBadRequest, "the backup password must be at least %d characters", backup.MinPasswordLength)
	}
	document, err := a.backupDocument()
	if err != nil {
		return nil, err
	}
	sealed, err := backup.Seal(document, password)
	if err != nil {
		return nil, errorf(CodeInternal, "cannot seal the backup: %v", err)
	}
	dir := filepath.Join(a.data.dir, "exports")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, errorf(CodeInternal, "cannot create the export directory: %v", err)
	}
	name := "motty-" + time.Now().Format("20060102-150405") + ".mottybak"
	path := filepath.Join(dir, name)
	if err := writeFileAtomic(path, sealed); err != nil {
		return nil, errorf(CodeInternal, "cannot write the backup: %v", err)
	}
	a.trimExports(dir)
	return map[string]any{"path": path, "name": name, "bytes": len(sealed)}, nil
}

// trimExports keeps the five newest backups; older ones are this device's own
// copies that the Files export has already taken elsewhere.
func (a *App) trimExports(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var files []os.DirEntry
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".mottybak") {
			files = append(files, entry)
		}
	}
	if len(files) <= 5 {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name() < files[j].Name() })
	for _, entry := range files[:len(files)-5] {
		_ = os.Remove(filepath.Join(dir, entry.Name()))
	}
}

// backupDocument gathers everything a backup holds. The sync service's own
// passwords stay out, as on the desktop.
func (a *App) backupDocument() (backup.Document, error) {
	records, err := a.data.config.Records()
	if err != nil {
		return backup.Document{}, errorf(CodeInternal, "cannot read the shared configuration: %v", err)
	}
	exported, err := a.data.vault.Export()
	if err != nil {
		return backup.Document{}, errorf(CodeInternal, "cannot read the saved secrets: %v", err)
	}
	document := backup.Document{
		Created: time.Now().UTC(), Device: a.data.config.Device(), Format: configFormat,
		Writer: "motty-ios", Records: records, KnownHosts: a.readKnownHosts(),
	}
	for _, secret := range exported {
		if !excludedFromBackup(secret.Name) {
			document.Secrets = append(document.Secrets, secret)
		}
	}
	return document, nil
}

// inspectBackup answers whether a file looks like a Motty backup, without its
// password.
func (a *App) inspectBackup(path string) (map[string]any, error) {
	if !withinDir(a.data.dir, path) {
		return nil, errorf(CodeBadRequest, "the backup must be inside the app's directory")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errorf(CodeBadRequest, "cannot read the backup: %v", err)
	}
	var envelope struct {
		Format  string `json:"format"`
		Version int    `json:"version"`
	}
	looks := json.Unmarshal(raw, &envelope) == nil && envelope.Format == "motty-backup"
	return map[string]any{"backup": looks, "version": envelope.Version, "bytes": len(raw)}, nil
}

// restoreBackupFile replaces the shared data with a local .mottybak file.
func (a *App) restoreBackupFile(path, password string) (map[string]any, error) {
	if err := a.ensureNoOpenConnections(); err != nil {
		return nil, err
	}
	if !withinDir(a.data.dir, path) {
		return nil, errorf(CodeBadRequest, "the backup must be inside the app's directory")
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, errorf(CodeNotFound, "the backup file is gone")
	}
	if err != nil {
		return nil, errorf(CodeBadRequest, "cannot read the backup: %v", err)
	}
	if info.Size() > backup.MaxSize {
		return nil, errorf(CodeBadRequest, "the backup is larger than %d MB", backup.MaxSize>>20)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errorf(CodeBadRequest, "cannot read the backup: %v", err)
	}
	document, err := backup.Open(raw, password)
	if err != nil {
		return nil, errorf(CodeBadRequest, "the backup cannot be opened: %v", err)
	}
	return a.applyBackupDocument(document)
}

// applyBackupDocument replaces the shared records with the document's, adds its
// known hosts and saves its secrets. The database it replaces is kept as
// config.db.before-restore.
func (a *App) applyBackupDocument(document backup.Document) (map[string]any, error) {
	if a.sync != nil && a.sync.busy() {
		return nil, errorf(CodeConflict, "a sync is in progress; try again when it finishes")
	}
	if document.Format > configFormat {
		return nil, errorf(CodeBadRequest, "the backup was made by a newer version of Motty")
	}
	entries := make([]configstore.Entry, 0, len(document.Records))
	live := make(map[configstore.Key]bool, len(document.Records))
	for _, record := range document.Records {
		if record.Kind == "" || record.ID == "" {
			return nil, errorf(CodeBadRequest, "the backup holds an unnamed record")
		}
		key := configstore.Key{Kind: record.Kind, ID: record.ID}
		if live[key] {
			return nil, errorf(CodeBadRequest, "the backup holds %s %s twice", record.Kind, record.ID)
		}
		live[key] = true
		if !record.Deleted {
			entries = append(entries, configstore.Entry{Kind: record.Kind, ID: record.ID, Payload: record.Payload})
		}
	}
	snapshot, err := a.data.config.Snapshot()
	if err != nil {
		return nil, errorf(CodeInternal, "cannot copy the configuration before restoring: %v", err)
	}
	if err := keepBeforeRestore(filepath.Join(a.data.dir, "config.db"), snapshot); err != nil {
		return nil, errorf(CodeInternal, "cannot keep a copy of the configuration: %v", err)
	}
	current, _, err := a.data.config.Load()
	if err != nil {
		return nil, errorf(CodeInternal, "cannot read the shared configuration: %v", err)
	}
	change := configstore.Change{Writer: "motty-ios-restore", Put: entries}
	for _, entry := range current {
		key := configstore.Key{Kind: entry.Kind, ID: entry.ID}
		if !live[key] {
			change.Delete = append(change.Delete, key)
		}
	}
	if _, err := a.data.config.Commit(change); err != nil {
		return nil, errorf(CodeInternal, "cannot restore the configuration: %v", err)
	}
	if err := a.hosts.reload(); err != nil {
		return nil, err
	}
	if err := a.mergeKnownHosts(document.KnownHosts); err != nil {
		return nil, errorf(CodeInternal, "cannot merge the trusted hosts: %v", err)
	}
	for _, secret := range document.Secrets {
		if secret.Deleted || excludedFromBackup(secret.Name) {
			continue
		}
		if err := a.data.secretSet(secret.Name, secret.Value); err != nil {
			return nil, errorf(CodeInternal, "cannot save the secrets from the backup: %v", err)
		}
	}
	a.ai.settings.reloadKey()
	return map[string]any{
		"hosts":   len(a.hosts.list()),
		"records": len(entries),
		"created": document.Created.UTC().Format(time.RFC3339),
		"device":  document.Device,
	}, nil
}

// keepBeforeRestore writes the database as it was to <path>.before-restore. A
// copy already there is the state before an earlier restore, which a second
// restore must not destroy, so it is moved aside.
func keepBeforeRestore(path string, data []byte) error {
	target := path + beforeRestoreSuffix
	if info, err := os.Stat(target); err == nil {
		aside := target + "." + info.ModTime().UTC().Format("20060102T150405.000000")
		if err := os.Rename(target, aside); err != nil {
			return err
		}
	}
	return writeFileAtomic(target, data)
}

// withinDir says whether path is inside dir, so a file picked in the UI cannot
// point the backup code at another app's container.
func withinDir(dir, path string) bool {
	base, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	target, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// ---- shared helpers ----

// excludedFromBackup says whether a saved secret stays out of backups and
// syncs: the credentials of the sync itself.
func excludedFromBackup(name string) bool {
	switch name {
	case webdavPasswordSecret, s3SecretName, gistTokenSecret, backupPasswordSecret:
		return true
	}
	return false
}

func (a *App) readKnownHosts() string {
	data, err := os.ReadFile(filepath.Join(a.data.dir, "known_hosts"))
	if err != nil {
		return ""
	}
	return string(data)
}

// mergeKnownHosts adds the incoming lines this device lacks; what it already
// trusts stays.
func (a *App) mergeKnownHosts(incoming string) error {
	if strings.TrimSpace(incoming) == "" {
		return nil
	}
	path := filepath.Join(a.data.dir, "known_hosts")
	current := a.readKnownHosts()
	have := map[string]bool{}
	for _, line := range strings.Split(current, "\n") {
		have[strings.TrimSpace(line)] = true
	}
	merged := strings.TrimRight(current, "\n")
	added := false
	for _, line := range strings.Split(incoming, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || have[line] {
			continue
		}
		if merged != "" {
			merged += "\n"
		}
		merged += line
		have[line], added = true, true
	}
	if !added {
		return nil
	}
	return writeFileAtomic(path, []byte(merged+"\n"))
}

// unionLines is remote's lines followed by the lines of local it lacks.
func unionLines(remote, local string) string {
	have := map[string]bool{}
	var lines []string
	for _, text := range []string{remote, local} {
		for _, line := range strings.Split(text, "\n") {
			if line = strings.TrimSpace(line); line != "" && !have[line] {
				have[line] = true
				lines = append(lines, line)
			}
		}
	}
	return strings.Join(lines, "\n")
}

// unionSecrets keeps the later version of each name.
func unionSecrets(local, other []vault.Secret) []vault.Secret {
	byName := make(map[string]vault.Secret, len(local)+len(other))
	for _, list := range [][]vault.Secret{other, local} {
		for _, secret := range list {
			if known, ok := byName[secret.Name]; !ok || known.Stamp < secret.Stamp {
				byName[secret.Name] = secret
			}
		}
	}
	union := make([]vault.Secret, 0, len(byName))
	for _, secret := range byName {
		union = append(union, secret)
	}
	sort.Slice(union, func(i, j int) bool { return union[i].Name < union[j].Name })
	return union
}

// secretsNewer says whether local holds a secret that remote lacks or has an
// older version of.
func secretsNewer(local, remote []vault.Secret) bool {
	have := make(map[string]string, len(remote))
	for _, secret := range remote {
		have[secret.Name] = secret.Stamp
	}
	for _, secret := range local {
		if stamp, ok := have[secret.Name]; !ok || stamp < secret.Stamp {
			return true
		}
	}
	return false
}

// hasNewLines says whether local has a line that remote lacks.
func hasNewLines(local, remote string) bool {
	have := map[string]bool{}
	for _, line := range strings.Split(remote, "\n") {
		have[strings.TrimSpace(line)] = true
	}
	for _, line := range strings.Split(local, "\n") {
		if line = strings.TrimSpace(line); line != "" && !have[line] {
			return true
		}
	}
	return false
}

// ---- RPC ----

type syncConfigureRequest struct {
	Kind              string          `json:"kind"`
	URL               string          `json:"url"`
	User              string          `json:"user"`
	Path              string          `json:"path"`
	S3                *syncS3Config   `json:"s3"`
	Gist              *syncGistConfig `json:"gist"`
	Secret            string          `json:"secret"`
	BackupPassword    string          `json:"backupPassword"`
	AllowInsecureHTTP bool            `json:"allowInsecureHTTP"`
	Manual            *bool           `json:"manual"`
}

// callSync handles sync.* methods.
func (a *App) callSync(method string, params json.RawMessage) (bool, any, error) {
	if !strings.HasPrefix(method, "sync.") {
		return false, nil, nil
	}
	switch method {
	case "sync.status":
		return true, a.sync.status(), nil
	case "sync.configure":
		var request syncConfigureRequest
		if err := decodeParams(params, &request); err != nil {
			return true, nil, err
		}
		status, err := a.sync.configure(request)
		if err == nil {
			a.requestSync()
		}
		return true, status, err
	case "sync.clear":
		status, err := a.sync.clear()
		return true, status, err
	case "sync.test":
		started, err := a.sync.test()
		return true, started, err
	case "sync.now":
		started, err := a.sync.now()
		return true, started, err
	case "sync.restore":
		restored, err := a.sync.restoreRemote()
		if err == nil {
			a.events.push("sync.restored", restored)
		}
		return true, restored, err
	default:
		return true, nil, errorf(CodeBadRequest, "unknown method %q", method)
	}
}

// callBackup handles backup.* methods.
func (a *App) callBackup(method string, params json.RawMessage) (bool, any, error) {
	if !strings.HasPrefix(method, "backup.") {
		return false, nil, nil
	}
	switch method {
	case "backup.export":
		var request struct {
			Password string `json:"password"`
		}
		if err := decodeParams(params, &request); err != nil {
			return true, nil, err
		}
		result, err := a.exportBackup(request.Password)
		return true, result, err
	case "backup.inspect":
		var request struct {
			Path string `json:"path"`
		}
		if err := decodeParams(params, &request); err != nil {
			return true, nil, err
		}
		result, err := a.inspectBackup(request.Path)
		return true, result, err
	case "backup.restore":
		var request struct {
			Path     string `json:"path"`
			Password string `json:"password"`
		}
		if err := decodeParams(params, &request); err != nil {
			return true, nil, err
		}
		result, err := a.restoreBackupFile(request.Path, request.Password)
		return true, result, err
	default:
		return true, nil, errorf(CodeBadRequest, "unknown method %q", method)
	}
}

// requestSync asks for a debounced sync after a local change.
func (a *App) requestSync() {
	if a.sync != nil {
		a.sync.noteChange()
	}
}
