package store

import (
	"maps"
	"slices"
	"time"

	"github.com/ironpark/mako/internal/plugin/pack"
)

// A node remembers, for each plugin ID it installed, the publisher key it
// installed it by last, after the plugin is removed and its pin
// forgotten, in publishers.json. A later first install of the ID by
// another key installs as any first install does, but says so, before
// the user is asked to allow it anything: the plugin of that ID the user
// had was another publisher's, which a name alike does not show. It is a
// note, not a pin: nothing is refused for it.

const (
	knownFile = "publishers.json"
	// maxKnown bounds the IDs remembered; the longest unused go first.
	maxKnown = 256
)

// knownKey is the key a plugin ID was last installed by, and when.
type knownKey struct {
	Key string    `json:"key"`
	At  time.Time `json:"at"`
}

// previousKey is the fingerprint of the key the ID was last installed by
// on this node, if it is not key; "" otherwise.
func (s *Store) previousKey(id, key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if k, ok := s.known[id]; ok && !pack.SameKey(k.Key, key) {
		return pack.Fingerprint(k.Key)
	}
	return ""
}

// rememberKey notes that the ID is installed by key now.
func (s *Store) rememberKey(id, key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := maps.Clone(s.known)
	next[id] = knownKey{Key: key, At: s.cfg.Now().UTC()}
	if len(next) > maxKnown {
		ids := slices.SortedFunc(maps.Keys(next), func(a, b string) int { return next[a].At.Compare(next[b].At) })
		for _, old := range ids[:len(next)-maxKnown] {
			delete(next, old)
		}
	}
	if err := writeJSON(s.path(knownFile), next); err != nil {
		s.cfg.Log.Warn("plugin's publisher not remembered", "plugin", id, "err", err)
		return
	}
	s.known = next
}
