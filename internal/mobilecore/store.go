package mobilecore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"motty/internal/configstore"
	"motty/internal/vault"
)

// The shared data lives in the desktop application's own formats, so a backup
// made here opens there and a sync merges both sides without translation:
//
//   - config.db is a configstore of records (format 23), one record per host;
//   - vault.json keeps the imported keys and the assistant's API key, sealed;
//   - known_hosts is the OpenSSH file the SSH client adds to.
//
// hosts.json, keys/ and the API key in ai.json are what earlier builds of this
// app wrote. They are read once, imported, and left in place as a backup.

// configFormat is the configstore epoch the desktop application reads and
// writes. A file from a newer build is refused instead of being damaged.
const configFormat = 23

// kinds of the records this app writes. They match the desktop's.
const (
	kindHostRecord = "host"
)

// local keys that mark a one-time import of an older file as done. They belong
// to this device and never travel, like the other configstore local values.
const (
	localKeysMigrated  = "mobile.keys_migrated"
	localHostsMigrated = "mobile.hosts_migrated"
)

// dataStore owns the shared files. One per app; every other store borrows it.
type dataStore struct {
	dir    string
	config *configstore.Store
	vault  *vault.Vault

	mu sync.Mutex
}

func openDataStore(dir string) (*dataStore, error) {
	config, err := configstore.Open(filepath.Join(dir, "config.db"), configFormat)
	if err != nil {
		return nil, errorf(CodeInternal, "cannot open the shared configuration: %v", err)
	}
	vaultFile, err := vault.Open(filepath.Join(dir, "vault.json"), platformKeystore(dir))
	if err != nil {
		config.Close()
		return nil, errorf(CodeInternal, "cannot open the saved secrets: %v", err)
	}
	if err := vaultFile.Prepare(); err != nil {
		config.Close()
		return nil, errorf(CodeInternal, "cannot open the saved secrets: %v", err)
	}
	// Saved secrets are versioned by the same clock as the records, so a
	// password or a key changed here is comparable with the one on another
	// device when the two are merged.
	vaultFile.SetStamper(func() (string, error) {
		stamp, err := config.NextStamp()
		return string(stamp), err
	})
	return &dataStore{dir: dir, config: config, vault: vaultFile}, nil
}

func (d *dataStore) close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.config != nil {
		d.config.Close()
		d.config = nil
	}
}

// secretGet reads one vault entry.
func (d *dataStore) secretGet(name string) (string, bool, error) {
	return d.vault.Get(name)
}

func (d *dataStore) secretSet(name, value string) error {
	return d.vault.Set(name, value)
}

func (d *dataStore) secretDelete(name string) error {
	return d.vault.Delete(name)
}

// hostRecordPayload is the desktop's record envelope: the item's place in its
// list and the item itself.
type hostRecordPayload struct {
	Ord  int64           `json:"o,omitempty"`
	Data json.RawMessage `json:"d"`
}

// desktopHostData is the desktop's savedHost for the fields this app knows.
// Unknown fields are preserved by patching the stored object rather than
// replacing it.
type desktopHostData struct {
	Color   string   `json:"color,omitempty"`
	ID      string   `json:"id"`
	Label   string   `json:"label"`
	Group   string   `json:"group,omitempty"`
	Tags    []string `json:"tags,omitempty"`
	Icon    string   `json:"icon,omitempty"`
	Address string   `json:"address"`
	User    string   `json:"user,omitempty"`
	Port    uint16   `json:"port,omitempty"`
	System  string   `json:"system,omitempty"`

	Protocol       string          `json:"protocol,omitempty"`
	Line           string          `json:"line,omitempty"`
	Flow           string          `json:"flow,omitempty"`
	Proxy          json.RawMessage `json:"proxy,omitempty"`
	Algorithms     string          `json:"algorithms,omitempty"`
	AlgorithmList  string          `json:"algorithm_list,omitempty"`
	Authentication string          `json:"authentication,omitempty"`
	Identity       string          `json:"identity,omitempty"`
	ForwardX11     bool            `json:"forward_x11,omitempty"`
	AgentForward   bool            `json:"agent_forward,omitempty"`
	NoHistory      bool            `json:"no_history,omitempty"`
	LoginCommand   string          `json:"login_command,omitempty"`
	Transfer       string          `json:"transfer,omitempty"`
	ViaSSH         bool            `json:"via_ssh,omitempty"`
	Clipboard      bool            `json:"clipboard,omitempty"`
	Console        bool            `json:"console,omitempty"`
	Chromeless     bool            `json:"chromeless,omitempty"`

	// Mobile is what the desktop does not model: its own app writes this
	// object and ignores it. A desktop that re-saves the host drops it; every
	// field in it has a desktop approximation above.
	Mobile *mobileHostData `json:"mobile,omitempty"`
}

type mobileHostData struct {
	ProxyJump string  `json:"proxy_jump,omitempty"`
	ViaSSH    string  `json:"via_ssh_host,omitempty"`
	Scale     float64 `json:"scale,omitempty"`
	Clipboard *bool   `json:"clipboard,omitempty"`
	CreatedAt int64   `json:"created_at,omitempty"`
	UpdatedAt int64   `json:"updated_at,omitempty"`
}

// catalogIDFor names a host by where it connects, using the desktop's formula,
// so both applications give the same server the same record id.
func catalogIDFor(host Host) string {
	source := strings.ToLower(strings.TrimSpace(host.Address)) + "\x00" +
		strings.TrimSpace(host.User) + "\x00" + fmt.Sprint(host.Port)
	if host.Protocol != "" && host.Protocol != ProtocolSSH {
		source += "\x00" + host.Protocol
	}
	digest := sha256.Sum256([]byte(source))
	return "host_" + hex.EncodeToString(digest[:12])
}

// decodeHostRecord reads one stored host. Ok is false for a record whose
// payload is not a host this build understands; such a record is left alone.
func decodeHostRecord(id string, payload []byte) (host Host, ord int64, ok bool) {
	var envelope hostRecordPayload
	if err := json.Unmarshal(payload, &envelope); err != nil || len(envelope.Data) == 0 {
		return Host{}, 0, false
	}
	var saved desktopHostData
	if err := json.Unmarshal(envelope.Data, &saved); err != nil {
		return Host{}, 0, false
	}
	host = Host{
		ID:            id,
		Label:         saved.Label,
		Group:         saved.Group,
		Tags:          saved.Tags,
		Icon:          saved.Icon,
		Color:         saved.Color,
		Address:       saved.Address,
		User:          saved.User,
		Port:          saved.Port,
		System:        saved.System,
		Protocol:      saved.Protocol,
		Line:          saved.Line,
		Flow:          saved.Flow,
		Algorithms:    saved.Algorithms,
		AlgorithmList: saved.AlgorithmList,
		Auth:          saved.Authentication,
		Identity:      saved.Identity,
		ForwardX11:    saved.ForwardX11,
		AgentForward:  saved.AgentForward,
		NoHistory:     saved.NoHistory,
		LoginCommand:  saved.LoginCommand,
		Transfer:      saved.Transfer,
		Console:       saved.Console,
		Chromeless:    saved.Chromeless,
	}
	if host.Protocol == "" {
		host.Protocol = ProtocolSSH
	}
	if host.IsDesktop() {
		// The desktop writes false (absent) for its own default of no shared
		// clipboard. The mobile object says what this app's user chose.
		share := saved.Clipboard
		if saved.Mobile != nil && saved.Mobile.Clipboard != nil {
			share = *saved.Mobile.Clipboard
		}
		host.Clipboard = &share
	}
	if saved.ViaSSH {
		if saved.Mobile != nil && saved.Mobile.ViaSSH != "" {
			host.ViaSSH = saved.Mobile.ViaSSH
		}
	}
	if saved.Mobile != nil {
		host.ProxyJump = saved.Mobile.ProxyJump
		host.Scale = saved.Mobile.Scale
		host.CreatedAt = saved.Mobile.CreatedAt
		host.UpdatedAt = saved.Mobile.UpdatedAt
	}
	host = host.Normalized()
	if host.Label == "" || host.Address == "" {
		return Host{}, 0, false
	}
	return host, envelope.Ord, true
}

// encodeHostRecord turns a host into a record payload, patching whatever the
// record already held so fields written by a newer desktop build survive.
func encodeHostRecord(existing []byte, host Host, ord int64) ([]byte, error) {
	data := map[string]json.RawMessage{}
	var previous hostRecordPayload
	if len(existing) > 0 && json.Unmarshal(existing, &previous) == nil && len(previous.Data) > 0 {
		_ = json.Unmarshal(previous.Data, &data)
	}
	setString := func(key, value string, keepEmpty bool) {
		if value == "" && !keepEmpty {
			delete(data, key)
			return
		}
		encoded, _ := json.Marshal(value)
		data[key] = encoded
	}
	setBool := func(key string, value bool) {
		if !value {
			delete(data, key)
			return
		}
		data[key] = json.RawMessage("true")
	}

	setString("id", host.ID, true)
	setString("label", host.Label, true)
	setString("group", host.Group, false)
	setString("icon", host.Icon, false)
	setString("color", host.Color, false)
	setString("address", host.Address, true)
	setString("user", host.User, false)
	setString("system", host.System, false)
	if host.Port != 0 {
		encoded, _ := json.Marshal(host.Port)
		data["port"] = encoded
	} else {
		delete(data, "port")
	}
	if len(host.Tags) > 0 {
		encoded, _ := json.Marshal(host.Tags)
		data["tags"] = encoded
	} else {
		delete(data, "tags")
	}
	if host.IsSSH() {
		delete(data, "protocol")
	} else {
		setString("protocol", host.Protocol, false)
	}
	setString("line", host.Line, false)
	setString("flow", host.Flow, false)
	setString("algorithms", host.Algorithms, false)
	setString("algorithm_list", host.AlgorithmList, false)
	setString("authentication", host.Auth, false)
	setString("identity", host.Identity, false)
	setString("login_command", host.LoginCommand, false)
	setString("transfer", host.Transfer, false)
	setBool("forward_x11", host.ForwardX11)
	setBool("agent_forward", host.AgentForward)
	setBool("no_history", host.NoHistory)
	setBool("console", host.Console)
	setBool("chromeless", host.Chromeless)
	if host.IsDesktop() {
		setBool("via_ssh", host.ViaSSH != "")
		setBool("clipboard", host.ClipboardEnabled())
	} else {
		setBool("via_ssh", false)
		setBool("clipboard", false)
	}

	mobile := map[string]json.RawMessage{}
	if raw, ok := data["mobile"]; ok {
		_ = json.Unmarshal(raw, &mobile)
	}
	setMobileString := func(key, value string) {
		if value == "" {
			delete(mobile, key)
			return
		}
		encoded, _ := json.Marshal(value)
		mobile[key] = encoded
	}
	setMobileString("proxy_jump", host.ProxyJump)
	setMobileString("via_ssh_host", host.ViaSSH)
	if host.Scale != 0 {
		encoded, _ := json.Marshal(host.Scale)
		mobile["scale"] = encoded
	} else {
		delete(mobile, "scale")
	}
	if host.IsDesktop() {
		encoded, _ := json.Marshal(host.ClipboardEnabled())
		mobile["clipboard"] = encoded
	} else {
		delete(mobile, "clipboard")
	}
	if host.CreatedAt != 0 {
		encoded, _ := json.Marshal(host.CreatedAt)
		mobile["created_at"] = encoded
	}
	if host.UpdatedAt != 0 {
		encoded, _ := json.Marshal(host.UpdatedAt)
		mobile["updated_at"] = encoded
	}
	if len(mobile) > 0 {
		encoded, err := json.Marshal(mobile)
		if err != nil {
			return nil, err
		}
		data["mobile"] = encoded
	} else {
		delete(data, "mobile")
	}

	encoded, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	return json.Marshal(hostRecordPayload{Ord: ord, Data: encoded})
}

// migrateLegacyHosts imports hosts.json, which builds before the configstore
// wrote. It is safe to run twice: the local marker says the work is done, and
// an interrupted first run continues with the hosts that are not stored yet.
func migrateLegacyHosts(data *dataStore, identityRefs map[string]string) (int, error) {
	_, local, err := data.config.Load()
	if err != nil {
		return 0, err
	}
	if string(local[localHostsMigrated]) == "1" {
		return 0, nil
	}
	path := filepath.Join(data.dir, "hosts.json")
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var legacy []Host
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return 0, errorf(CodeInternal, "the old host catalog is damaged: %v", err)
	}
	entries, _, err := data.config.Load()
	if err != nil {
		return 0, err
	}
	stored := map[string]bool{}
	used := map[string]bool{}
	for _, entry := range entries {
		if entry.Kind == kindHostRecord {
			stored[entry.ID] = true
			used[entry.ID] = true
		}
	}
	var put []configstore.Entry
	imported := 0
	for index, host := range legacy {
		host = host.Normalized()
		if host.Address == "" {
			continue
		}
		if ref, ok := identityRefs[host.Identity]; ok {
			host.Identity = ref
		} else if host.Identity != "" {
			// Some early catalogs kept the path relative to the data directory
			// or just the key's file name.
			for _, candidate := range []string{
				filepath.Join(data.dir, filepath.FromSlash(host.Identity)),
				filepath.Join(data.dir, "keys", filepath.Base(host.Identity)),
			} {
				if ref, ok := identityRefs[candidate]; ok {
					host.Identity = ref
					break
				}
			}
		}
		id := catalogIDFor(host)
		if used[id] {
			id = uniqueHostID(id, used)
		}
		used[id] = true
		host.ID = id
		if host.CreatedAt == 0 {
			host.CreatedAt = host.UpdatedAt
		}
		payload, err := encodeHostRecord(nil, host, int64(index))
		if err != nil {
			return imported, err
		}
		if stored[id] {
			continue
		}
		put = append(put, configstore.Entry{Kind: kindHostRecord, ID: id, Payload: payload})
		imported++
	}
	change := configstore.Change{Writer: "motty-ios-migrate", Put: put,
		Local: map[string][]byte{localHostsMigrated: []byte("1")}}
	if _, err := data.config.Commit(change); err != nil {
		return 0, errorf(CodeInternal, "cannot import the old host catalog: %v", err)
	}
	return imported, nil
}

// uniqueHostID appends a suffix to an id already in use, as the desktop does
// when two hosts name the same server.
func uniqueHostID(base string, used map[string]bool) string {
	for suffix := 2; ; suffix++ {
		candidate := fmt.Sprintf("%s_%d", base, suffix)
		if !used[candidate] {
			return candidate
		}
	}
}

// configstoreChangeLocal writes one device-local marker value.
func configstoreChangeLocal(key string) configstore.Change {
	return configstore.Change{Writer: "motty-ios", Local: map[string][]byte{key: []byte("1")}}
}
