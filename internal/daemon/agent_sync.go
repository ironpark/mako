package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/mcp"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/skills"
	"github.com/ironpark/mako/internal/syncstate"
	"github.com/ironpark/mako/internal/transport"
	"github.com/ironpark/mako/pkg/crdt"
	"github.com/ironpark/mako/pkg/fsutil"
)

// Peer-to-peer agent config CRDT sync (revision 47).
//
// Replicates agent definitions and profiles (excluding env), MCP context
// servers (excluding command env), built-in skill overrides (skills/<name>/SKILL.md),
// and custom prompt templates (templates/*.md) across mutually trusted peers
// while Follow other nodes and Sync agents are both enabled.

const maxSyncedFileBytes = 512 << 10

var errAgentSyncPeer = errors.New("agent config sync is only accepted from a mutually trusted peer")

type agentLocalBaseline struct {
	ready     bool
	agents    map[string][]byte
	servers   map[string][]byte
	skills    map[string][]byte
	templates map[string][]byte
}

type agentConfigSync struct {
	mu      sync.Mutex
	applyMu sync.Mutex
	writeMu sync.Mutex

	dir    string
	path   string
	state  *protocol.AgentSyncState
	digest []byte
	clock  *crdt.Clock
	base   agentLocalBaseline
}

func (a *agentConfigSync) load(dir, nodeID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.dir = dir
	a.path = filepath.Join(dir, "agent-sync.pb")
	st := &protocol.AgentSyncState{}
	if data, err := os.ReadFile(a.path); err == nil {
		if proto.Unmarshal(data, st) != nil {
			st = &protocol.AgentSyncState{}
		}
	}
	now := physNow()
	a.clock = crdt.NewClock(nodeID, true)
	syncstate.ObserveAgentConfig(st, a.clock, now)
	healed := syncstate.HealAgentConfig(st, a.clock, now)
	a.state = st
	a.digest = syncstate.DigestAgentConfig(st)
	if healed {
		go a.save(proto.CloneOf(st))
	}
}

func (a *agentConfigSync) save(snap *protocol.AgentSyncState) {
	if a.path == "" || snap == nil {
		return
	}
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	data, err := proto.Marshal(snap)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(a.path), 0o700); err != nil {
		return
	}
	_ = fsutil.Write(a.path, data)
}

func (a *agentConfigSync) snapshot() (*protocol.AgentSyncState, []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state == nil {
		return &protocol.AgentSyncState{}, nil
	}
	return proto.CloneOf(a.state), bytes.Clone(a.digest)
}

func (a *agentConfigSync) currentDigest() []byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	return bytes.Clone(a.digest)
}

func (a *agentConfigSync) merge(remote *protocol.AgentSyncState) (merged *protocol.AgentSyncState, changed bool) {
	a.mu.Lock()
	if a.state == nil {
		a.state = &protocol.AgentSyncState{}
	}
	if a.clock == nil {
		a.clock = crdt.NewClock("", true)
	}
	now := physNow()
	changed = syncstate.MergeAgentConfig(a.state, remote, a.clock, now)
	if changed {
		a.digest = syncstate.DigestAgentConfig(a.state)
	}
	merged = proto.CloneOf(a.state)
	a.mu.Unlock()
	if changed {
		a.save(merged)
	}
	return merged, changed
}

func (d *Daemon) syncsAgents() bool {
	return d.followed().workspace.SyncsAgents(d.syncDir())
}

// followedSnapshot returns the current in-memory agentFile config without
// triggering disk reloads or onConfigReloaded callbacks. Safe to call while
// holding d.agentSync.applyMu.
func (d *Daemon) followedSnapshot() followedConfig {
	f := &d.agentFile
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.read {
		f.setFromConfigLocked(d.config())
	}
	return followedConfig{
		agents:      cloneAgentsMap(f.agents),
		isolateHome: f.isolateHome,
		shell:       cloneShell(f.shell),
		servers:     cloneContextServers(f.servers),
		workspace:   cloneWorkspace(f.workspace),
		decision:    cloneDecision(f.decision),
	}
}

func (d *Daemon) syncsAgentsSnapshot() bool {
	return d.followedSnapshot().workspace.SyncsAgents(d.syncDir())
}

func (d *Daemon) skillsDir() string {
	if dir := d.syncDir(); dir != "" {
		return filepath.Join(dir, "skills")
	}
	dir, _ := skills.OverridesDir()
	return dir
}

func (d *Daemon) templatesDir() string {
	if dir := d.syncDir(); dir != "" {
		return filepath.Join(dir, "templates")
	}
	dir, _ := config.TemplatesDir()
	return dir
}

func (d *Daemon) agentSyncDigest() []byte {
	if !d.syncsAgents() {
		return nil
	}
	return d.agentSync.currentDigest()
}

func (d *Daemon) readCurrentAgentSnapshot() (curAgents map[string]agentspec.Agent, agentsMap, serversMap, skillsMap, templatesMap map[string][]byte) {
	f := d.followedSnapshot()
	curAgents = f.agents
	agentsMap = map[string][]byte{}
	for name, ag := range f.agents {
		if name == "" || name == agentspec.ShellName || !agentspec.ValidName(name) {
			continue
		}
		if b, err := syncstate.MarshalAgent(ag); err == nil {
			agentsMap[name] = b
		}
	}
	serversMap = map[string][]byte{}
	for id, srv := range f.servers {
		if id == "" {
			continue
		}
		if b, err := syncstate.MarshalMCPServer(srv); err == nil {
			serversMap[id] = b
		}
	}
	skillsMap = map[string][]byte{}
	if sdir := d.skillsDir(); sdir != "" {
		for _, name := range skills.Names() {
			p := filepath.Join(sdir, name, "SKILL.md")
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Size() <= maxSyncedFileBytes {
				if data, err := os.ReadFile(p); err == nil {
					skillsMap[name] = data
				}
			}
		}
	}
	templatesMap = map[string][]byte{}
	if tdir := d.templatesDir(); tdir != "" {
		if entries, err := os.ReadDir(tdir); err == nil {
			for _, e := range entries {
				name := e.Name()
				if e.IsDir() || !syncstate.ValidTemplateFilename(name) {
					continue
				}
				p := filepath.Join(tdir, name)
				if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Size() <= maxSyncedFileBytes {
					if data, err := os.ReadFile(p); err == nil {
						templatesMap[name] = data
					}
				}
			}
		}
	}
	return curAgents, agentsMap, serversMap, skillsMap, templatesMap
}

// captureLocalAgentConfig diffs local agent definitions, MCP servers, skill
// overrides, and prompt templates against the CRDT state and baseline, stamping
// only real local customizations, edits, and deletions.
func (d *Daemon) captureLocalAgentConfig() bool {
	_ = d.followed()
	d.agentSync.applyMu.Lock()
	defer d.agentSync.applyMu.Unlock()
	return d.captureLocalAgentConfigLocked()
}

// captureLocalAgentConfigLocked runs captureLocalAgentConfig while the caller
// already holds d.agentSync.applyMu.
func (d *Daemon) captureLocalAgentConfigLocked() bool {
	curAgents, agentsSnap, serversSnap, skillsSnap, templatesSnap := d.readCurrentAgentSnapshot()

	a := &d.agentSync
	a.mu.Lock()
	if a.state == nil {
		a.state = &protocol.AgentSyncState{}
	}
	if a.clock == nil {
		a.clock = crdt.NewClock(d.config().NodeID, true)
	}
	now := physNow()
	st := a.state
	changed := syncstate.HealAgentConfig(st, a.clock, now)

	// 1. Agents (skip untouched built-in defaults unless already tracked in CRDT state,
	// and once baseline is ready only stamp entries that actually changed locally)
	for name, val := range agentsSnap {
		cur := st.GetAgents()[name]
		if a.base.ready {
			prevVal, hadPrev := a.base.agents[name]
			localChanged := (!hadPrev && !syncstate.IsDefaultAgent(name, curAgents[name])) || (hadPrev && !bytes.Equal(prevVal, val))
			if !localChanged {
				continue
			}
		} else if cur == nil && syncstate.IsDefaultAgent(name, curAgents[name]) {
			continue
		}
		if cur == nil || cur.GetDeleted() || !bytes.Equal(cur.GetValue(), val) {
			if st.Agents == nil {
				st.Agents = map[string]*protocol.BytesReg{}
			}
			st.Agents[name] = &protocol.BytesReg{
				Value: bytes.Clone(val),
				Clock: syncstate.FromHLC(a.clock.Now(now)),
			}
			changed = true
		}
	}
	if a.base.ready {
		for name := range a.base.agents {
			if _, still := agentsSnap[name]; !still {
				if cur := st.GetAgents()[name]; cur != nil && !cur.GetDeleted() {
					st.Agents[name] = &protocol.BytesReg{
						Deleted: true,
						Clock:   syncstate.FromHLC(a.clock.Now(now)),
					}
					changed = true
				}
			}
		}
	}

	// 2. MCP Context Servers, Skill Overrides, and Prompt Templates
	syncBytesMap := func(dst *map[string]*protocol.BytesReg, curSnap, prevSnap map[string][]byte) {
		for k, val := range curSnap {
			if a.base.ready {
				if prevVal, hadPrev := prevSnap[k]; hadPrev && bytes.Equal(prevVal, val) {
					continue
				}
			}
			cur := (*dst)[k]
			if cur == nil || cur.GetDeleted() || !bytes.Equal(cur.GetValue(), val) {
				if *dst == nil {
					*dst = map[string]*protocol.BytesReg{}
				}
				(*dst)[k] = &protocol.BytesReg{
					Value: bytes.Clone(val),
					Clock: syncstate.FromHLC(a.clock.Now(now)),
				}
				changed = true
			}
		}
		if a.base.ready {
			for k := range prevSnap {
				if _, still := curSnap[k]; !still {
					if cur := (*dst)[k]; cur != nil && !cur.GetDeleted() {
						(*dst)[k] = &protocol.BytesReg{
							Deleted: true,
							Clock:   syncstate.FromHLC(a.clock.Now(now)),
						}
						changed = true
					}
				}
			}
		}
	}
	syncBytesMap(&st.ContextServers, serversSnap, a.base.servers)
	syncBytesMap(&st.Skills, skillsSnap, a.base.skills)
	syncBytesMap(&st.Templates, templatesSnap, a.base.templates)

	a.base.ready = true
	a.base.agents = cloneBytesMap(agentsSnap)
	a.base.servers = cloneBytesMap(serversSnap)
	a.base.skills = cloneBytesMap(skillsSnap)
	a.base.templates = cloneBytesMap(templatesSnap)

	var snap *protocol.AgentSyncState
	if changed {
		a.digest = syncstate.DigestAgentConfig(st)
		snap = proto.CloneOf(st)
	}
	a.mu.Unlock()
	if changed {
		a.save(snap)
	}
	return changed
}

// applyAgentConfigSync reconciles local config.json (agents & context_servers,
// preserving local env), skill overrides, and prompt templates to match the
// merged CRDT state.
func (d *Daemon) applyAgentConfigSync() {
	a := &d.agentSync
	a.applyMu.Lock()
	if !d.syncsAgentsSnapshot() {
		a.applyMu.Unlock()
		return
	}

	st, _ := a.snapshot()
	if syncstate.EmptyAgentConfig(st) {
		a.applyMu.Unlock()
		return
	}

	configChanged, err := d.applySyncedAgentsAndServers(st.GetAgents(), st.GetContextServers())
	if err != nil {
		d.log.Warn("synced agent config not applied", "err", err)
	}
	skillsChanged := d.applySyncedSkills(st.GetSkills())
	templatesChanged := d.applySyncedTemplates(st.GetTemplates())

	// Refresh baseline so applied files are not re-stamped as new local edits.
	d.refreshAgentLocalBaselineLocked()
	a.applyMu.Unlock()

	if skillsChanged {
		go d.syncBuiltinSkills(d.ctx)
	}
	if configChanged || skillsChanged || templatesChanged {
		d.syncProjectAgents()
		d.changes.fireConfig()
		d.pokeStatus()
	}
}

func (d *Daemon) refreshAgentLocalBaselineLocked() {
	_, agentsSnap, serversSnap, skillsSnap, templatesSnap := d.readCurrentAgentSnapshot()
	a := &d.agentSync
	a.mu.Lock()
	a.base.ready = true
	a.base.agents = cloneBytesMap(agentsSnap)
	a.base.servers = cloneBytesMap(serversSnap)
	a.base.skills = cloneBytesMap(skillsSnap)
	a.base.templates = cloneBytesMap(templatesSnap)
	a.mu.Unlock()
}

func (d *Daemon) applySyncedAgentsAndServers(agentRegs, serverRegs map[string]*protocol.BytesReg) (bool, error) {
	if len(agentRegs) == 0 && len(serverRegs) == 0 {
		return false, nil
	}
	f := &d.agentFile
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.read {
		f.setFromConfigLocked(d.config())
	}

	// Read current config (from disk if FollowAgents is active, else memory).
	cfg := cloneConfig(d.config())
	if f.path != "" {
		if body, err := os.ReadFile(f.path); err == nil {
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(body, &raw); err != nil {
				return false, err
			}
			if _, ok := raw["agents"]; ok {
				cfg.Agents = nil
			}
			if _, ok := raw["context_servers"]; ok {
				cfg.ContextServers = nil
			}
			if err := json.Unmarshal(body, cfg); err != nil {
				return false, err
			}
			cfg.TakeShell()
		} else if !os.IsNotExist(err) {
			return false, err
		}
	}

	changed := false
	if len(agentRegs) > 0 {
		if cfg.Agents == nil {
			cfg.Agents = map[string]agentspec.Agent{}
		}
		for name, reg := range agentRegs {
			if name == "" || name == agentspec.ShellName || !agentspec.ValidName(name) {
				continue
			}
			localPrev, hadLocal := cfg.Agents[name]
			if reg.GetDeleted() {
				if hadLocal {
					if defCmd, builtin := agentspec.Builtin[name]; builtin {
						restored := syncstate.MergeLocalAgentEnv(agentspec.Agent{Command: slices.Clone(defCmd)}, localPrev)
						if !syncstate.SameAgent(localPrev, restored) {
							cfg.Agents[name] = restored
							changed = true
						}
					} else {
						delete(cfg.Agents, name)
						changed = true
					}
				}
				continue
			}
			var synced agentspec.Agent
			if err := json.Unmarshal(reg.GetValue(), &synced); err != nil {
				continue
			}
			next := syncstate.MergeLocalAgentEnv(synced, localPrev)
			if !hadLocal || !syncstate.SameAgent(localPrev, next) {
				cfg.Agents[name] = next
				changed = true
			}
		}
	}

	if len(serverRegs) > 0 {
		if cfg.ContextServers == nil {
			cfg.ContextServers = map[string]mcp.Server{}
		}
		for id, reg := range serverRegs {
			if id == "" {
				continue
			}
			localPrev, hadLocal := cfg.ContextServers[id]
			if reg.GetDeleted() {
				if hadLocal {
					delete(cfg.ContextServers, id)
					changed = true
				}
				continue
			}
			var synced mcp.Server
			if err := json.Unmarshal(reg.GetValue(), &synced); err != nil {
				continue
			}
			next := syncstate.MergeLocalMCPEnv(synced, localPrev)
			if !hadLocal || !syncstate.SameMCPServer(localPrev, next) {
				cfg.ContextServers[id] = next
				changed = true
			}
		}
	}

	if !changed {
		return false, nil
	}

	if f.path != "" {
		if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
			return false, err
		}
		body, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return false, err
		}
		payload := append(body, '\n')
		if err := fsutil.Write(f.path, payload); err != nil {
			return false, err
		}
		var written config.Config
		if err := json.Unmarshal(payload, &written); err != nil {
			return false, err
		}
		written.TakeShell()
		f.setFromConfigLocked(&written)
		f.sum = sha256.Sum256(payload)
		f.hasSum = true
		f.hasBadSum = false
		if fi, err := os.Stat(f.path); err == nil {
			f.statMod, f.statSize, f.hasStat = fi.ModTime(), fi.Size(), true
		} else {
			f.hasStat = false
		}
	} else {
		f.setFromConfigLocked(cfg)
	}
	d.syncConfigFromAgentFileLocked(f)
	return true, nil
}

func (d *Daemon) applySyncedSkills(skillRegs map[string]*protocol.BytesReg) bool {
	if len(skillRegs) == 0 {
		return false
	}
	sdir := d.skillsDir()
	if sdir == "" {
		return false
	}
	changed := false
	for name, reg := range skillRegs {
		if !syncstate.ValidSkillName(name) {
			continue
		}
		if _, ok := skills.BuiltinByName(name); !ok {
			continue
		}
		dst := filepath.Join(sdir, name, "SKILL.md")
		if reg.GetDeleted() {
			if _, err := os.Stat(dst); err == nil {
				if os.Remove(dst) == nil {
					_ = os.Remove(filepath.Dir(dst))
					changed = true
				}
			}
			continue
		}
		if len(reg.GetValue()) == 0 || len(reg.GetValue()) > maxSyncedFileBytes {
			continue
		}
		if existing, err := os.ReadFile(dst); err == nil && bytes.Equal(existing, reg.GetValue()) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			d.log.Warn("synced skill mkdir failed", "skill", name, "err", err)
			continue
		}
		if err := fsutil.Write(dst, reg.GetValue()); err != nil {
			d.log.Warn("synced skill write failed", "skill", name, "err", err)
			continue
		}
		changed = true
	}
	return changed
}

func (d *Daemon) applySyncedTemplates(tmplRegs map[string]*protocol.BytesReg) bool {
	if len(tmplRegs) == 0 {
		return false
	}
	tdir := d.templatesDir()
	if tdir == "" {
		return false
	}
	changed := false
	for file, reg := range tmplRegs {
		if !syncstate.ValidTemplateFilename(file) {
			continue
		}
		dst := filepath.Join(tdir, file)
		if reg.GetDeleted() {
			if _, err := os.Stat(dst); err == nil {
				if os.Remove(dst) == nil {
					changed = true
				}
			}
			continue
		}
		if len(reg.GetValue()) > maxSyncedFileBytes {
			continue
		}
		if existing, err := os.ReadFile(dst); err == nil && bytes.Equal(existing, reg.GetValue()) {
			continue
		}
		if err := os.MkdirAll(tdir, 0o755); err != nil {
			d.log.Warn("synced templates mkdir failed", "err", err)
			continue
		}
		if err := fsutil.Write(dst, reg.GetValue()); err != nil {
			d.log.Warn("synced template write failed", "template", file, "err", err)
			continue
		}
		changed = true
	}
	return changed
}

func (d *Daemon) takeAgentSyncDigest(fromNodeID string, remoteDigest []byte) {
	if !d.syncsAgents() {
		return
	}
	localDigest := d.agentSync.currentDigest()
	if len(remoteDigest) == 0 && len(localDigest) == 0 {
		return
	}
	if bytes.Equal(remoteDigest, localDigest) {
		return
	}
	d.syncAgentConfigWithPeer(fromNodeID)
}

func (d *Daemon) syncAgentConfigWithPeer(nodeID string) {
	d.mu.Lock()
	if d.syncingAgentConfig == nil {
		d.syncingAgentConfig = map[string]bool{}
	}
	if d.syncingAgentConfig[nodeID] {
		d.mu.Unlock()
		return
	}
	d.syncingAgentConfig[nodeID] = true
	d.mu.Unlock()

	go func() {
		defer func() {
			d.mu.Lock()
			delete(d.syncingAgentConfig, nodeID)
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

		d.captureLocalAgentConfig()
		localState, _ := d.agentSync.snapshot()
		remoteState, err := transport.Ask[*protocol.AgentSyncState](ctx, stream, protocol.TypeSyncAgentConfig, &protocol.SyncAgentConfig{State: localState})
		if err != nil {
			return
		}
		if _, changed := d.agentSync.merge(remoteState); changed {
			d.applyAgentConfigSync()
		}
	}()
}

func (d *Daemon) handleSyncAgentConfig(_ context.Context, stream *transport.Stream, env *protocol.Envelope, fromPeer bool) error {
	if !fromPeer || !d.syncsAgents() {
		return errAgentSyncPeer
	}
	req, err := protocol.Body[*protocol.SyncAgentConfig](env)
	if err != nil {
		return err
	}
	d.captureLocalAgentConfig()
	merged, changed := d.agentSync.merge(req.GetState())
	if changed {
		d.applyAgentConfigSync()
	}
	return stream.Send(protocol.TypeAgentSyncState, env.GetId(), merged)
}
