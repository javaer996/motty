package mobilecore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"motty/internal/backup"
	"motty/internal/cloudsync"
	"motty/internal/configstore"
)

// fakeCloud is one remote file in memory.
type fakeCloud struct {
	mu   sync.Mutex
	data []byte
	tag  string
	puts int
}

func (cloud *fakeCloud) Test(context.Context) error { return nil }

func (cloud *fakeCloud) Get(context.Context) ([]byte, string, error) {
	cloud.mu.Lock()
	defer cloud.mu.Unlock()
	if cloud.data == nil {
		return nil, "", cloudsync.ErrNotFound
	}
	return append([]byte(nil), cloud.data...), cloud.tag, nil
}

func (cloud *fakeCloud) Put(_ context.Context, data []byte, previous string) (string, error) {
	cloud.mu.Lock()
	defer cloud.mu.Unlock()
	if cloud.data == nil {
		if previous != "" {
			return "", cloudsync.ErrChanged
		}
	} else if previous != cloud.tag {
		return "", cloudsync.ErrChanged
	}
	cloud.data = append([]byte(nil), data...)
	cloud.puts++
	cloud.tag = fmt.Sprintf("tag-%d", cloud.puts)
	return cloud.tag, nil
}

func syncTestApp(t *testing.T, cloud cloudsync.Store) *App {
	t.Helper()
	app, err := Start(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	app.sync.newStore = func(syncSettings, string) (cloudsync.Store, error) { return cloud, nil }
	configured := callApp(t, app, "sync.configure", `{
		"kind":"webdav","url":"https://dav.example.com/dav","user":"u","path":"motty/backup.mottybak",
		"secret":"password","backupPassword":"backup-pass","manual":true}`)
	if !configured.OK {
		t.Fatalf("sync.configure failed: %+v", configured.Error)
	}
	return app
}

func startSync(t *testing.T, app *App) map[string]any {
	t.Helper()
	started := callApp(t, app, "sync.now", "")
	if !started.OK {
		t.Fatalf("sync.now failed: %+v", started.Error)
	}
	return waitForEvent(t, app, "sync.done", "sync.error")
}

func waitForEvent(t *testing.T, app *App, kinds ...string) map[string]any {
	t.Helper()
	wanted := map[string]bool{}
	for _, kind := range kinds {
		wanted[kind] = true
	}
	buffer := make([]byte, 64<<10)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		count := app.PollEvent(buffer)
		switch {
		case count > 0:
			var event map[string]any
			if err := json.Unmarshal(buffer[:count], &event); err != nil {
				t.Fatal(err)
			}
			if kind, _ := event["type"].(string); wanted[kind] {
				if kind == "sync.error" {
					t.Fatalf("sync failed: %v", event["message"])
				}
				return event
			}
		case count < 0:
			buffer = make([]byte, -count)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	t.Fatalf("no %v event in time", kinds)
	return nil
}

func hostCount(t *testing.T, app *App) int {
	t.Helper()
	listed := callApp(t, app, "hosts.list", "")
	var list struct {
		Hosts []Host `json:"hosts"`
	}
	if err := json.Unmarshal(listed.Result, &list); err != nil {
		t.Fatal(err)
	}
	return len(list.Hosts)
}

// blockingCloud holds the first download so a test can look at the manager
// while a round is in flight.
type blockingCloud struct {
	fakeCloud
	release chan struct{}
}

func (cloud *blockingCloud) Get(ctx context.Context) ([]byte, string, error) {
	select {
	case <-cloud.release:
	case <-ctx.Done():
		return nil, "", ctx.Err()
	}
	return cloud.fakeCloud.Get(ctx)
}

func TestSyncReplacesWaitForTheRunningRound(t *testing.T) {
	cloud := &blockingCloud{release: make(chan struct{})}
	app := syncTestApp(t, cloud)

	started := callApp(t, app, "sync.now", "")
	if !started.OK {
		t.Fatalf("sync.now failed: %+v", started.Error)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !app.sync.busy() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !app.sync.busy() {
		t.Fatal("the sync round did not start")
	}

	configured := callApp(t, app, "sync.configure", `{
		"kind":"webdav","url":"https://other.example.com/dav","user":"u","path":"motty/backup.mottybak",
		"secret":"another-secret","backupPassword":"another-passphrase","manual":true}`)
	if configured.OK || configured.Error == nil || configured.Error.Code != CodeConflict {
		t.Fatalf("configure during a sync = %+v", configured.Error)
	}
	if _, err := app.sync.clear(); err == nil {
		t.Fatal("clear succeeded during a sync")
	}
	if _, err := app.sync.restoreRemote(); err == nil {
		t.Fatal("restore succeeded during a sync")
	}

	close(cloud.release)
	waitForEvent(t, app, "sync.done", "sync.error")
}

func TestBackupExportRestoreRoundTrip(t *testing.T) {
	app, err := Start(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	first := callApp(t, app, "hosts.save", `{"label":"web","address":"example.com","user":"root"}`)
	if !first.OK {
		t.Fatal(first.Error)
	}
	exported := callApp(t, app, "backup.export", `{"password":"backup-pass"}`)
	if !exported.OK {
		t.Fatalf("backup.export failed: %+v", exported.Error)
	}
	var export struct {
		Path  string `json:"path"`
		Name  string `json:"name"`
		Bytes int    `json:"bytes"`
	}
	if err := json.Unmarshal(exported.Result, &export); err != nil {
		t.Fatal(err)
	}
	if export.Bytes == 0 || filepath.Dir(export.Path) != filepath.Join(app.data.dir, "exports") {
		t.Fatalf("export = %+v", export)
	}

	if inspected := callApp(t, app, "backup.inspect", fmt.Sprintf(`{"path":%q}`, export.Path)); !inspected.OK {
		t.Fatalf("backup.inspect failed: %+v", inspected.Error)
	} else {
		var info struct {
			Backup bool `json:"backup"`
		}
		_ = json.Unmarshal(inspected.Result, &info)
		if !info.Backup {
			t.Fatalf("inspect did not recognize the backup: %s", inspected.Result)
		}
	}

	if second := callApp(t, app, "hosts.save", `{"label":"later","address":"later.example.com","user":"root"}`); !second.OK {
		t.Fatal(second.Error)
	}
	if hostCount(t, app) != 2 {
		t.Fatal("the second host was not saved")
	}

	restored := callApp(t, app, "backup.restore", fmt.Sprintf(`{"path":%q,"password":"backup-pass"}`, export.Path))
	if !restored.OK {
		t.Fatalf("backup.restore failed: %+v", restored.Error)
	}
	if hostCount(t, app) != 1 {
		t.Fatalf("after restore there are %d hosts, want 1", hostCount(t, app))
	}
	if _, err := os.Stat(filepath.Join(app.data.dir, "config.db.before-restore")); err != nil {
		t.Fatalf("the pre-restore copy is missing: %v", err)
	}
	wrong := callApp(t, app, "backup.restore", fmt.Sprintf(`{"path":%q,"password":"wrong-pass"}`, export.Path))
	if wrong.OK || wrong.Error == nil {
		t.Fatal("a wrong backup password was accepted")
	}
	outside := callApp(t, app, "backup.restore", `{"path":"/etc/passwd","password":"backup-pass"}`)
	if outside.OK || outside.Error == nil {
		t.Fatal("a path outside the app directory was accepted")
	}
}

func TestSyncMergesHostsAndSecrets(t *testing.T) {
	cloud := &fakeCloud{}
	first := syncTestApp(t, cloud)
	second := syncTestApp(t, cloud)

	saved := callApp(t, first, "hosts.save", `{"label":"shared","address":"shared.example.com","user":"root"}`)
	if !saved.OK {
		t.Fatal(saved.Error)
	}
	startSync(t, first)
	startSync(t, second)
	if hostCount(t, second) != 1 {
		t.Fatalf("second device has %d hosts, want 1", hostCount(t, second))
	}

	// A key and the assistant's API key travel too.
	key := base64.StdEncoding.EncodeToString(testPrivateKey(t, ""))
	imported := callApp(t, first, "keys.import", fmt.Sprintf(`{"name":"id_test","data":%q}`, key))
	if !imported.OK {
		t.Fatalf("keys.import failed: %+v", imported.Error)
	}
	settings := callApp(t, first, "ai.settings.save", `{"provider":"openai","model":"m","apiKey":"sk-shared"}`)
	if !settings.OK {
		t.Fatalf("ai.settings.save failed: %+v", settings.Error)
	}
	startSync(t, first)
	startSync(t, second)
	listedKeys := callApp(t, second, "keys.list", "")
	var keyList struct {
		Keys []keyInfo `json:"keys"`
	}
	if err := json.Unmarshal(listedKeys.Result, &keyList); err != nil {
		t.Fatal(err)
	}
	if len(keyList.Keys) != 1 || keyList.Keys[0].Name != "id_test" {
		t.Fatalf("second device keys = %+v", keyList.Keys)
	}
	if got, _ := second.ai.settings.get().current(); got.APIKey != "sk-shared" {
		t.Fatalf("second device assistant key = %q", got.APIKey)
	}

	// A deletion travels as a tombstone.
	var host Host
	if err := json.Unmarshal(saved.Result, &host); err != nil {
		t.Fatal(err)
	}
	if deleted := callApp(t, second, "hosts.delete", fmt.Sprintf(`{"id":%q}`, host.ID)); !deleted.OK {
		t.Fatal(deleted.Error)
	}
	startSync(t, second)
	startSync(t, first)
	if hostCount(t, first) != 0 {
		t.Fatalf("first device still has %d hosts after the deletion", hostCount(t, first))
	}

	// A sync with nothing new does not upload.
	cloud.mu.Lock()
	before := cloud.puts
	cloud.mu.Unlock()
	startSync(t, first)
	startSync(t, second)
	cloud.mu.Lock()
	after := cloud.puts
	cloud.mu.Unlock()
	if after != before {
		t.Fatalf("an idle sync uploaded %d times", after-before)
	}
}

// The desktop release line has moved its record epoch past this tree. The
// automatic sync takes the newer epochs in, and a manual restore takes any
// document at all: refusing an epoch would leave the phone unable to bring the
// desktop's copy over.
func TestSyncTakesANewerDesktopBackup(t *testing.T) {
	cloud := &fakeCloud{}
	app := syncTestApp(t, cloud)

	if saved := callApp(t, app, "hosts.save", `{"label":"server","address":"server.example.com","user":"root"}`); !saved.OK {
		t.Fatal(saved.Error)
	}
	document, err := app.backupDocument()
	if err != nil {
		t.Fatal(err)
	}
	password, found, err := app.data.secretGet(backupPasswordSecret)
	if err != nil || !found {
		t.Fatalf("the sync backup password is missing: %v", err)
	}
	document.Format = maxBackupFormat
	// A record kind and fields this app does not model must travel intact.
	stamp, err := app.data.config.NextStamp()
	if err != nil {
		t.Fatal(err)
	}
	future := configstore.Record{
		Kind: "workspace-template", ID: "future", Stamp: stamp,
		Payload: []byte(`{"d":{"new_field":true}}`),
	}
	document.Records = append(document.Records, future)
	sealed, err := backup.Seal(document, password)
	if err != nil {
		t.Fatal(err)
	}
	cloud.mu.Lock()
	cloud.data, cloud.tag = sealed, "tag-newer"
	cloud.mu.Unlock()

	// The ordinary sync takes the document in.
	startSync(t, app)
	if hostCount(t, app) != 1 {
		t.Fatalf("the sync did not apply the newer document: %d hosts", hostCount(t, app))
	}

	// Restore opens it as well.
	if restored := callApp(t, app, "sync.restore", ""); !restored.OK {
		t.Fatalf("restore of a newer-era backup failed: %+v", restored.Error)
	}
	records, err := app.data.config.Records()
	if err != nil {
		t.Fatal(err)
	}
	carried := false
	for _, record := range records {
		if record.Kind == future.Kind && record.ID == future.ID {
			carried = string(record.Payload) == string(future.Payload)
		}
	}
	if !carried {
		t.Fatal("a record this app does not model was not carried through")
	}

	// A manual restore does not stop at the sync's ceiling.
	document.Format = maxBackupFormat + 100
	sealed, err = backup.Seal(document, password)
	if err != nil {
		t.Fatal(err)
	}
	cloud.mu.Lock()
	cloud.data, cloud.tag = sealed, "tag-newest"
	cloud.mu.Unlock()
	if restored := callApp(t, app, "sync.restore", ""); !restored.OK {
		t.Fatalf("restore refused a backup from an unknown epoch: %+v", restored.Error)
	}
	if hostCount(t, app) != 1 {
		t.Fatalf("the restore left %d hosts", hostCount(t, app))
	}

	// The background sync still stops at the newest epoch it knows how to
	// merge, so an unseen structure is never taken in without the user.
	if _, err := app.sync.mergeRemote(backup.Document{Format: maxBackupFormat + 100}, false); !errors.Is(err, backup.ErrTooNew) {
		t.Fatalf("the automatic sync took an unknown epoch: %v", err)
	}
}

func TestSyncRestoreReplacesLocalData(t *testing.T) {
	cloud := &fakeCloud{}
	first := syncTestApp(t, cloud)
	second := syncTestApp(t, cloud)

	if saved := callApp(t, first, "hosts.save", `{"label":"server","address":"server.example.com","user":"root"}`); !saved.OK {
		t.Fatal(saved.Error)
	}
	startSync(t, first)

	if local := callApp(t, second, "hosts.save", `{"label":"local","address":"local.example.com","user":"root"}`); !local.OK {
		t.Fatal(local.Error)
	}
	restored := callApp(t, second, "sync.restore", "")
	if !restored.OK {
		t.Fatalf("sync.restore failed: %+v", restored.Error)
	}
	listed := callApp(t, second, "hosts.list", "")
	var list struct {
		Hosts []Host `json:"hosts"`
	}
	if err := json.Unmarshal(listed.Result, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Hosts) != 1 || list.Hosts[0].Label != "server" {
		t.Fatalf("after restore hosts = %+v", list.Hosts)
	}
}
