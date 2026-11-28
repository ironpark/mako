package plugin

import (
	"encoding/json"
	"maps"
	"math"
	"reflect"
	"slices"
	"unicode/utf8"
)

// DisabledSnapshot returns the disabled flag of every loaded plugin plus any
// plugin currently marked disabled in the grants store.
func (m *Manager) DisabledSnapshot() map[string]bool {
	out := map[string]bool{}
	m.grant.mu.RLock()
	for id, e := range m.grant.data {
		if e.Disabled {
			out[id] = true
		}
	}
	m.grant.mu.RUnlock()
	for _, p := range m.all() {
		if _, set := out[p.id]; !set {
			out[p.id] = false
		}
	}
	return out
}

// ApplySyncedDisabled applies a peer-synced disabled flag for plugin id without
// modifying permissions/grants or agent allowances.
func (m *Manager) ApplySyncedDisabled(id string, disabled bool) (bool, error) {
	if !ValidID(id) {
		return false, nil
	}
	cur := m.grant.get(id)
	if cur.Disabled == disabled {
		return false, nil
	}
	if err := m.grant.update(id, func(e *grantEntry) {
		e.Disabled = disabled
	}); err != nil {
		return false, err
	}
	if p, ok := m.plugin(id); ok {
		p.enableReset.Store(!disabled)
		if disabled {
			p.cancelPending("the plugin was disabled")
		}
		p.signalRecheck()
	}
	return true, nil
}

// NonSecretSettingsSnapshot returns canonical JSON bytes for every explicitly
// set non-secret setting (global and scoped) declared by currently loaded
// plugins, keyed by "<plugin_id>\x00<scope_key>\x00<setting_key>". Secret
// settings are strictly excluded and never read from the vault.
func (m *Manager) NonSecretSettingsSnapshot() map[string][]byte {
	out := map[string][]byte{}
	for _, p := range m.all() {
		v := p.current()
		if v == nil || len(v.man.Settings) == 0 {
			continue
		}
		decls := make(map[string]SettingDecl, len(v.man.Settings))
		for _, d := range v.man.Settings {
			if d.Type == SettingSecret {
				continue
			}
			decls[d.Key] = d
		}
		if len(decls) == 0 {
			continue
		}
		p.settings.mu.Lock()
		p.settings.loadLocked()
		for k, val := range p.settings.values {
			d, ok := decls[k]
			if !ok || !fits(d, val) {
				continue
			}
			if b, err := json.Marshal(val); err == nil {
				out[p.id+"\x00\x00"+k] = b
			}
		}
		for scopeKey, entries := range p.settings.scoped {
			for k, val := range entries {
				d, ok := decls[k]
				if !ok || !fits(d, val) {
					continue
				}
				if b, err := json.Marshal(val); err == nil {
					out[p.id+"\x00"+scopeKey+"\x00"+k] = b
				}
			}
		}
		p.settings.mu.Unlock()
	}
	return out
}

// SyncedSettingUpdate represents a single non-secret setting update from peer
// sync: Deleted == true unsets the setting; otherwise Value is canonical JSON
// encoding a string, bool, or number.
type SyncedSettingUpdate struct {
	ScopeKey string
	Key      string
	Value    []byte
	Deleted  bool
}

// ApplySyncedSettings applies peer-synced non-secret settings to a loaded
// plugin. Any key not declared in the plugin's manifest or declared as a secret
// is strictly ignored.
func (m *Manager) ApplySyncedSettings(pluginID string, updates []SyncedSettingUpdate) bool {
	p, ok := m.plugin(pluginID)
	if !ok {
		return false
	}
	v := p.current()
	if v == nil || len(v.man.Settings) == 0 {
		return false
	}
	decls := make(map[string]SettingDecl, len(v.man.Settings))
	for _, d := range v.man.Settings {
		if d.Type == SettingSecret {
			continue
		}
		decls[d.Key] = d
	}
	if len(decls) == 0 {
		return false
	}

	changed := false
	changedScopes := map[string]bool{}
	allScopes := false

	p.settings.mu.Lock()
	p.settings.loadLocked()
	for _, u := range updates {
		d, ok := decls[u.Key]
		if !ok {
			continue
		}
		if _, _, err := parseScopeKey(u.ScopeKey); err != nil {
			continue
		}
		if u.Deleted {
			if u.ScopeKey == "" {
				if _, had := p.settings.values[u.Key]; had {
					delete(p.settings.values, u.Key)
					changed = true
					allScopes = true
				}
			} else if sc := p.settings.scoped[u.ScopeKey]; sc != nil {
				if _, had := sc[u.Key]; had {
					delete(sc, u.Key)
					if len(sc) == 0 {
						delete(p.settings.scoped, u.ScopeKey)
					}
					changed = true
					changedScopes[u.ScopeKey] = true
				}
			}
			continue
		}
		var val any
		if err := json.Unmarshal(u.Value, &val); err != nil || !fits(d, val) {
			continue
		}
		switch tv := val.(type) {
		case string:
			if len(tv) > maxSettingText || !utf8.ValidString(tv) {
				continue
			}
		case float64:
			if math.IsInf(tv, 0) || math.IsNaN(tv) {
				continue
			}
		}
		if u.ScopeKey == "" {
			if prev, had := p.settings.values[u.Key]; !had || !reflect.DeepEqual(prev, val) {
				p.settings.values[u.Key] = val
				changed = true
				allScopes = true
			}
		} else {
			sc := p.settings.scoped[u.ScopeKey]
			if sc == nil {
				sc = map[string]any{}
				p.settings.scoped[u.ScopeKey] = sc
			}
			if prev, had := sc[u.Key]; !had || !reflect.DeepEqual(prev, val) {
				sc[u.Key] = val
				changed = true
				changedScopes[u.ScopeKey] = true
			}
		}
	}
	if changed {
		if err := p.settings.saveLocked(); err != nil {
			m.cfg.Log.Warn("synced plugin settings not saved", "plugin", pluginID, "err", err)
			p.settings.mu.Unlock()
			return false
		}
	}
	p.settings.mu.Unlock()

	if !changed {
		return false
	}
	p.status(func() {})
	scopesCopy := maps.Clone(changedScopes)
	p.post(func() {
		if p.rt == nil || p.ensure() != nil {
			return
		}
		p.mu.Lock()
		var loaded []*instance
		for _, in := range p.inst {
			if in.loaded {
				loaded = append(loaded, in)
			}
		}
		p.mu.Unlock()
		var reloadedKeys []string
		for _, in := range loaded {
			matched := allScopes
			if !matched {
				for scopeKey := range scopesCopy {
					if p.settingScopeApplies(scopeKey, in.scopeKey) {
						matched = true
						break
					}
				}
			}
			if !matched {
				continue
			}
			p.load(in)
			reloadedKeys = append(reloadedKeys, in.key)
		}
		if allScopes {
			p.renderAll()
		} else if len(reloadedKeys) > 0 {
			slices.Sort(reloadedKeys)
			for _, key := range reloadedKeys {
				if p.rt == nil {
					return
				}
				p.render(key)
			}
		}
	})
	return true
}

// LoadedPluginIDs returns the IDs of plugins currently loaded with a parsed manifest.
func (m *Manager) LoadedPluginIDs() map[string]bool {
	out := map[string]bool{}
	for _, p := range m.all() {
		if p.current() != nil {
			out[p.id] = true
		}
	}
	return out
}
