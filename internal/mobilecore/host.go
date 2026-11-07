package mobilecore

import (
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"motty/internal/configstore"
	"motty/internal/sshconfig"
)

// Authentication values a host may carry. Empty means automatic: try the
// imported or default keys, then the password.
const (
	AuthAutomatic = ""
	AuthPassword  = "password"
	AuthKey       = "key"
	AuthAgent     = "agent"
)

// Protocols a saved host can speak. SSH opens a terminal; VNC and RDP open a
// remote desktop view.
const (
	ProtocolSSH = "ssh"
	ProtocolVNC = "vnc"
	ProtocolRDP = "rdp"
)

// defaultPorts are the ports a new host of each protocol starts on.
var defaultPorts = map[string]uint16{
	ProtocolSSH: 22,
	ProtocolVNC: 5900,
	ProtocolRDP: 3389,
}

// Proxy kinds a saved host can dial its destination through. They are the
// desktop's strings, so a host written there means the same thing here.
const (
	ProxySOCKS5 = "socks5"
	ProxyHTTP   = "http"
)

// HostProxy is a host's outbound proxy, without its password: the password
// lives in the vault (see hostProxyPasswordSecret) and travels with a backup or
// a sync.
type HostProxy struct {
	Kind    string `json:"kind"`
	Address string `json:"address,omitempty"`
	User    string `json:"user,omitempty"`
	Bypass  string `json:"bypass,omitempty"`
}

// Host is one saved destination. The fields mirror the desktop catalog where
// they exist, so a later import/export can share a file format.
type Host struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Group    string   `json:"group,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	Icon     string   `json:"icon,omitempty"`
	Color    string   `json:"color,omitempty"`
	Address  string   `json:"address"`
	User     string   `json:"user"`
	Port     uint16   `json:"port"`
	Protocol string   `json:"protocol,omitempty"`
	Auth     string   `json:"auth,omitempty"`
	Identity string   `json:"identity,omitempty"`
	// ProxyJump is a comma-separated chain of user@host:port endpoints.
	ProxyJump    string `json:"proxyJump,omitempty"`
	LoginCommand string `json:"loginCommand,omitempty"`
	NoHistory    bool   `json:"noHistory,omitempty"`
	// ViaSSH is the id of a saved SSH host whose open session carries a VNC or
	// RDP connection to this host. Empty dials the server directly.
	ViaSSH string `json:"viaSsh,omitempty"`
	// Clipboard shares the clipboard with the remote desktop in both
	// directions. Nil means on for VNC and RDP; SSH ignores it.
	Clipboard *bool `json:"clipboard,omitempty"`
	// Console asks RDP for the administrative console session.
	Console bool `json:"console,omitempty"`
	// Scale is the display scale an RDP server should draw at (0 and 1 are
	// both 100%).
	Scale float64 `json:"scale,omitempty"`
	// Proxy is the outbound proxy this host's traffic leaves through, nil for a
	// direct connection. There is no global proxy on a phone, so a host either
	// carries one or goes direct.
	Proxy     *HostProxy `json:"proxy,omitempty"`
	CreatedAt int64      `json:"createdAt,omitempty"`
	UpdatedAt int64      `json:"updatedAt,omitempty"`

	// The fields below are the desktop catalog's; this app keeps them so a host
	// written there survives a round trip through this one.
	System        string `json:"system,omitempty"`
	Line          string `json:"line,omitempty"`
	Flow          string `json:"flow,omitempty"`
	Algorithms    string `json:"algorithms,omitempty"`
	AlgorithmList string `json:"algorithmList,omitempty"`
	ForwardX11    bool   `json:"forwardX11,omitempty"`
	AgentForward  bool   `json:"agentForward,omitempty"`
	Transfer      string `json:"transfer,omitempty"`
	Chromeless    bool   `json:"chromeless,omitempty"`
}

// IsSSH reports whether this host opens a terminal (which is also what a host
// saved before protocols existed becomes).
func (h Host) IsSSH() bool {
	return h.Protocol == "" || h.Protocol == ProtocolSSH
}

// IsDesktop reports whether this host opens a remote desktop.
func (h Host) IsDesktop() bool {
	return h.Protocol == ProtocolVNC || h.Protocol == ProtocolRDP
}

// ClipboardEnabled reports whether the clipboard crosses for a desktop host.
func (h Host) ClipboardEnabled() bool {
	if h.Clipboard == nil {
		return h.IsDesktop()
	}
	return *h.Clipboard
}

// Normalized returns the host with defaults applied and fields trimmed.
func (h Host) Normalized() Host {
	h.Label = strings.TrimSpace(h.Label)
	h.Group = strings.Trim(strings.TrimSpace(h.Group), "/")
	h.Address = strings.TrimSpace(h.Address)
	h.User = strings.TrimSpace(h.User)
	h.Identity = strings.TrimSpace(h.Identity)
	h.ProxyJump = strings.TrimSpace(h.ProxyJump)
	h.ViaSSH = strings.TrimSpace(h.ViaSSH)
	h.Protocol = strings.ToLower(strings.TrimSpace(h.Protocol))
	if h.Protocol == "" {
		h.Protocol = ProtocolSSH
	}
	if h.Port == 0 {
		h.Port = defaultPorts[h.Protocol]
		if h.Port == 0 {
			h.Port = 22
		}
	}
	if h.Label == "" {
		h.Label = h.Address
	}
	if h.Tags == nil {
		h.Tags = []string{}
	}
	h.Proxy = normalizeHostProxy(h.Proxy)
	if h.IsDesktop() {
		if h.Clipboard == nil {
			enabled := true
			h.Clipboard = &enabled
		}
		if h.Scale < 0.5 || h.Scale > 4 {
			h.Scale = 0
		}
	} else {
		// SSH keeps the field out of files it never used before.
		h.Clipboard = nil
		h.Console = false
		h.Scale = 0
	}
	return h
}

// Validate reports what a caller must fix before a host is saved or dialed.
func (h Host) Validate() error {
	switch {
	case h.Label == "":
		return errorf(CodeBadRequest, "a label is required")
	case h.Address == "":
		return errorf(CodeBadRequest, "an address is required")
	case h.Port == 0:
		return errorf(CodeBadRequest, "the port must be between 1 and 65535")
	}
	switch h.Protocol {
	case "", ProtocolSSH:
		if h.User == "" {
			return errorf(CodeBadRequest, "an account name is required")
		}
		switch h.Auth {
		case AuthAutomatic, AuthPassword, AuthKey, AuthAgent:
		default:
			return errorf(CodeBadRequest, "unknown authentication mode %q", h.Auth)
		}
	case ProtocolVNC, ProtocolRDP:
		// VNC has no account; RDP accepts an empty one and asks on connect.
	default:
		return errorf(CodeBadRequest, "unknown protocol %q", h.Protocol)
	}
	if h.Proxy != nil {
		switch h.Proxy.Kind {
		case ProxySOCKS5, ProxyHTTP:
		default:
			return errorf(CodeBadRequest, "unknown proxy kind %q", h.Proxy.Kind)
		}
		if h.Proxy.Address == "" {
			return errorf(CodeBadRequest, "a proxy address is required")
		}
	}
	return nil
}

// normalizeHostProxy trims a saved proxy and drops one of an unknown kind. An
// empty kind means no proxy at all. A missing address is kept so Validate can
// report it instead of the proxy silently disappearing.
func normalizeHostProxy(proxy *HostProxy) *HostProxy {
	if proxy == nil {
		return nil
	}
	copy := *proxy
	copy.Kind = strings.ToLower(strings.TrimSpace(copy.Kind))
	copy.Address = strings.TrimSpace(copy.Address)
	copy.User = strings.TrimSpace(copy.User)
	copy.Bypass = strings.TrimSpace(copy.Bypass)
	switch copy.Kind {
	case ProxySOCKS5, ProxyHTTP:
		return &copy
	}
	return nil
}

// hostStore keeps the catalog as configstore records, the same way the desktop
// application does, so a host added here and one added there merge into one.
type hostStore struct {
	mu       sync.RWMutex
	data     *dataStore
	hosts    map[string]Host
	ords     map[string]int64
	payloads map[string][]byte
}

// reload replaces the in-memory catalog with what the store holds now, after a
// sync or a restore changed it.
func (s *hostStore) reload() error {
	entries, _, err := s.data.config.Load()
	if err != nil {
		return errorf(CodeInternal, "cannot read the host catalog: %v", err)
	}
	hosts := map[string]Host{}
	ords := map[string]int64{}
	payloads := map[string][]byte{}
	for _, entry := range entries {
		if entry.Kind != kindHostRecord {
			continue
		}
		host, ord, ok := decodeHostRecord(entry.ID, entry.Payload)
		if !ok {
			continue
		}
		hosts[host.ID] = host
		ords[host.ID] = ord
		payloads[host.ID] = entry.Payload
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hosts, s.ords, s.payloads = hosts, ords, payloads
	return nil
}

func openHostStore(data *dataStore) (*hostStore, error) {
	store := &hostStore{
		data:     data,
		hosts:    map[string]Host{},
		ords:     map[string]int64{},
		payloads: map[string][]byte{},
	}
	entries, _, err := data.config.Load()
	if err != nil {
		return nil, errorf(CodeInternal, "cannot read the host catalog: %v", err)
	}
	for _, entry := range entries {
		if entry.Kind != kindHostRecord {
			continue
		}
		host, ord, ok := decodeHostRecord(entry.ID, entry.Payload)
		if !ok {
			continue // a record this build cannot read is left alone
		}
		store.hosts[host.ID] = host
		store.ords[host.ID] = ord
		store.payloads[host.ID] = entry.Payload
	}
	return store, nil
}

// list returns a copy of every host, grouped by group path and then label.
func (s *hostStore) list() []Host {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Host, 0, len(s.hosts))
	for _, host := range s.hosts {
		out = append(out, host)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return strings.ToLower(out[i].Label) < strings.ToLower(out[j].Label)
	})
	return out
}

func (s *hostStore) get(id string) (Host, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	host, ok := s.hosts[id]
	return host, ok
}

// save inserts or replaces a host and returns the stored copy. A new host is
// named after where it connects, like the desktop catalog names it; a host
// being edited keeps its id.
func (s *hostStore) save(host Host) (Host, error) {
	host = host.Normalized()
	if err := host.Validate(); err != nil {
		return Host{}, err
	}
	now := time.Now().Unix()
	s.mu.Lock()
	defer s.mu.Unlock()
	if host.ID == "" {
		used := make(map[string]bool, len(s.hosts))
		for id := range s.hosts {
			used[id] = true
		}
		id := catalogIDFor(host)
		if used[id] {
			id = uniqueHostID(id, used)
		}
		host.ID = id
		host.CreatedAt = now
	} else if previous, ok := s.hosts[host.ID]; ok {
		host.CreatedAt = previous.CreatedAt
	}
	host.UpdatedAt = now
	ord, exists := s.ords[host.ID]
	if !exists {
		ord = s.nextOrdLocked()
	}
	payload, err := encodeHostRecord(s.payloads[host.ID], host, ord)
	if err != nil {
		return Host{}, errorf(CodeInternal, "cannot encode the host: %v", err)
	}
	_, err = s.data.config.Commit(configstore.Change{
		Writer: "motty-ios",
		Put:    []configstore.Entry{{Kind: kindHostRecord, ID: host.ID, Payload: payload}},
	})
	if err != nil {
		return Host{}, errorf(CodeInternal, "cannot save the host: %v", err)
	}
	s.hosts[host.ID] = host
	s.ords[host.ID] = ord
	s.payloads[host.ID] = payload
	return host, nil
}

func (s *hostStore) delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.hosts[id]; !ok {
		return errorf(CodeNotFound, "no host with id %q", id)
	}
	_, err := s.data.config.Commit(configstore.Change{
		Writer: "motty-ios",
		Delete: []configstore.Key{{Kind: kindHostRecord, ID: id}},
	})
	if err != nil {
		return errorf(CodeInternal, "cannot delete the host: %v", err)
	}
	delete(s.hosts, id)
	delete(s.ords, id)
	delete(s.payloads, id)
	return nil
}

func (s *hostStore) nextOrdLocked() int64 {
	var highest int64
	for _, ord := range s.ords {
		if ord > highest {
			highest = ord
		}
	}
	return highest + 1
}

// importSSHConfig reads an OpenSSH config file and adds every concrete alias.
// An alias that already exists (same address, user and port) is skipped.
func (s *hostStore) importSSHConfig(path string) (imported []Host, warnings []sshconfig.Warning, err error) {
	result, err := sshconfig.ParseFile(path)
	if err != nil {
		return nil, nil, errorf(CodeBadRequest, "cannot read the SSH config: %v", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]bool{}
	used := make(map[string]bool, len(s.hosts))
	for id, existing := range s.hosts {
		seen[importKey(existing.Address, existing.User, existing.Port)] = true
		used[id] = true
	}
	now := time.Now().Unix()
	nextOrd := s.nextOrdLocked()
	var put []configstore.Entry
	var added []Host
	for _, entry := range result.Hosts {
		address := entry.HostName
		if address == "" {
			address = entry.Alias
		}
		user := entry.User
		if user == "" {
			user = "root"
		}
		// Missing ports become 22 in Normalized, so dedupe against the same
		// value or a second import would add the host again.
		port := entry.Port
		if port == 0 {
			port = 22
		}
		key := importKey(address, user, port)
		if seen[key] {
			continue
		}
		seen[key] = true
		host := Host{
			Label:     entry.Alias,
			Address:   address,
			User:      user,
			Port:      port,
			Identity:  entry.IdentityFile,
			ProxyJump: formatProxyJump(entry.ProxyJump),
			CreatedAt: now,
			UpdatedAt: now,
		}.Normalized()
		id := catalogIDFor(host)
		if used[id] {
			id = uniqueHostID(id, used)
		}
		used[id] = true
		host.ID = id
		ord := nextOrd
		nextOrd++
		payload, err := encodeHostRecord(nil, host, ord)
		if err != nil {
			return nil, result.Warnings, errorf(CodeInternal, "cannot encode the host: %v", err)
		}
		s.ords[host.ID] = ord
		s.payloads[host.ID] = payload
		put = append(put, configstore.Entry{Kind: kindHostRecord, ID: host.ID, Payload: payload})
		added = append(added, host)
	}
	if len(put) > 0 {
		if _, err := s.data.config.Commit(configstore.Change{Writer: "motty-ios-import", Put: put}); err != nil {
			for _, host := range added {
				delete(s.ords, host.ID)
				delete(s.payloads, host.ID)
			}
			return nil, result.Warnings, errorf(CodeInternal, "cannot import the SSH config: %v", err)
		}
	}
	for _, host := range added {
		s.hosts[host.ID] = host
		imported = append(imported, host)
	}
	return imported, result.Warnings, nil
}

func importKey(address, user string, port uint16) string {
	return strings.ToLower(address) + "\x00" + user + "\x00" + strconv.FormatUint(uint64(port), 10)
}

func formatProxyJump(jumps []sshconfig.JumpHost) string {
	parts := make([]string, 0, len(jumps))
	for _, jump := range jumps {
		part := jump.Host
		if jump.User != "" {
			part = jump.User + "@" + part
		}
		if jump.Port != 0 && jump.Port != 22 {
			part += ":" + strconv.FormatUint(uint64(jump.Port), 10)
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, ",")
}

// importWarning is the JSON shape of an sshconfig.Warning. The parser's own
// type has Go-style field names; the front end expects lower camel case.
type importWarning struct {
	Code      string `json:"code"`
	Path      string `json:"path,omitempty"`
	Line      int    `json:"line,omitempty"`
	Directive string `json:"directive,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

func exportWarnings(warnings []sshconfig.Warning) []importWarning {
	out := make([]importWarning, len(warnings))
	for i, warning := range warnings {
		out[i] = importWarning{
			Code:      string(warning.Code),
			Path:      warning.Path,
			Line:      warning.Line,
			Directive: warning.Directive,
			Detail:    warning.Detail,
		}
	}
	return out
}
