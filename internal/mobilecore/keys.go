package mobilecore

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/crypto/ssh"

	"motty/internal/sshconn"
)

// keyInfo is the front end's view of one imported private key. Path is what a
// host's Identity field stores: an sshkey: reference into the vault.
type keyInfo struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	PublicKey string `json:"publicKey,omitempty"`
	Encrypted bool   `json:"encrypted"`
	CreatedAt int64  `json:"createdAt"`
}

// storedKey is what the vault holds under an sshkey: reference, byte for byte
// the shape the desktop application stores.
type storedKey struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

// keyStore keeps imported private keys in the vault, where they travel with a
// backup and a sync. Keys an earlier build wrote to <dataDir>/keys are read from
// there, and imported into the vault on first start.
type keyStore struct {
	dir  string
	data *dataStore
}

func openKeyStore(data *dataStore) (*keyStore, error) {
	dir := filepath.Join(data.dir, "keys")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, errorf(CodeInternal, "cannot create the key directory: %v", err)
	}
	return &keyStore{dir: dir, data: data}, nil
}

// list returns imported keys sorted by name. A vault key hides a legacy file of
// the same name.
func (s *keyStore) list() []keyInfo {
	out := s.vaultInfos()
	seen := make(map[string]bool, len(out))
	for _, info := range out {
		seen[info.Name] = true
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return out
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasPrefix(name, ".") || seen[name] {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, name))
		if err != nil {
			continue
		}
		info, err := inspectKey(name, filepath.Join(s.dir, name), data)
		if err != nil {
			continue
		}
		if stat, err := entry.Info(); err == nil {
			info.CreatedAt = stat.ModTime().Unix()
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// vaultInfos lists the keys the vault holds.
func (s *keyStore) vaultInfos() []keyInfo {
	names, err := s.data.vault.Names()
	if err != nil {
		return []keyInfo{}
	}
	out := make([]keyInfo, 0, len(names))
	for _, ref := range names {
		if !sshconn.IsStoredKey(ref) {
			continue
		}
		value, found, err := s.data.secretGet(ref)
		if err != nil || !found {
			continue
		}
		var stored storedKey
		if err := json.Unmarshal([]byte(value), &stored); err != nil || stored.Key == "" {
			continue
		}
		info, err := inspectKey(stored.Name, ref, []byte(stored.Key))
		if err != nil {
			continue
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// inspectKey describes one key's public half without keeping the private half.
func inspectKey(name, path string, data []byte) (keyInfo, error) {
	info := keyInfo{Name: name, Path: path}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		var missing *ssh.PassphraseMissingError
		if errors.As(err, &missing) {
			info.Encrypted = true
			info.PublicKey = marshalPublicKey(missing.PublicKey)
			return info, nil
		}
		return keyInfo{}, errorf(CodeInternal, "the key is damaged: %v", err)
	}
	info.PublicKey = marshalPublicKey(signer.PublicKey())
	return info, nil
}

func marshalPublicKey(key ssh.PublicKey) string {
	if key == nil {
		return ""
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
}

// importKey validates and stores one private key in the vault. An existing name
// is a conflict unless overwrite is set, so a stray import cannot replace a key
// a host still points at.
func (s *keyStore) importKey(name string, data []byte, overwrite bool) (keyInfo, error) {
	name = strings.TrimSpace(name)
	if err := validKeyName(name); err != nil {
		return keyInfo{}, err
	}
	if len(data) == 0 {
		return keyInfo{}, errorf(CodeBadRequest, "the key file is empty")
	}
	if len(data) > 1<<20 {
		return keyInfo{}, errorf(CodeBadRequest, "the key file is larger than 1 MiB")
	}
	// Refuse anything the SSH library cannot read, unless it merely lacks the
	// passphrase; that prompt happens when the key is used.
	if _, err := ssh.ParsePrivateKey(data); err != nil {
		var missing *ssh.PassphraseMissingError
		if !errors.As(err, &missing) {
			return keyInfo{}, errorf(CodeBadRequest, "not an SSH private key: %v", err)
		}
	}

	existingRef, _ := s.vaultRefForName(name)
	legacyPath := filepath.Join(s.dir, name)
	_, legacyErr := os.Stat(legacyPath)
	legacy := legacyErr == nil
	if (existingRef != "" || legacy) && !overwrite {
		return keyInfo{}, errorf(CodeConflict, "a key named %q already exists", name)
	}
	ref, err := newStoredKeyRef()
	if err != nil {
		return keyInfo{}, errorf(CodeInternal, "cannot name the key: %v", err)
	}
	encoded, err := json.Marshal(storedKey{Name: name, Key: string(data)})
	if err != nil {
		return keyInfo{}, errorf(CodeInternal, "cannot encode the key: %v", err)
	}
	if err := s.data.secretSet(ref, string(encoded)); err != nil {
		return keyInfo{}, errorf(CodeInternal, "cannot save the key: %v", err)
	}
	if existingRef != "" && existingRef != ref {
		// Replacing a key under the same name: the old reference must not stay
		// behind, or the list would show the name twice.
		_ = s.data.secretDelete(existingRef)
	}
	info, err := inspectKey(name, ref, data)
	if err != nil {
		return keyInfo{}, err
	}
	return info, nil
}

// delete removes a key by name, from the vault and from the legacy directory.
func (s *keyStore) delete(name string) error {
	if err := validKeyName(name); err != nil {
		return err
	}
	ref, err := s.vaultRefForName(name)
	if err != nil {
		return err
	}
	found := false
	if ref != "" {
		if err := s.data.secretDelete(ref); err != nil {
			return errorf(CodeInternal, "cannot delete the key: %v", err)
		}
		found = true
	}
	path := filepath.Join(s.dir, name)
	if err := os.Remove(path); err == nil {
		found = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return errorf(CodeInternal, "cannot delete the key: %v", err)
	}
	if !found {
		return errorf(CodeNotFound, "no key named %q", name)
	}
	return nil
}

// keyData reads the private key a host's Identity reference names. The SSH
// client calls it while connecting.
func (s *keyStore) keyData(ref string) ([]byte, error) {
	value, found, err := s.data.secretGet(ref)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New("the key is no longer saved in Motty")
	}
	var stored storedKey
	if err := json.Unmarshal([]byte(value), &stored); err != nil || stored.Key == "" {
		return nil, errors.New("the saved key is damaged")
	}
	return []byte(stored.Key), nil
}

// vaultRefForName finds the sshkey: reference a stored key was saved under.
func (s *keyStore) vaultRefForName(name string) (string, error) {
	names, err := s.data.vault.Names()
	if err != nil {
		return "", errorf(CodeInternal, "cannot list the saved keys: %v", err)
	}
	for _, ref := range names {
		if !sshconn.IsStoredKey(ref) {
			continue
		}
		value, found, err := s.data.secretGet(ref)
		if err != nil || !found {
			continue
		}
		var stored storedKey
		if json.Unmarshal([]byte(value), &stored) == nil && stored.Name == name {
			return ref, nil
		}
	}
	return "", nil
}

// migrateLegacyFiles imports the private keys earlier builds wrote as files in
// <dataDir>/keys into the vault, and returns the old path of each one with the
// reference it now lives under. It is idempotent: a name already in the vault
// keeps its reference, and the files are left in place as a backup.
func (s *keyStore) migrateLegacyFiles() (map[string]string, error) {
	refs := map[string]string{}
	byName := map[string]string{}
	names, err := s.data.vault.Names()
	if err != nil {
		return refs, errorf(CodeInternal, "cannot list the saved keys: %v", err)
	}
	for _, ref := range names {
		if !sshconn.IsStoredKey(ref) {
			continue
		}
		value, found, err := s.data.secretGet(ref)
		if err != nil || !found {
			continue
		}
		var stored storedKey
		if json.Unmarshal([]byte(value), &stored) == nil && stored.Name != "" {
			byName[stored.Name] = ref
		}
	}
	_, local, err := s.data.config.Load()
	if err != nil {
		return refs, err
	}
	done := string(local[localKeysMigrated]) == "1"
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return refs, nil
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		path := filepath.Join(s.dir, name)
		if ref, ok := byName[name]; ok {
			refs[path] = ref
			continue
		}
		if done {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil || len(data) == 0 {
			continue
		}
		if _, err := ssh.ParsePrivateKey(data); err != nil {
			var missing *ssh.PassphraseMissingError
			if !errors.As(err, &missing) {
				continue // not a key this app can use; leave it alone
			}
		}
		ref, err := newStoredKeyRef()
		if err != nil {
			return refs, err
		}
		encoded, err := json.Marshal(storedKey{Name: name, Key: string(data)})
		if err != nil {
			continue
		}
		if err := s.data.secretSet(ref, string(encoded)); err != nil {
			return refs, errorf(CodeInternal, "cannot import the old key %q: %v", name, err)
		}
		refs[path] = ref
		byName[name] = ref
	}
	if !done {
		if _, err := s.data.config.Commit(configstoreChangeLocal(localKeysMigrated)); err != nil {
			return refs, err
		}
	}
	return refs, nil
}

func newStoredKeyRef() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return sshconn.StoredKeyPrefix + hex.EncodeToString(random[:]), nil
}

func validKeyName(name string) error {
	if name == "" || name == "." || name == ".." || name != filepath.Base(name) || strings.ContainsAny(name, `/\`) {
		return errorf(CodeBadRequest, "invalid key name %q", name)
	}
	return nil
}
