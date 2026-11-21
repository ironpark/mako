package plugin

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ironpark/mako/internal/uiblock"

	"github.com/ironpark/mako/internal/protocol"
)

// A plugin is an actor: one goroutine owns its runtime and runs what is
// asked of it, a job at a time, so its JavaScript is never entered twice
// at once; what the front-ends read of it is kept under its lock.

// maxSavedState bounds a block's state kept while its plugin sleeps.
const maxSavedState = 256 << 10

// minRefresh is the shortest time between a block's loads while it shows.
const minRefresh = 15 * time.Second

var errGone = errors.New("the plugin was removed")

type plugin struct {
	m           *Manager
	id          string
	jobs        chan func()
	recheck     chan struct{} // how the plugin stands as installed may have changed
	done        chan struct{}
	exited      chan struct{} // closed once the actor has stopped
	quitOnce    sync.Once
	settings    *settingsStore
	vault       *vault
	store       *kvStore
	db          *dbStore
	logRate     *rateLimit
	netRate     *rateLimit
	evRate      *rateLimit
	secRate     *rateLimit
	dbRate      *rateLimit
	trRate      *rateLimit
	fsRate      *rateLimit
	fsWriteRate *rateLimit
	subRate     *rateLimit
	dropLog     *rateLimit
	subs        *subscriptions
	asks        asks
	msgRate     *rateLimit
	startRate   *rateLimit
	layoutRate  *rateLimit
	layoutBusy  atomic.Bool // a change of the layout of the plugin's is under way

	mu      sync.Mutex
	folder  string // the folder's fingerprint last seen
	v       *version
	read    bool   // its folder was read once, and it is listed
	loadErr string // why the folder's newest version is not running
	stale   bool   // v is older than the folder's newest, which failed
	failErr string // why the plugin was stopped; until its folder changes or it is enabled
	running bool
	inst    map[string]*instance
	changed chan struct{}
	logs    []*protocol.PluginLog

	// The actor's alone.
	rt       *jsRuntime
	saved    map[string]string // blocks' states while the runtime is down
	failures int               // the handlers' calls failed in a row
	// renderFailures are the renders failed in a row.
	renderFailures int
	lastUsed       time.Time
	gesture        bool   // the call under way answers the user
	openURL        string // a link the call asked to open
	toast          string // a short notice the call asked to show
}

const maxPluginLogs = 40

func (p *plugin) recordLog(level, msg string) {
	if level == "" {
		level = "info"
	}
	entry := &protocol.PluginLog{
		At:      time.Now().UnixMilli(),
		Level:   level,
		Message: protocol.Printable(msg),
	}
	p.mu.Lock()
	p.logs = append(p.logs, entry)
	if len(p.logs) > maxPluginLogs {
		p.logs = p.logs[len(p.logs)-maxPluginLogs:]
	}
	p.mu.Unlock()
}

// maxScopeInstancesPerBlock bounds how many scope instances of one block stay
// cached in memory and JS heap at once; actively watched instances are never
// evicted.
const maxScopeInstancesPerBlock = 16

// instance is one (block, scope) instance of a plugin: its state lives in the
// runtime; its tree, last error, watchers, and LRU timestamp here.
type instance struct {
	key      string        // instanceKey(id, scopeKey)
	id       string        // block ID from manifest
	scopeKey string        // canonical scope key: "", "space:<id>", "project:<id>", "session:<id>"
	tree     *uiblock.Tree // the last render that read; guarded by plugin.mu
	err      string        // the last call's error; guarded by plugin.mu
	subs     int           // the streams watching it; guarded by plugin.mu
	lastUsed time.Time     // LRU timestamp; guarded by plugin.mu

	loaded   bool // the actor's: load ran since the runtime started
	lastLoad time.Time
	refresh  time.Duration
}

// getOrCreateInstanceLocked returns the instance for (blockID, scopeKey),
// creating it if absent and evicting LRU unwatched instances beyond
// maxScopeInstancesPerBlock. Caller must hold p.mu.
func (p *plugin) getOrCreateInstanceLocked(blockID, scopeKey string) (*instance, []string) {
	key := instanceKey(blockID, scopeKey)
	now := time.Now()
	if in, ok := p.inst[key]; ok {
		in.lastUsed = now
		return in, nil
	}
	in := &instance{key: key, id: blockID, scopeKey: scopeKey, lastUsed: now}
	p.inst[key] = in
	evicted := p.trimBlockInstancesLocked(blockID, key)
	return in, evicted
}

// trimBlockInstancesLocked evicts the least-recently-used unwatched instances
// for blockID (excluding keepKey) while total instances for blockID exceed
// maxScopeInstancesPerBlock. Caller must hold p.mu.
func (p *plugin) trimBlockInstancesLocked(blockID, keepKey string) []string {
	var evicted []string
	for {
		count := 0
		var victim *instance
		for _, in := range p.inst {
			if in.id != blockID {
				continue
			}
			count++
			if in.subs > 0 || in.key == keepKey {
				continue
			}
			if victim == nil || in.lastUsed.Before(victim.lastUsed) || (in.lastUsed.Equal(victim.lastUsed) && in.key < victim.key) {
				victim = in
			}
		}
		if count <= maxScopeInstancesPerBlock || victim == nil {
			break
		}
		delete(p.inst, victim.key)
		evicted = append(evicted, victim.key)
	}
	return evicted
}

func newPlugin(m *Manager, id string) *plugin {
	data := filepath.Join(m.cfg.DataDir, id)
	v := newVault(m, id)
	return &plugin{
		m: m, id: id,
		jobs:        make(chan func(), 32),
		recheck:     make(chan struct{}, 1),
		done:        make(chan struct{}),
		exited:      make(chan struct{}),
		settings:    newSettingsStore(filepath.Join(data, "settings.json"), v),
		vault:       v,
		store:       newKVStore(filepath.Join(data, "storage.json"), m.lim.Storage),
		db:          newDBStore(filepath.Join(data, "db"), m.lim.DBQuota, m.syncClock, func() { m.invalidateSyncDigest(true) }),
		logRate:     newRateLimit(m.lim.Logs, time.Minute),
		netRate:     newRateLimit(m.lim.HTTPPerMinute, time.Minute),
		evRate:      newRateLimit(m.lim.Events, time.Second),
		secRate:     newRateLimit(secretsPerMinute, time.Minute),
		dbRate:      newRateLimit(dbOpsPerMinute, time.Minute),
		trRate:      newRateLimit(transcriptsPerMin, time.Minute),
		fsRate:      newRateLimit(fsReadsPerMin, time.Minute),
		fsWriteRate: newRateLimit(fsWritesPerMin, time.Minute),
		subRate:     newRateLimit(eventsPerSecond, time.Second),
		dropLog:     newRateLimit(1, time.Minute),
		msgRate:     newRateLimit(messagesPerMin, time.Minute),
		startRate:   newRateLimit(agentStartsPerHr, time.Hour),
		layoutRate:  newRateLimit(layoutPatchesPerMin, time.Minute),
		subs:        &subscriptions{path: filepath.Join(data, "subscriptions.json")},
		inst:        map[string]*instance{},
		changed:     make(chan struct{}),
		saved:       map[string]string{},
	}
}

// run is the actor: it runs jobs until the plugin is removed or the
// manager stops, and stops an idle runtime and reloads blocks on time.
func (p *plugin) run(ctx context.Context) {
	defer close(p.exited)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	defer p.closeRuntime()
	defer p.db.closeAll()
	for {
		select {
		case job := <-p.jobs:
			job()
		case <-p.recheck:
			p.restand()
		case <-tick.C:
			p.tick()
		case <-p.done:
			return
		case <-ctx.Done():
			return
		}
	}
}

// post queues a job, unless the queue is full or the plugin gone.
func (p *plugin) post(job func()) bool {
	select {
	case <-p.done:
		return false
	default:
	}
	select {
	case p.jobs <- job:
		return true
	default:
		return false
	}
}

// do runs job on the actor and waits for it, as long as ctx lets it.
func (p *plugin) do(ctx context.Context, job func() error) error {
	res := make(chan error, 1)
	select {
	case p.jobs <- func() { res <- job() }:
	case <-p.done:
		return errGone
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-res:
		return err
	case <-p.done:
		return errGone
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *plugin) quit() {
	p.quitOnce.Do(func() {
		close(p.done)
		p.bump()
	})
}

// noteFolder records the folder's fingerprint, reporting a change.
func (p *plugin) noteFolder(print string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if print == p.folder {
		return false
	}
	p.folder = print
	return true
}

// bump tells the streams watching the plugin's blocks to look again.
func (p *plugin) bump() {
	p.mu.Lock()
	close(p.changed)
	p.changed = make(chan struct{})
	p.mu.Unlock()
}

// status changes something a listing shows: the streams look again and
// the front-ends are told.
func (p *plugin) status(fn func()) {
	p.mu.Lock()
	fn()
	close(p.changed)
	p.changed = make(chan struct{})
	p.mu.Unlock()
	p.m.cfg.Changed()
}

// listed says the plugin's folder was read once, so that a listing shows
// how it stands rather than that it has not been read.
func (p *plugin) listed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.read
}

func (p *plugin) current() *version {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.v
}

// stateLocked is how the plugin stands, and why: a plugin blocked stands
// so before anything else it might, and whatever the user said.
func (p *plugin) stateLocked(g grantEntry, st InstallStatus) (string, string) {
	switch {
	case p.v == nil:
		return protocol.PluginFailed, cmpOr(p.loadErr, "its folder has not been read")
	case st.Blocked != "":
		return protocol.PluginBlocked, st.Blocked
	case g.Disabled:
		return protocol.PluginDisabled, ""
	}
	if ask := asked(p.v.man, g.Granted); len(ask) > 0 {
		for _, a := range ask {
			if !slices.Contains(g.Denied, a) {
				return protocol.PluginWaiting, ""
			}
		}
		return protocol.PluginDenied, ""
	}
	switch {
	case p.failErr != "":
		return protocol.PluginFailed, p.failErr
	case p.running:
		return protocol.PluginRunning, ""
	}
	return protocol.PluginReady, ""
}

func (p *plugin) state() (string, string) {
	g, st := p.m.grant.get(p.id), p.installStatus()
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stateLocked(g, st)
}

// info is the plugin as a listing shows it.
func (p *plugin) info() *protocol.PluginInfo {
	g, st := p.m.grant.get(p.id), p.installStatus()
	p.mu.Lock()
	defer p.mu.Unlock()
	state, err := p.stateLocked(g, st)
	out := &protocol.PluginInfo{Id: p.id, State: state, Error: err, Stale: p.stale,
		Installed: st.Installed, Source: st.Source, Key: st.Key, Update: st.Update, Blocked: st.Blocked}
	if p.stale && err == "" {
		out.Error = p.loadErr
	}
	// Both may quote what the plugin's package or its code wrote.
	out.Error, out.Blocked = protocol.Printable(out.Error), protocol.Printable(out.Blocked)
	if p.v == nil {
		return out
	}
	man := p.v.man
	out.Version, out.Publisher = man.Version, man.Publisher
	out.Permissions = man.Permissions
	out.Granted = grantsOf(man, g.Granted).list(man)
	out.AgentAsk, out.AgentsAllowed = p.asks.get().info(), g.AgentsAllowed
	out.Asked = asked(man, g.Granted)
	for _, b := range man.Blocks {
		out.Blocks = append(out.Blocks, &protocol.PluginBlock{Name: blockName(p.id, b.ID), Title: b.Title, Slots: b.Slots, Scope: b.Scope})
	}
	for _, c := range man.Commands {
		out.Commands = append(out.Commands, &protocol.PluginCommand{Id: c.ID, Title: c.Title})
	}
	for _, tl := range man.Tools {
		out.Tools = append(out.Tools, &protocol.PluginTool{Id: tl.ID, Description: tl.Description, Scope: tl.Scope})
	}
	for _, s := range man.Settings {
		out.Settings = append(out.Settings, p.settings.forFront(s))
	}
	if len(p.logs) > 0 {
		out.Logs = append([]*protocol.PluginLog(nil), p.logs...)
	}
	out.StorageUsed = int64(p.store.used())
	out.StorageQuota = int64(p.m.lim.Storage)
	out.DbUsed = p.db.used()
	out.DbQuota = int64(p.m.lim.DBQuota)
	return out
}

// list is the grants in the manifest's order.
func (g grants) list(m *Manifest) []string {
	var out []string
	for _, p := range m.Permissions {
		if g[p] {
			out = append(out, p)
		}
	}
	return out
}

// grants are what the plugin holds now.
func (p *plugin) grants() grants {
	v := p.current()
	if v == nil {
		return grants{}
	}
	return grantsOf(v.man, p.m.grant.get(p.id).Granted)
}
