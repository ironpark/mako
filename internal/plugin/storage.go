package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/ironpark/mako/internal/atomicfile"
)

// A plugin with the storage permission has a store of its own, a JSON
// file of keys and values in its data folder on this node, bounded in
// keys, in each key's and value's size, and in all it holds. It is not
// synced between nodes in this version.

const (
	maxStorageKey   = 256
	maxStorageValue = 256 << 10
	maxStorageKeys  = 1024
)

// kvStore is a plugin's store.
type kvStore struct {
	mu     sync.Mutex
	path   string
	quota  int
	data   map[string]string
	loaded bool
}

func newKVStore(path string, quota int) *kvStore { return &kvStore{path: path, quota: quota} }

func (s *kvStore) load() {
	if s.loaded {
		return
	}
	s.loaded = true
	s.data = map[string]string{}
	if b, err := os.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(b, &s.data)
	}
}

func (s *kvStore) usedLocked() int {
	n := 0
	for k, v := range s.data {
		n += len(k) + len(v)
	}
	return n
}

func (s *kvStore) get(key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	v, ok := s.data[key]
	return v, ok
}

// set writes key's value, unless the store would go over its bounds, and
// returns what it holds after.
func (s *kvStore) set(key, value string) (int, error) {
	if err := checkKey(key); err != nil {
		return 0, err
	}
	if len(value) > maxStorageValue || !utf8.ValidString(value) {
		return 0, fmt.Errorf("a value is UTF-8 of at most %d KiB", maxStorageValue>>10)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	old, had := s.data[key]
	if !had && len(s.data) >= maxStorageKeys {
		return 0, fmt.Errorf("the store holds %d keys, its most", maxStorageKeys)
	}
	used := s.usedLocked() + len(value) - len(old)
	if !had {
		used += len(key)
	}
	if used > s.quota {
		return 0, fmt.Errorf("the write would take the store to %d bytes, over its quota of %d", used, s.quota)
	}
	s.data[key] = value
	if err := s.saveLocked(); err != nil {
		if had {
			s.data[key] = old
		} else {
			delete(s.data, key)
		}
		return 0, err
	}
	return used, nil
}

func (s *kvStore) del(key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	_, ok := s.data[key]
	if !ok {
		return false, nil
	}
	delete(s.data, key)
	return true, s.saveLocked()
}

func (s *kvStore) keys(prefix string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	var out []string
	for k := range s.data {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

func (s *kvStore) used() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	return s.usedLocked()
}

func (s *kvStore) saveLocked() error {
	b, err := json.Marshal(s.data)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	return atomicfile.Write(s.path, b)
}

func checkKey(key string) error {
	if key == "" || len(key) > maxStorageKey || !utf8.ValidString(key) {
		return fmt.Errorf("a key is UTF-8 of 1 to %d bytes", maxStorageKey)
	}
	return nil
}
