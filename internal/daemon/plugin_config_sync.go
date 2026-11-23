package daemon

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/plugin"
	"github.com/ironpark/mako/internal/plugin/store"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/syncstate"
	"github.com/ironpark/mako/internal/transport"
	"github.com/ironpark/mako/pkg/crdt"
	"github.com/ironpark/mako/pkg/fsutil"
)

// Peer-to-peer plugin config CRDT sync (revision 47).
//
// Replicates plugin registries, registry-installed plugin records (ID, registry,
// pinned publisher key, version), plugin disabled flags, and non-secret plugin
// settings between mutually trusted peers while Follow other nodes and Sync
// plugins are both enabled. Permissions/grants, secret settings, and local
// development plugin folders are strictly excluded and stay node-local.

var errPluginConfigSyncPeer = errors.New("plugin config sync is only accepted from a mutually trusted peer")

type pluginLocalBaseline struct {
	ready     bool
	added     map[string]bool
	noDefault bool
	installs  map[string]store.Install
	disabled  map[string]bool
	settings  map[string][]byte
}

type pluginConfigSync struct {
	mu      sync.Mutex
	applyMu sync.Mutex
	writeMu sync.Mutex

	dir    string
	path   string
	state  *protocol.PluginConfigSyncState
	digest []byte
	clock  *crdt.Clock
	base   pluginLocalBaseline
}

func (p *pluginConfigSync) load(dir, nodeID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dir = dir
	p.path = filepath.Join(dir, "plugin-config-sync.pb")
	st := &protocol.PluginConfigSyncState{}
	if data, err := os.ReadFile(p.path); err == nil {
		if proto.Unmarshal(data, st) != nil {
			st = &protocol.PluginConfigSyncState{}
		}
	}
	now := physNow()
	p.clock = crdt.NewClock(nodeID, true)
	syncstate.ObservePluginConfig(st, p.clock, now)
	healed := syncstate.HealPluginConfig(st, p.clock, now)
	p.state = st
	p.digest = syncstate.DigestPluginConfig(st)
	if healed {
		go p.save(proto.CloneOf(st))
	}
}

func (p *pluginConfigSync) save(snap *protocol.PluginConfigSyncState) {
	if p.path == "" || snap == nil {
		return
	}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	data, err := proto.Marshal(snap)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p.path), 0o700); err != nil {
		return
	}
	_ = fsutil.Write(p.path, data)
}

func (p *pluginConfigSync) snapshot() (*protocol.PluginConfigSyncState, []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state == nil {
		return &protocol.PluginConfigSyncState{}, nil
	}
	return proto.CloneOf(p.state), bytes.Clone(p.digest)
}

func (p *pluginConfigSync) currentDigest() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return bytes.Clone(p.digest)
}

func (p *pluginConfigSync) merge(remote *protocol.PluginConfigSyncState) (merged *protocol.PluginConfigSyncState, changed bool) {
	p.mu.Lock()
	if p.state == nil {
		p.state = &protocol.PluginConfigSyncState{}
	}
	if p.clock == nil {
		p.clock = crdt.NewClock("", true)
	}
	now := physNow()
	changed = syncstate.MergePluginConfig(p.state, remote, p.clock, now)
	if changed {
		p.digest = syncstate.DigestPluginConfig(p.state)
	}
	merged = proto.CloneOf(p.state)
	p.mu.Unlock()
	if changed {
		p.save(merged)
	}
	return merged, changed
}

func (d *Daemon) syncsPlugins() bool {
	return d.followed().workspace.SyncsPlugins(d.syncDir())
}

func (d *Daemon) syncDir() string {
	if d.dir != "" {
		return d.dir
	}
	return d.workspace.dir
}

func (d *Daemon) pluginConfigSyncDigest() []byte {
	if d.plugins == nil || d.store == nil || !d.syncsPlugins() {
		return nil
	}
	return d.pluginConfigSync.currentDigest()
}

func (d *Daemon) onPluginConfigLocalChange() {
	if d.captureLocalPluginConfig() {
		d.pokeStatus()
	}
}

// captureLocalPluginConfig diffs local store/manager state against both the
// CRDT state and the last observed local baseline, stamping CRDT updates only
// for genuine local additions, edits, or removals.
func (d *Daemon) captureLocalPluginConfig() bool {
	if d.plugins == nil || d.store == nil {
		return false
	}
	storeSnap := d.store.SyncSnapshot()
	disabledSnap := d.plugins.DisabledSnapshot()
	settingsSnap := d.plugins.NonSecretSettingsSnapshot()
	loaded := d.plugins.LoadedPluginIDs()

	p := &d.pluginConfigSync
	p.mu.Lock()
	if p.state == nil {
		p.state = &protocol.PluginConfigSyncState{}
	}
	if p.clock == nil {
		p.clock = crdt.NewClock(d.config().NodeID, true)
	}
	now := physNow()
	st := p.state
	changed := syncstate.HealPluginConfig(st, p.clock, now)

	curAdded := map[string]bool{}
	for _, u := range storeSnap.Added {
		curAdded[u] = true
	}

	// 1. Custom registries
	for u := range curAdded {
		if p.base.ready && p.base.added[u] {
			continue
		}
		if !syncstate.ElementLive(st.GetRegistries()[u], "plugin:reg:"+u) {
			if st.Registries == nil {
				st.Registries = map[string]*protocol.ElementMeta{}
			}
			m := st.Registries[u]
			if m == nil {
				m = &protocol.ElementMeta{}
				st.Registries[u] = m
			}
			m.AddedAt = syncstate.FromHLC(p.clock.Now(now))
			changed = true
		}
	}
	if p.base.ready {
		for u := range p.base.added {
			if !curAdded[u] && syncstate.ElementLive(st.GetRegistries()[u], "plugin:reg:"+u) {
				if st.Registries == nil {
					st.Registries = map[string]*protocol.ElementMeta{}
				}
				m := st.Registries[u]
				if m == nil {
					m = &protocol.ElementMeta{}
					st.Registries[u] = m
				}
				m.RemovedAt = syncstate.FromHLC(p.clock.Now(now))
				changed = true
			}
		}
	}

	// 2. Built-in default registry removal flag (unset by default on fresh nodes)
	if storeSnap.NoDefault {
		if (!p.base.ready || !p.base.noDefault) && (st.NoDefaultRegistry == nil || !st.NoDefaultRegistry.GetValue()) {
			st.NoDefaultRegistry = &protocol.BoolReg{Value: true, Clock: syncstate.FromHLC(p.clock.Now(now))}
			changed = true
		}
	} else if p.base.ready && p.base.noDefault {
		if st.NoDefaultRegistry == nil || st.NoDefaultRegistry.GetValue() {
			st.NoDefaultRegistry = &protocol.BoolReg{Value: false, Clock: syncstate.FromHLC(p.clock.Now(now))}
			changed = true
		}
	}

	// 3. Store-installed plugins
	for id, in := range storeSnap.Installs {
		if p.base.ready {
			if prev, hadPrev := p.base.installs[id]; hadPrev && prev.Registry == in.Registry && prev.Key == in.Key && prev.Version == in.Version {
				continue
			}
		}
		cur := st.GetInstalls()[id]
		if cur == nil || !cur.GetInstalled() || cur.GetRegistry() != in.Registry || cur.GetKey() != in.Key || cur.GetVersion() != in.Version {
			if st.Installs == nil {
				st.Installs = map[string]*protocol.PluginInstallSyncEntry{}
			}
			st.Installs[id] = &protocol.PluginInstallSyncEntry{
				Installed: true,
				Registry:  in.Registry,
				Key:       in.Key,
				Version:   in.Version,
				Clock:     syncstate.FromHLC(p.clock.Now(now)),
			}
			changed = true
		}
	}
	if p.base.ready {
		for id := range p.base.installs {
			if _, still := storeSnap.Installs[id]; !still {
				if cur := st.GetInstalls()[id]; cur == nil || cur.GetInstalled() {
					if st.Installs == nil {
						st.Installs = map[string]*protocol.PluginInstallSyncEntry{}
					}
					st.Installs[id] = &protocol.PluginInstallSyncEntry{
						Installed: false,
						Clock:     syncstate.FromHLC(p.clock.Now(now)),
					}
					changed = true
				}
			}
		}
	}

	// 4. Disabled states (only stamp true on initial scan, or changes relative to baseline)
	for id, off := range disabledSnap {
		cur := st.GetDisabled()[id]
		wasOff, hadBase := p.base.disabled[id]
		if off {
			if (!p.base.ready || !hadBase || !wasOff) && (cur == nil || !cur.GetValue()) {
				if st.Disabled == nil {
					st.Disabled = map[string]*protocol.BoolReg{}
				}
				st.Disabled[id] = &protocol.BoolReg{Value: true, Clock: syncstate.FromHLC(p.clock.Now(now))}
				changed = true
			}
		} else if p.base.ready && hadBase && wasOff {
			if cur == nil || cur.GetValue() {
				if st.Disabled == nil {
					st.Disabled = map[string]*protocol.BoolReg{}
				}
				st.Disabled[id] = &protocol.BoolReg{Value: false, Clock: syncstate.FromHLC(p.clock.Now(now))}
				changed = true
			}
		}
	}

	// 5. Non-secret settings for loaded plugins
	for k, val := range settingsSnap {
		if p.base.ready {
			if prev, hadPrev := p.base.settings[k]; hadPrev && bytes.Equal(prev, val) {
				continue
			}
		}
		cur := st.GetSettings()[k]
		if cur == nil || cur.GetDeleted() || !bytes.Equal(cur.GetValue(), val) {
			if st.Settings == nil {
				st.Settings = map[string]*protocol.BytesReg{}
			}
			st.Settings[k] = &protocol.BytesReg{
				Value: bytes.Clone(val),
				Clock: syncstate.FromHLC(p.clock.Now(now)),
			}
			changed = true
		}
	}
	if p.base.ready {
		for k := range p.base.settings {
			if _, still := settingsSnap[k]; !still {
				pluginID, _, _, ok := syncstate.ParseSettingSyncKey(k)
				// Only emit an unset tombstone if the plugin is still loaded here
				// (not simply uninstalled or mid-reload).
				if ok && loaded[pluginID] {
					if cur := st.GetSettings()[k]; cur == nil || !cur.GetDeleted() {
						if st.Settings == nil {
							st.Settings = map[string]*protocol.BytesReg{}
						}
						st.Settings[k] = &protocol.BytesReg{
							Deleted: true,
							Clock:   syncstate.FromHLC(p.clock.Now(now)),
						}
						changed = true
					}
				}
			}
		}
	}

	// Update local baseline.
	p.base.ready = true
	p.base.added = curAdded
	p.base.noDefault = storeSnap.NoDefault
	p.base.installs = store.CloneInstalls(storeSnap.Installs)
	p.base.disabled = maps.Clone(disabledSnap)
	p.base.settings = cloneBytesMap(settingsSnap)

	var snap *protocol.PluginConfigSyncState
	if changed {
		p.digest = syncstate.DigestPluginConfig(st)
		snap = proto.CloneOf(st)
	}
	p.mu.Unlock()
	if changed {
		p.save(snap)
	}
	return changed
}

// applyPluginConfigSync reconciles local registries, store installs, disabled
// flags, and non-secret settings to match the merged CRDT state.
func (d *Daemon) applyPluginConfigSync(ctx context.Context) {
	if d.plugins == nil || d.store == nil || !d.syncsPlugins() {
		return
	}
	p := &d.pluginConfigSync
	p.applyMu.Lock()
	defer p.applyMu.Unlock()

	st, _ := p.snapshot()
	if syncstate.EmptyPluginConfig(st) {
		return
	}

	anyChanged := false

	// 1. Reconcile registries first so installs can resolve custom registries.
	desiredRegs := syncstate.LiveRegistries(st)
	noDefault := st.GetNoDefaultRegistry().GetValue()
	if d.store.ApplySyncedRegistries(ctx, desiredRegs, noDefault) {
		anyChanged = true
	}

	// 2. Reconcile store-installed plugins (sorted for determinism).
	installIDs := slices.Sorted(maps.Keys(st.GetInstalls()))
	for _, id := range installIDs {
		if ctx.Err() != nil {
			return
		}
		entry := st.Installs[id]
		if entry.GetInstalled() {
			changed, err := d.store.EnsureSyncedInstall(ctx, id, entry.GetRegistry(), entry.GetKey(), entry.GetVersion())
			if err != nil {
				d.log.Warn("synced plugin install failed", "plugin", id, "registry", entry.GetRegistry(), "err", err)
			} else if changed {
				anyChanged = true
			}
		} else {
			changed, err := d.store.RemoveSyncedInstall(id)
			if err != nil {
				d.log.Warn("synced plugin removal failed", "plugin", id, "err", err)
			} else if changed {
				anyChanged = true
			}
		}
	}

	// 3. Reconcile disabled states (never touches grants/permissions).
	for _, id := range slices.Sorted(maps.Keys(st.GetDisabled())) {
		off := st.Disabled[id].GetValue()
		changed, err := d.plugins.ApplySyncedDisabled(id, off)
		if err != nil {
			d.log.Warn("synced plugin disabled state failed", "plugin", id, "err", err)
		} else if changed {
			anyChanged = true
		}
	}

	// 4. Reconcile non-secret settings for loaded plugins.
	if d.applySyncedPluginSettings(st) {
		anyChanged = true
	}

	// Refresh local baseline so applied state is never mistaken for a local edit.
	d.refreshPluginLocalBaseline()

	if anyChanged {
		d.changes.firePlugins()
		d.pokeStatus()
	}
}

func (d *Daemon) applySyncedPluginSettings(st *protocol.PluginConfigSyncState) bool {
	if d.plugins == nil || st == nil || len(st.GetSettings()) == 0 {
		return false
	}
	byPlugin := map[string][]plugin.SyncedSettingUpdate{}
	for k, reg := range st.GetSettings() {
		pid, scopeKey, skey, ok := syncstate.ParseSettingSyncKey(k)
		if !ok {
			continue
		}
		byPlugin[pid] = append(byPlugin[pid], plugin.SyncedSettingUpdate{
			ScopeKey: scopeKey,
			Key:      skey,
			Value:    reg.GetValue(),
			Deleted:  reg.GetDeleted(),
		})
	}
	changed := false
	for _, pid := range slices.Sorted(maps.Keys(byPlugin)) {
		if d.plugins.ApplySyncedSettings(pid, byPlugin[pid]) {
			changed = true
		}
	}
	return changed
}

// applyPendingPluginSettings applies any synced non-secret settings to plugins
// that just finished loading their manifest after a store install.
func (d *Daemon) applyPendingPluginSettings() {
	if d.plugins == nil || d.store == nil || !d.syncsPlugins() {
		return
	}
	if !d.pluginConfigSync.applyMu.TryLock() {
		return
	}
	defer d.pluginConfigSync.applyMu.Unlock()
	st, _ := d.pluginConfigSync.snapshot()
	if syncstate.EmptyPluginConfig(st) {
		return
	}
	if d.applySyncedPluginSettings(st) {
		d.refreshPluginLocalBaseline()
		d.changes.firePlugins()
	}
}

func (d *Daemon) refreshPluginLocalBaseline() {
	if d.plugins == nil || d.store == nil {
		return
	}
	storeSnap := d.store.SyncSnapshot()
	disabledSnap := d.plugins.DisabledSnapshot()
	settingsSnap := d.plugins.NonSecretSettingsSnapshot()

	curAdded := map[string]bool{}
	for _, u := range storeSnap.Added {
		curAdded[u] = true
	}
	p := &d.pluginConfigSync
	p.mu.Lock()
	p.base.ready = true
	p.base.added = curAdded
	p.base.noDefault = storeSnap.NoDefault
	p.base.installs = store.CloneInstalls(storeSnap.Installs)
	p.base.disabled = maps.Clone(disabledSnap)
	p.base.settings = cloneBytesMap(settingsSnap)
	p.mu.Unlock()
}

func (d *Daemon) takePluginConfigSyncDigest(fromNodeID string, remoteDigest []byte) {
	if d.plugins == nil || d.store == nil || !d.syncsPlugins() {
		return
	}
	localDigest := d.pluginConfigSync.currentDigest()
	if len(remoteDigest) == 0 && len(localDigest) == 0 {
		return
	}
	if bytes.Equal(remoteDigest, localDigest) {
		return
	}
	d.syncPluginConfigWithPeer(fromNodeID)
}

func (d *Daemon) syncPluginConfigWithPeer(nodeID string) {
	d.mu.Lock()
	if d.syncingPluginConfig == nil {
		d.syncingPluginConfig = map[string]bool{}
	}
	if d.syncingPluginConfig[nodeID] {
		d.mu.Unlock()
		return
	}
	d.syncingPluginConfig[nodeID] = true
	d.mu.Unlock()

	go func() {
		defer func() {
			d.mu.Lock()
			delete(d.syncingPluginConfig, nodeID)
			d.mu.Unlock()
		}()
		node, err := d.node(nodeID)
		if err != nil {
			return
		}
		if node.GetRevision() > 0 && node.GetRevision() < protocol.RevisionConfigSync {
			return
		}
		ctx, cancel := context.WithTimeout(d.ctx, 20*time.Second)
		defer cancel()
		stream, err := d.peerStream(ctx, node)
		if err != nil {
			return
		}
		defer stream.Close()

		d.captureLocalPluginConfig()
		localState, _ := d.pluginConfigSync.snapshot()
		remoteState, err := transport.Ask[*protocol.PluginConfigSyncState](ctx, stream, protocol.TypeSyncPluginConfig, &protocol.SyncPluginConfig{State: localState})
		if err != nil {
			return
		}
		if _, changed := d.pluginConfigSync.merge(remoteState); changed {
			applyCtx, applyCancel := context.WithTimeout(d.ctx, 2*time.Minute)
			defer applyCancel()
			d.applyPluginConfigSync(applyCtx)
		}
	}()
}

func (d *Daemon) handleSyncPluginConfig(_ context.Context, stream *transport.Stream, env *protocol.Envelope, fromPeer bool) error {
	if !fromPeer || d.plugins == nil || d.store == nil || !d.syncsPlugins() {
		return errPluginConfigSyncPeer
	}
	req, err := protocol.Body[*protocol.SyncPluginConfig](env)
	if err != nil {
		return err
	}
	d.captureLocalPluginConfig()
	merged, changed := d.pluginConfigSync.merge(req.GetState())
	if changed {
		go func() {
			ctx, cancel := context.WithTimeout(d.ctx, 2*time.Minute)
			defer cancel()
			d.applyPluginConfigSync(ctx)
		}()
	}
	return stream.Send(protocol.TypePluginConfigSyncState, env.GetId(), merged)
}

func cloneBytesMap(m map[string][]byte) map[string][]byte {
	if len(m) == 0 {
		return map[string][]byte{}
	}
	out := make(map[string][]byte, len(m))
	for k, v := range m {
		out[k] = bytes.Clone(v)
	}
	return out
}
