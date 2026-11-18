package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/ironpark/mako/internal/plugin/registry"
	"github.com/ironpark/mako/internal/protocol"
)

// A node's registries are the one mako comes with, unless the user
// removed it, and those the user added, in the order added. Each one's
// index last read is kept, so that a registry out of reach keeps its
// listings and its blocklist until it is read again.

const fetchIndexWithin = 20 * time.Second

// reg is a registry as the node last read it.
type reg struct {
	url     string // as the node keeps it
	index   string // its index file's URL
	builtin bool
	idx     *registry.Index
	fetched time.Time
	err     string
}

// cached is a registry's index as kept on disk.
type cached struct {
	URL     string          `json:"url"`
	Fetched time.Time       `json:"fetched"`
	Index   json.RawMessage `json:"index"`
}

// cachePath is where a registry's index is kept: a folder whose name no
// plugin's data folder can take.
func (s *Store) cachePath(url string) string {
	sum := sha256.Sum256([]byte(url))
	return s.path("_registries/" + hex.EncodeToString(sum[:12]) + ".json")
}

// loadRegs makes the registries of the settings, with their indexes
// read before.
func (s *Store) loadRegs() []*reg {
	var urls []string
	builtin := ""
	if s.cfg.Default != "" && !s.settings.NoDefault {
		if canon, _, err := registry.Normalize(s.cfg.Default, s.cfg.Loopback); err == nil {
			builtin = canon
			urls = append(urls, canon)
		}
	}
	for _, u := range s.settings.Added {
		if !slices.Contains(urls, u) {
			urls = append(urls, u)
		}
	}
	var out []*reg
	for _, u := range urls {
		if r := s.newReg(u, u == builtin); r != nil {
			out = append(out, r)
		}
	}
	return out
}

// newReg is a registry of the URL, with its index read before; nil for
// a URL that does not check.
func (s *Store) newReg(u string, builtin bool) *reg {
	canon, index, err := registry.Normalize(u, s.cfg.Loopback)
	if err != nil {
		return nil
	}
	r := &reg{url: canon, index: index, builtin: builtin}
	var c cached
	readJSON(s.cachePath(canon), &c)
	if c.URL == canon {
		if idx, err := registry.Parse(c.Index, s.cfg.Loopback); err == nil {
			r.idx, r.fetched = idx, c.Fetched
		}
	}
	return r
}

func (s *Store) regLocked(url string) *reg {
	for _, r := range s.regs {
		if r.url == url {
			return r
		}
	}
	return nil
}

// Registries are the node's registries as they stand.
func (s *Store) Registries() []*protocol.RegistryInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*protocol.RegistryInfo, 0, len(s.regs))
	for _, r := range s.regs {
		info := &protocol.RegistryInfo{Url: r.url, Builtin: r.builtin, Error: r.err}
		if !r.fetched.IsZero() {
			info.FetchedAt = r.fetched.Unix()
		}
		if r.idx != nil {
			info.Name = r.idx.Name
			info.Plugins, info.Blocked = int32(len(r.idx.Plugins)), int32(len(r.idx.Blocklist))
		}
		out = append(out, info)
	}
	return out
}

// AddRegistry adds a registry by its URL and reads it. One that cannot
// be read now is added all the same, its error beside it.
func (s *Store) AddRegistry(ctx context.Context, raw string) error {
	canon, _, err := registry.Normalize(raw, s.cfg.Loopback)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.regLocked(canon) != nil {
		s.mu.Unlock()
		return fmt.Errorf("%s is a registry of this node already", canon)
	}
	if len(s.regs) >= 32 {
		s.mu.Unlock()
		return errors.New("a node takes 32 registries at most")
	}
	builtin := false
	if s.cfg.Default != "" {
		if d, _, err := registry.Normalize(s.cfg.Default, s.cfg.Loopback); err == nil && d == canon {
			builtin = true
		}
	}
	next := s.settings
	if builtin {
		next.NoDefault = false
	} else {
		next.Added = append(slices.Clone(next.Added), canon)
	}
	if err := writeJSON(s.path("registries.json"), next); err != nil {
		s.mu.Unlock()
		return err
	}
	s.settings = next
	r := s.newReg(canon, builtin)
	if builtin {
		s.regs = append([]*reg{r}, s.regs...)
	} else {
		s.regs = append(s.regs, r)
	}
	s.mu.Unlock()
	s.cfg.Log.Info("plugin registry added", "registry", canon)
	if r != nil {
		s.read(ctx, r)
	}
	s.recheck()
	return nil
}

// RemoveRegistry removes a registry, its listings and its blocklist with
// it. Plugins installed from it stay, and no longer hear of updates.
func (s *Store) RemoveRegistry(raw string) error {
	canon, _, err := registry.Normalize(raw, s.cfg.Loopback)
	if err != nil {
		return err
	}
	s.mu.Lock()
	r := s.regLocked(canon)
	if r == nil {
		s.mu.Unlock()
		return fmt.Errorf("%s is not a registry of this node", canon)
	}
	next := s.settings
	if r.builtin {
		next.NoDefault = true
	} else {
		next.Added = slices.DeleteFunc(slices.Clone(next.Added), func(u string) bool { return u == canon })
	}
	if err := writeJSON(s.path("registries.json"), next); err != nil {
		s.mu.Unlock()
		return err
	}
	s.settings = next
	s.regs = slices.DeleteFunc(s.regs, func(x *reg) bool { return x == r })
	s.mu.Unlock()
	_ = removeFile(s.cachePath(canon))
	s.cfg.Log.Info("plugin registry removed", "registry", canon)
	s.recheck()
	return nil
}

// Refresh reads every registry again now, at once, and has the manager
// look again at what is blocked.
func (s *Store) Refresh(ctx context.Context) {
	s.refreshOlder(ctx, 0)
	s.recheck()
}

// refreshOlder reads again, at once, the registries last read longer ago
// than age, or that failed.
func (s *Store) refreshOlder(ctx context.Context, age time.Duration) {
	s.mu.Lock()
	var due []*reg
	for _, r := range s.regs {
		if age == 0 || r.idx == nil || r.err != "" || s.cfg.Now().Sub(r.fetched) > age {
			due = append(due, r)
		}
	}
	s.mu.Unlock()
	var wg sync.WaitGroup
	for _, r := range due {
		wg.Go(func() { s.read(ctx, r) })
	}
	wg.Wait()
}

// read fetches a registry's index and keeps it, or keeps the error and
// the index read before. An index whose blocklist is not the one kept
// has every plugin look again at once, whatever asked for the reading (a
// search, an install, the timer), so that a plugin newly blocked runs no
// further.
func (s *Store) read(ctx context.Context, r *reg) {
	data, err := s.fetch.Get(ctx, r.index, registry.MaxIndexBytes, fetchIndexWithin)
	var idx *registry.Index
	if err == nil {
		idx, err = registry.Parse(data, s.cfg.Loopback)
	}
	now := s.cfg.Now()
	s.mu.Lock()
	if s.regLocked(r.url) != r {
		s.mu.Unlock()
		return // removed meanwhile
	}
	if err != nil {
		r.err = protocol.Printable(err.Error())
		s.mu.Unlock()
		// A registry that is not there yet, the default one among them,
		// is an error beside it in Settings, not a warning in the log.
		s.cfg.Log.Debug("plugin registry not read", "registry", r.url, "err", err)
		return
	}
	changed := r.idx == nil && len(idx.Blocklist) > 0 || r.idx != nil && !sameBlocklist(r.idx.Blocklist, idx.Blocklist)
	r.idx, r.fetched, r.err = idx, now, ""
	if err := writeJSON(s.cachePath(r.url), cached{URL: r.url, Fetched: now, Index: data}); err != nil {
		s.cfg.Log.Warn("plugin registry's index not kept", "registry", r.url, "err", err)
	}
	s.mu.Unlock()
	if changed {
		s.recheck()
	}
}

// sameBlocklist says two blocklists hold the same entries in the same
// order.
func sameBlocklist(a, b []*registry.Block) bool {
	return slices.EqualFunc(a, b, func(x, y *registry.Block) bool { return *x == *y })
}

// blockedLocked is why a registry blocks a plugin of the ID, version and
// publisher key, "" if none does: the first registry's entry that does.
func (s *Store) blockedLocked(id, version, key string) string {
	for _, r := range s.regs {
		if r.idx == nil {
			continue
		}
		for _, b := range r.idx.Blocklist {
			if b.Matches(id, version, key) {
				return fmt.Sprintf("blocked by the registry %s: %s", r.name(), b.Reason)
			}
		}
	}
	return ""
}

// name is how a registry is named to the user: its URL, which is what
// they added, with the name its index gives itself.
func (r *reg) name() string {
	if r.idx != nil && r.idx.Name != "" {
		return fmt.Sprintf("%s (%q)", r.url, r.idx.Name)
	}
	return r.url
}

func removeFile(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
