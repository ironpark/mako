package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/ironpark/mako/internal/keychain"
	"github.com/ironpark/mako/pkg/fsutil"
)

// A plugin's secrets are its secret settings, which the user sets, and
// what it keeps itself through the secrets group of the Host API. They
// are the plugin's alone, on this node alone: kept in the system's
// keychain, a service of each plugin's; never synced, never written to
// the log, never sent to a front-end, which is told only whether a
// secret setting is set; and removed with the plugin.
//
// A node whose system has no keychain keeps them in secrets.json in the
// plugin's data folder, readable by the user alone (0600), and says so in
// Settings ▸ Plugins and `mako plugins`. Secrets kept so before, or a
// secret setting kept in settings.json by an older mako, move into the
// keychain once there is one.
//
// What the keychain holds of a plugin is listed, names alone, in its
// secrets-index.json: the keychains have no listing mako may trust to
// be its own, and a plugin removed has each of its secrets removed by
// name.

const (
	// maxSecret bounds a secret's value: what every system's store takes
	// (Windows' Credential Manager holds 2560 bytes).
	maxSecret = 2048
	// maxSecrets bounds the secrets a plugin keeps through the Host API.
	maxSecrets = 32
	// secretsPerMinute bounds the plugin's calls of the secrets group,
	// each of which may reach the system's keychain.
	secretsPerMinute = 60
)

// secretName is a name a plugin gives a secret of its own.
var secretName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// The kinds of secret, the prefix of each one's name in the vault.
const (
	vaultSetting = "setting:"
	vaultSecret  = "secret:"
)

// vault keeps one plugin's secrets.
type vault struct {
	kc      keychain.Keychain // nil: the file
	service string            // the plugin's in the keychain
	index   string            // secrets-index.json
	file    string            // secrets.json

	mu     sync.Mutex
	loaded bool
	names  map[string]bool   // what the keychain holds, or the file
	values map[string]string // read so far, or the file's whole
}

// keychainService is the service a plugin's secrets are kept under in
// the system's keychain: the plugin's ID and the node's, of its data
// folder, so that two nodes of one user on one machine keep theirs apart.
func keychainService(dataDir, id string) string {
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		abs = dataDir
	}
	sum := sha256.Sum256([]byte(abs))
	return "mako plugin " + id + " (" + hex.EncodeToString(sum[:4]) + ")"
}

func newVault(m *Manager, id string) *vault {
	data := filepath.Join(m.cfg.DataDir, id)
	return &vault{
		kc: m.cfg.Keychain, service: keychainService(m.cfg.DataDir, id),
		index: filepath.Join(data, "secrets-index.json"), file: filepath.Join(data, "secrets.json"),
	}
}

// reset forgets what was read, for a vault whose files were removed.
func (v *vault) reset() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.loaded, v.names, v.values = false, nil, nil
}

// loadLocked reads the index, or the file; and, with a keychain, moves
// into it what a file holds from before, once.
func (v *vault) loadLocked() {
	if v.loaded {
		return
	}
	v.loaded = true
	v.names, v.values = map[string]bool{}, map[string]string{}
	var file map[string]string
	if b, err := os.ReadFile(v.file); err == nil {
		_ = json.Unmarshal(b, &file)
	}
	if v.kc == nil {
		for k, val := range file {
			v.names[k], v.values[k] = true, val
		}
		return
	}
	var names []string
	if b, err := os.ReadFile(v.index); err == nil {
		_ = json.Unmarshal(b, &names)
	}
	for _, n := range names {
		v.names[n] = true
	}
	if len(file) == 0 {
		return
	}
	left := map[string]string{}
	for k, val := range file {
		if err := v.kc.Set(v.service, k, val); err != nil {
			left[k] = val
			continue
		}
		v.names[k], v.values[k] = true, val
	}
	if v.saveIndexLocked() != nil {
		return
	}
	v.writeFileLocked(left)
}

func (v *vault) saveIndexLocked() error {
	names := make([]string, 0, len(v.names))
	for n := range v.names {
		names = append(names, n)
	}
	sort.Strings(names)
	b, _ := json.Marshal(names)
	if err := os.MkdirAll(filepath.Dir(v.index), 0o700); err != nil {
		return err
	}
	return fsutil.Write(v.index, b)
}

// writeFileLocked writes the file's secrets, or removes it with none.
func (v *vault) writeFileLocked(values map[string]string) error {
	if len(values) == 0 {
		if err := os.Remove(v.file); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	b, err := json.Marshal(values)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(v.file), 0o700); err != nil {
		return err
	}
	return fsutil.Write(v.file, b) // 0600
}

// has says the vault holds name, without reading its value.
func (v *vault) has(name string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.loadLocked()
	return v.names[name]
}

// get reads name's value.
func (v *vault) get(name string) (string, bool, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.loadLocked()
	if !v.names[name] {
		return "", false, nil
	}
	if val, ok := v.values[name]; ok {
		return val, true, nil
	}
	val, err := v.kc.Get(v.service, name)
	if errors.Is(err, keychain.ErrNotFound) {
		// Removed from the keychain by hand: the index follows.
		delete(v.names, name)
		_ = v.saveIndexLocked()
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	v.values[name] = val
	return val, true, nil
}

// set writes name's value.
func (v *vault) set(name, value string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.loadLocked()
	if v.kc == nil {
		next := maps.Clone(v.values)
		next[name] = value
		if err := v.writeFileLocked(next); err != nil {
			return err
		}
		v.names[name], v.values[name] = true, value
		return nil
	}
	// The index names it first, so that a secret the keychain holds is
	// never one a removal would not know of.
	had := v.names[name]
	v.names[name] = true
	if err := v.saveIndexLocked(); err != nil {
		v.names[name] = had
		return err
	}
	if err := v.kc.Set(v.service, name, value); err != nil {
		if !had {
			delete(v.names, name)
			_ = v.saveIndexLocked()
		}
		delete(v.values, name)
		return err
	}
	v.values[name] = value
	return nil
}

// del removes name, reporting whether there was one.
func (v *vault) del(name string) (bool, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.loadLocked()
	if !v.names[name] {
		return false, nil
	}
	if v.kc == nil {
		next := maps.Clone(v.values)
		delete(next, name)
		if err := v.writeFileLocked(next); err != nil {
			return false, err
		}
	} else if err := v.kc.Delete(v.service, name); err != nil && !errors.Is(err, keychain.ErrNotFound) {
		return false, err
	}
	delete(v.names, name)
	delete(v.values, name)
	if v.kc != nil {
		return true, v.saveIndexLocked()
	}
	return true, nil
}

// list is the names of a kind, without its prefix, sorted.
func (v *vault) list(prefix string) []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.loadLocked()
	var out []string
	for n := range v.names {
		if rest, ok := strings.CutPrefix(n, prefix); ok {
			out = append(out, rest)
		}
	}
	slices.Sort(out)
	return out
}

// purge removes every secret of the plugin's from the keychain, as the
// plugin is removed; its files go with its data folder. It returns the
// first error, having tried every secret.
func (v *vault) purge() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.loadLocked()
	var first error
	if v.kc != nil {
		for n := range v.names {
			if err := v.kc.Delete(v.service, n); err != nil && !errors.Is(err, keychain.ErrNotFound) && first == nil {
				first = err
			}
		}
	}
	v.loaded, v.names, v.values = false, nil, nil
	return first
}

// checkSecret checks a secret's value: UTF-8 of at most maxSecret bytes.
func checkSecret(what, value string) error {
	if len(value) > maxSecret || !utf8.ValidString(value) {
		return fmt.Errorf("%s is text of at most %d bytes", what, maxSecret)
	}
	return nil
}

// SecretStore says where the node keeps plugins' secrets: the system's
// keychain by name, or "file", with why there is no keychain.
func (m *Manager) SecretStore() (store, note string) {
	if m.cfg.Keychain != nil {
		return m.cfg.Keychain.Name(), ""
	}
	return "file", m.cfg.KeychainNote
}
