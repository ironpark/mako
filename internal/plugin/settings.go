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
// data folder on this node (mode 0600, as a secret's value is among
// them); a secret is never sent to a front-end, which is told only
// whether one is set. The system's keychain is for a later version.

const (
	maxSettingText   = 1000
	maxSettingSecret = 4096
)

type settingsStore struct {
	mu     sync.Mutex
	path   string
	values map[string]any // string, bool or float64
	loaded bool
}

func newSettingsStore(path string) *settingsStore { return &settingsStore{path: path} }

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
// else its default, else the type's zero.
func (s *settingsStore) value(d SettingDecl) (any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	if v, ok := s.values[d.Key]; ok && fits(d, v) {
		return v, true
	}
	if d.Default != nil {
		return d.Default, false
	}
	switch d.Type {
	case SettingBoolean:
		return false, false
	case SettingNumber:
		return 0.0, false
	}
	return "", false
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
	default:
		limit := maxSettingText
		if d.Type == SettingSecret {
			limit = maxSettingSecret
		}
		if len(text) > limit || !utf8.ValidString(text) {
			return fmt.Errorf("%s is text of at most %d bytes", d.Key, limit)
		}
		v = text
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	if v == nil {
		delete(s.values, d.Key)
	} else {
		s.values[d.Key] = v
	}
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
func (s *settingsStore) forHost(d SettingDecl) *hostapi.Setting {
	v, set := s.value(d)
	out := &hostapi.Setting{Key: d.Key, Type: d.Type, Set: set}
	switch v := v.(type) {
	case string:
		out.Text = v
	case bool:
		out.Flag = v
	case float64:
		out.Number = v
	}
	return out
}

// forFront is a setting as a front-end shows it: a secret's value never.
func (s *settingsStore) forFront(d SettingDecl) *protocol.PluginSetting {
	v, set := s.value(d)
	out := &protocol.PluginSetting{Key: d.Key, Type: d.Type, Label: d.Label, Description: d.Description,
		Options: d.Options, Set: set}
	switch v := v.(type) {
	case string:
		if d.Type != SettingSecret {
			out.Value = v
		}
	case bool:
		out.Value = strconv.FormatBool(v)
	case float64:
		out.Value = strconv.FormatFloat(v, 'f', -1, 64)
	}
	return out
}
