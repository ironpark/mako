package plugin

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"unicode/utf8"

	"github.com/ironpark/mako/internal/atomicfile"
	"github.com/ironpark/mako/internal/plugin/hostapi"
	"github.com/ironpark/mako/internal/protocol"
)

// A plugin's settings are what its manifest declares, set by the user in
// Settings ▸ Plugins and read by the plugin alone. They are kept in its
// data folder on this node (mode 0600), but for a secret's value, which
// is kept in its vault (secrets.go): the system's keychain, or a file of
// its own without one. A secret is never sent to a front-end, which is
// told only whether one is set.

const maxSettingText = 1000

type settingsStore struct {
	mu     sync.Mutex
	path   string
	vault  *vault
	values map[string]any // string, bool or float64
	loaded bool
}

func newSettingsStore(path string, v *vault) *settingsStore {
	return &settingsStore{path: path, vault: v}
}

// reset forgets what was read, for settings whose file was removed.
func (s *settingsStore) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values, s.loaded = nil, false
}

func (s *settingsStore) loadLocked() {
	if s.loaded {
		return
	}
	s.loaded = true
	s.values = map[string]any{}
	if b, err := os.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(b, &s.values)
	}
}

// value is the setting's value, of its type, and whether the user set it:
// else its default, else the type's zero. A secret's is read from the
// vault.
func (s *settingsStore) value(d SettingDecl) (any, bool, error) {
	if d.Type == SettingSecret {
		s.migrate(d)
		v, ok, err := s.vault.get(vaultSetting + d.Key)
		return v, ok, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	if v, ok := s.values[d.Key]; ok && fits(d, v) {
		return v, true, nil
	}
	if d.Default != nil {
		return d.Default, false, nil
	}
	switch d.Type {
	case SettingBoolean:
		return false, false, nil
	case SettingNumber:
		return 0.0, false, nil
	}
	return "", false, nil
}

// migrate moves a secret setting an older mako kept in settings.json into
// the vault, once. One that does not move stays where it was, and is
// tried again on the next read.
func (s *settingsStore) migrate(d SettingDecl) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	v, ok := s.values[d.Key].(string)
	if !ok {
		return
	}
	if checkSecret(d.Key, v) != nil || s.vault.set(vaultSetting+d.Key, v) != nil {
		return
	}
	delete(s.values, d.Key)
	_ = s.saveLocked()
}

// migrateAll moves every secret setting of m's out of settings.json.
func (s *settingsStore) migrateAll(m *Manifest) {
	for _, d := range m.Settings {
		if d.Type == SettingSecret {
			s.migrate(d)
		}
	}
}

// legacy says settings.json still holds a value of key's, as an older
// mako kept a secret.
func (s *settingsStore) legacy(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	_, ok := s.values[key].(string)
	return ok
}

// fits says v, as read from the file, is a value of d's type, and of its
// options for a select.
func fits(d SettingDecl, v any) bool {
	switch v := v.(type) {
	case string:
		return d.Type == SettingString || d.Type == SettingSecret || d.Type == SettingSelect && slices.Contains(d.Options, v)
	case bool:
		return d.Type == SettingBoolean
	case float64:
		return d.Type == SettingNumber
	}
	return false
}

// set sets d to text as its type reads it; "" unsets it.
func (s *settingsStore) set(d SettingDecl, text string) error {
	var v any
	switch {
	case text == "":
	case d.Type == SettingBoolean:
		b, err := strconv.ParseBool(text)
		if err != nil {
			return fmt.Errorf("%s is true or false", d.Key)
		}
		v = b
	case d.Type == SettingNumber:
		n, err := strconv.ParseFloat(text, 64)
		if err != nil || math.IsInf(n, 0) || math.IsNaN(n) {
			return fmt.Errorf("%s is a number", d.Key)
		}
		v = n
	case d.Type == SettingSelect:
		if !slices.Contains(d.Options, text) {
			return fmt.Errorf("%s is one of its options", d.Key)
		}
		v = text
	case d.Type == SettingSecret:
		if err := checkSecret(d.Key, text); err != nil {
			return err
		}
		v = text
	default:
		if len(text) > maxSettingText || !utf8.ValidString(text) {
			return fmt.Errorf("%s is text of at most %d bytes", d.Key, maxSettingText)
		}
		v = text
	}
	if d.Type == SettingSecret {
		s.migrate(d)
		if v == nil {
			_, err := s.vault.del(vaultSetting + d.Key)
			return err
		}
		return s.vault.set(vaultSetting+d.Key, text)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	if v == nil {
		delete(s.values, d.Key)
	} else {
		s.values[d.Key] = v
	}
	return s.saveLocked()
}

func (s *settingsStore) saveLocked() error {
	b, err := json.Marshal(s.values)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	return atomicfile.Write(s.path, b)
}

// forHost is a setting as the plugin reads it.
func (s *settingsStore) forHost(d SettingDecl) (*hostapi.Setting, error) {
	v, set, err := s.value(d)
	if err != nil {
		return nil, fmt.Errorf("setting %s: %w", d.Key, err)
	}
	out := &hostapi.Setting{Key: d.Key, Type: d.Type, Set: set}
	switch v := v.(type) {
	case string:
		out.Text = v
	case bool:
		out.Flag = v
	case float64:
		out.Number = v
	}
	return out, nil
}

// forFront is a setting as a front-end shows it: a secret's value never,
// nor is it read for this, only whether the vault holds one.
func (s *settingsStore) forFront(d SettingDecl) *protocol.PluginSetting {
	out := &protocol.PluginSetting{Key: d.Key, Type: d.Type, Label: d.Label, Description: d.Description,
		Options: d.Options}
	if d.Type == SettingSecret {
		out.Set = s.vault.has(vaultSetting+d.Key) || s.legacy(d.Key)
		return out
	}
	v, set, _ := s.value(d)
	out.Set = set
	switch v := v.(type) {
	case string:
		out.Value = v
	case bool:
		out.Value = strconv.FormatBool(v)
	case float64:
		out.Value = strconv.FormatFloat(v, 'f', -1, 64)
	}
	return out
}
