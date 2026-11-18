// Package plugin runs the node's plugins in the daemon: each in a sandbox
// of its own (qngg, QuickJS on WebAssembly), started when one of its
// blocks shows or one of its commands runs and stopped once idle, reaching
// mako only through the Host API (internal/plugin/hostapi), whose every
// call is checked against what the user allowed it. A plugin draws its
// blocks as trees of a fixed vocabulary, which go to the front-ends as
// the protocol's messages, first whole and then as patches. See
// docs/plugins.md.
//
// In this version every plugin is a folder of the daemon's plugins
// folder, read again whenever it changes, as in development.
package plugin

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ironpark/mako/internal/atomicfile"
	"github.com/ironpark/mako/internal/layoutdoc"
	"github.com/ironpark/mako/internal/protocol"
)

// Limits bound what a plugin may use. The zero value of a field takes its
// default (DefaultLimits).
type Limits struct {
	Memory        int           // its JavaScript heap, in bytes
	Stack         int           // its JavaScript stack, in bytes
	CPU           time.Duration // a call's JavaScript time
	Wall          time.Duration // a call's time in all, host work included
	Idle          time.Duration // with nothing shown or run, until it is stopped
	Failures      int           // calls failed in a row, after which it is stopped
	Storage       int           // its store's bytes
	Events        int           // events a second from the front-ends, beyond which they are refused
	Updates       time.Duration // between two updates of a block on a stream
	HTTPTimeout   time.Duration // an HTTP request's
	HTTPBody      int           // an HTTP response's bytes
	HTTPPerMinute int           // HTTP requests a minute
	Logs          int           // log lines a minute
}

// DefaultLimits are the limits a plugin runs under.
var DefaultLimits = Limits{
	Memory:        32 << 20,
	Stack:         512 << 10,
	CPU:           2 * time.Second,
	Wall:          30 * time.Second,
	Idle:          5 * time.Minute,
	Failures:      3,
	Storage:       1 << 20,
	Events:        20,
	Updates:       100 * time.Millisecond,
	HTTPTimeout:   10 * time.Second,
	HTTPBody:      1 << 20,
	HTTPPerMinute: 60,
	Logs:          120,
}

func (l Limits) withDefaults() Limits {
	d := DefaultLimits
	pick := func(v *int, def int) {
		if *v <= 0 {
			*v = def
		}
	}
	pickD := func(v *time.Duration, def time.Duration) {
		if *v <= 0 {
			*v = def
		}
	}
	pick(&l.Memory, d.Memory)
	pick(&l.Stack, d.Stack)
	pickD(&l.CPU, d.CPU)
	pickD(&l.Wall, d.Wall)
	pickD(&l.Idle, d.Idle)
	pick(&l.Failures, d.Failures)
	pick(&l.Storage, d.Storage)
	pick(&l.Events, d.Events)
	pickD(&l.Updates, d.Updates)
	pickD(&l.HTTPTimeout, d.HTTPTimeout)
	pick(&l.HTTPBody, d.HTTPBody)
	pick(&l.HTTPPerMinute, d.HTTPPerMinute)
	pick(&l.Logs, d.Logs)
	return l
}

// Config is what a Manager runs with.
type Config struct {
	// Dir is the plugins' folder, a folder of each, named for its ID.
	Dir string
	// DataDir holds what each plugin keeps on this node, its settings and
	// store, a folder of each; and the user's grants, beside it.
	DataDir string
	Log     *slog.Logger
	// Sessions lists the node's sessions, for sessions:read.
	Sessions func() []*protocol.SessionInfo
	// Changed is told when the list of plugins, or how one stands, changes.
	Changed func()
	// Poll is how often the folder is read for changes; 0 for a second.
	Poll   time.Duration
	Limits Limits
	// Installs says which plugins were installed from a registry, and
	// which a registry blocks; nil for none.
	Installs Installs
	// NetTest sends the requests for a host to another base URL, a test's
	// server on loopback (MAKO_PLUGIN_NET_TEST). The host must still be
	// granted. It is for development and tests alone.
	NetTest map[string]string
}

// Manager runs the plugins of a folder.
type Manager struct {
	cfg   Config
	lim   Limits
	net   *fetcher
	ctx   context.Context
	stop  context.CancelFunc
	grant *grantStore
	// installs is Config.Installs, or none.
	installs Installs
	// scanMu keeps the folder's readings, and its changes by Replace and
	// Remove, one at a time.
	scanMu sync.Mutex

	mu      sync.Mutex
	plugins map[string]*plugin
	wg      sync.WaitGroup
}

// New makes a Manager; Run starts it.
func New(cfg Config) *Manager {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Poll <= 0 {
		cfg.Poll = time.Second
	}
	if cfg.Sessions == nil {
		cfg.Sessions = func() []*protocol.SessionInfo { return nil }
	}
	if cfg.Changed == nil {
		cfg.Changed = func() {}
	}
	if cfg.Installs == nil {
		cfg.Installs = noInstalls{}
	}
	lim := cfg.Limits.withDefaults()
	ctx, stop := context.WithCancel(context.Background())
	return &Manager{
		cfg: cfg, lim: lim, ctx: ctx, stop: stop, installs: cfg.Installs,
		net:     newFetcher(lim, cfg.Log, cfg.NetTest),
		grant:   loadGrants(filepath.Join(cfg.DataDir, "grants.json")),
		plugins: map[string]*plugin{},
	}
}

// Run reads the folder for plugins, and again every Poll, until ctx ends;
// then it stops them all.
func (m *Manager) Run(ctx context.Context) {
	defer m.shutdown()
	if len(m.cfg.NetTest) > 0 {
		m.cfg.Log.Warn("plugins' HTTP is sent to test servers", "hosts", m.cfg.NetTest)
	}
	m.scan()
	t := time.NewTicker(m.cfg.Poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.scan()
		}
	}
}

func (m *Manager) shutdown() {
	m.stop()
	m.wg.Wait()
}

// scan reads the folder: a plugin new to it is started, one whose files
// changed is read again, and one gone is stopped.
func (m *Manager) scan() {
	m.scanMu.Lock()
	defer m.scanMu.Unlock()
	entries, _ := os.ReadDir(m.cfg.Dir)
	seen := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || !pluginID.MatchString(name) {
			continue
		}
		seen[name] = true
		print := fingerprint(m.cfg.Dir, name)
		m.mu.Lock()
		p, ok := m.plugins[name]
		if !ok {
			p = newPlugin(m, name)
			m.plugins[name] = p
			m.wg.Add(1)
			go func() { defer m.wg.Done(); p.run(m.ctx) }()
		}
		m.mu.Unlock()
		if p.noteFolder(print) {
			p.post(func() { p.reload() })
		}
	}
	m.mu.Lock()
	var gone []*plugin
	for id, p := range m.plugins {
		if !seen[id] {
			gone = append(gone, p)
			delete(m.plugins, id)
		}
	}
	m.mu.Unlock()
	for _, p := range gone {
		p.quit()
	}
	if len(gone) > 0 {
		m.cfg.Changed()
	}
}

func (m *Manager) plugin(id string) (*plugin, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.plugins[id]
	return p, ok
}

func (m *Manager) all() []*plugin {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*plugin, 0, len(m.plugins))
	for _, p := range m.plugins {
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b *plugin) int { return strings.Compare(a.id, b.id) })
	return out
}

// List is the plugins as they stand, by ID.
func (m *Manager) List() []*protocol.PluginInfo {
	var out []*protocol.PluginInfo
	for _, p := range m.all() {
		if p.listed() {
			out = append(out, p.info())
		}
	}
	return out
}

// Blocks are the blocks the plugins contribute, for the catalog agents
// read.
func (m *Manager) Blocks() []layoutdoc.Block {
	var out []layoutdoc.Block
	for _, p := range m.all() {
		v := p.current()
		if v == nil {
			continue
		}
		for _, b := range v.man.Blocks {
			out = append(out, layoutdoc.Block{Name: blockName(p.id, b.ID), Title: b.Title, Props: layoutdoc.NoProps()})
		}
	}
	return out
}

// BlockProps makes the plugins' blocks a layoutdoc.Catalog. A plugin's
// block takes no props in this version.
func (m *Manager) BlockProps(name string) (map[string]any, bool) {
	id, block, ok := splitBlock(name)
	if !ok {
		return nil, false
	}
	p, ok := m.plugin(id)
	if !ok {
		return nil, false
	}
	v := p.current()
	if v == nil || !slices.ContainsFunc(v.man.Blocks, func(b BlockDecl) bool { return b.ID == block }) {
		return nil, false
	}
	return layoutdoc.NoProps(), true
}

// blockName is a plugin's block's registered name: plugin:<id>/<block>.
// Both parts are the daemon's to join, from the plugin's own ID and a
// block its manifest declares, so no plugin names another's block.
func blockName(id, block string) string { return "plugin:" + id + "/" + block }

// splitBlock takes a block's name apart into the plugin's ID and the
// block's.
func splitBlock(name string) (id, block string, ok bool) {
	rest, ok := strings.CutPrefix(name, "plugin:")
	if !ok {
		return "", "", false
	}
	id, block, ok = strings.Cut(rest, "/")
	if !ok || !pluginID.MatchString(id) || !partID.MatchString(block) {
		return "", "", false
	}
	return id, block, true
}

// grantStore is what the user allowed each plugin, and refused, kept on
// this node alone.
type grantStore struct {
	mu   sync.Mutex
	path string
	data map[string]grantEntry
}

type grantEntry struct {
	// Granted are the permissions the user allowed.
	Granted []string `json:"granted,omitempty"`
	// Denied are the permissions the plugin asked for when the user
	// refused them; asked for again as they stand, the plugin stays
	// refused, and asking for others, it asks again.
	Denied []string `json:"denied,omitempty"`
	// Disabled says the user turned the plugin off.
	Disabled bool `json:"disabled,omitempty"`
}

func loadGrants(path string) *grantStore {
	g := &grantStore{path: path, data: map[string]grantEntry{}}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &g.data)
	}
	return g
}

func (g *grantStore) get(id string) grantEntry {
	g.mu.Lock()
	defer g.mu.Unlock()
	e := g.data[id]
	e.Granted, e.Denied = slices.Clone(e.Granted), slices.Clone(e.Denied)
	return e
}

func (g *grantStore) update(id string, fn func(*grantEntry)) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	e := g.data[id]
	fn(&e)
	if len(e.Granted) == 0 && len(e.Denied) == 0 && !e.Disabled {
		delete(g.data, id)
	} else {
		g.data[id] = e
	}
	b, err := json.MarshalIndent(g.data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(g.path), 0o700); err != nil {
		return err
	}
	return atomicfile.Write(g.path, b)
}
