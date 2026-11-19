// Package store installs plugins on a node from the registries its user
// added (internal/plugin/registry), as signed packages
// (internal/plugin/pack), and keeps what each install pinned: the
// registry it came from and its publisher's key. Nobody reviews a
// registry, so the node is where safety is kept:
//
//   - A package is installed only once its SHA-256 is the one its
//     registry's index lists, its signature holds for its contents by
//     the publisher key the index lists, and its manifest is the ID and
//     version listed and reads strictly (no exec).
//   - The publisher key is pinned at the first install. A version signed
//     by another key is not taken as an update, from any registry; the
//     user removes the plugin, which forgets the pin, to install another
//     publisher's.
//   - A plugin follows the registry it was installed from; the same ID
//     in another registry is another listing, never taken silently.
//   - Every registry's blocklist applies to every plugin of the node,
//     whichever registry it came from, and a folder in development by
//     its ID: removing the registry lifts its list.
//   - A plugin is never installed over a folder written by hand, and
//     never downgraded.
//
// What a version asks for beyond the one it replaces is asked of the
// user before it runs, by the manager's grants, as for a folder.
package store

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ironpark/mako/internal/plugin"
	"github.com/ironpark/mako/internal/plugin/pack"
	"github.com/ironpark/mako/internal/plugin/registry"
	"github.com/ironpark/mako/pkg/fsutil"
)

// DefaultRegistry is the registry every node starts with, which the
// project's owner keeps. It is reviewed no more than any other. It is
// read only when the user opens the store or has installed a plugin, and
// failing to read it is an error beside it in Settings, no more.
const DefaultRegistry = "https://github.com/ironpark/mako-plugins"

// RefreshEvery is how often the registries are read again while a
// plugin of theirs is installed, for its updates and the blocklists.
const RefreshEvery = 6 * time.Hour

// searchStale is how old an index may be when the store is searched
// before it is read again.
const searchStale = 10 * time.Minute

// Config is what a Store runs with.
type Config struct {
	// DataDir is the plugins' data folder (plugin-data), where the store
	// keeps its registries, installs and the indexes last read.
	DataDir string
	// Default is the registry every node starts with; "" for none.
	Default string
	// Loopback lets registries and packages be on this machine's
	// loopback, by http: for tests alone (internal/testhooks).
	Loopback bool
	Log      *slog.Logger
	// Now is the clock; nil for time.Now.
	Now func() time.Time
}

// Store is a node's registries and the plugins installed from them.
type Store struct {
	cfg   Config
	fetch *registry.Fetcher
	man   *plugin.Manager

	// installMu keeps installs, updates and removals one at a time.
	installMu sync.Mutex

	mu       sync.Mutex
	settings registrySettings
	regs     []*reg
	installs map[string]Install
	known    map[string]knownKey // publishers.json
}

// registrySettings are the registries the user added, kept in
// registries.json.
type registrySettings struct {
	Added     []string `json:"added,omitempty"`
	NoDefault bool     `json:"no_default,omitempty"`
}

// Install is a plugin installed from a registry, kept in installs.json:
// what was pinned, and what the package was. Pending is the version an
// install is putting in place, kept while it does (folder.go); a first
// install's pin is its pending version alone until the folder is in
// place, and is no install meanwhile.
type Install struct {
	Registry    string    `json:"registry,omitempty"`
	Key         string    `json:"key,omitempty"`
	Version     string    `json:"version,omitempty"`
	SHA256      string    `json:"sha256,omitempty"`
	Contents    string    `json:"contents,omitempty"`
	Permissions []string  `json:"permissions,omitempty"`
	At          time.Time `json:"at,omitzero"`
	Pending     *Install  `json:"pending,omitempty"`
}

// settled says the pin names a version in place, not only one pending.
func (in Install) settled() bool { return in.Contents != "" }

// New makes a Store, reading what it kept; Attach gives it the manager.
func New(cfg Config) *Store {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	s := &Store{cfg: cfg, fetch: registry.NewFetcher(cfg.Loopback), installs: map[string]Install{}, known: map[string]knownKey{}}
	readJSON(s.path("registries.json"), &s.settings)
	readJSON(s.path("installs.json"), &s.installs)
	readJSON(s.path(knownFile), &s.known)
	// A pin an older mako kept may spell its key otherwise: it is kept
	// as the key, written as mako writes it now.
	for id, in := range s.installs {
		if k, err := pack.CanonicalKey(in.Key); err == nil {
			in.Key = k
			s.installs[id] = in
		}
	}
	s.regs = s.loadRegs()
	return s
}

// Attach gives the store the manager of the plugins it installs.
func (s *Store) Attach(m *plugin.Manager) { s.man = m }

// Run reads the registries again every RefreshEvery while a plugin of
// theirs is installed, and at once if one is, until ctx ends. A node
// with none installed reads none until its user opens the store.
func (s *Store) Run(ctx context.Context) {
	if s.cfg.Loopback {
		s.cfg.Log.Warn("plugin registries and packages may be on loopback, by http: for tests alone")
	}
	t := time.NewTicker(RefreshEvery)
	defer t.Stop()
	for first := true; ; first = false {
		if s.anyInstalled() {
			s.Refresh(ctx)
		}
		if first {
			// The indexes read before stand meanwhile.
			s.recheck()
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Store) anyInstalled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.installs) > 0
}

// installedLocked is the plugin's pin, if it names a version in place.
func (s *Store) installedLocked(id string) (Install, bool) {
	in, ok := s.installs[id]
	if !ok || !in.settled() {
		return Install{}, false
	}
	return in, true
}

// recheck has the manager look again at what is blocked and updated.
func (s *Store) recheck() {
	if s.man != nil {
		s.man.Recheck()
	}
}

func (s *Store) path(name string) string { return filepath.Join(s.cfg.DataDir, name) }

func readJSON(path string, v any) {
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, v)
	}
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return fsutil.Write(path, append(b, '\n'))
}

// Status makes the store the manager's plugin.Installs.
func (s *Store) Status(id, version string) plugin.InstallStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	in, ok := s.installedLocked(id)
	st := plugin.InstallStatus{Installed: ok, Source: in.Registry, Key: in.Key}
	if version == "" {
		version = in.Version
	}
	st.Blocked = s.blockedLocked(id, version, in.Key)
	if ok {
		if r := s.regLocked(in.Registry); r != nil && r.idx != nil {
			if e := r.idx.Find(id); e != nil && pack.SameKey(e.Key, in.Key) {
				if v := e.Newest(plugin.APIVersion); v != nil && plugin.CompareVersions(v.Version, in.Version) > 0 &&
					s.blockedLocked(id, v.Version, e.Key) == "" {
					st.Update = v.Version
				}
			}
		}
	}
	return st
}

// Installed is the plugin's install, if it was installed from a registry.
func (s *Store) Installed(id string) (Install, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.installedLocked(id)
}
