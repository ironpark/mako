package daemon

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ironpark/mako/internal/keychain"
	"github.com/ironpark/mako/internal/layoutdoc"
	"github.com/ironpark/mako/internal/plugin"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/testhooks"
	"github.com/ironpark/mako/internal/transport"
	"github.com/ironpark/mako/internal/workspace"
)

// The node's plugins run here (internal/plugin), each a folder of
// plugins/ beside config.json; what each keeps on this node, and what the
// user allowed them, is in plugin-data/. A node's front-ends list them and
// act on them; a peer may not: what a plugin is allowed is the user's to
// say on the node it runs on. The blocks plugins and agents draw are
// served to a node let in as well (watch_block, block_event,
// list_agent_blocks, revision 30), routed by node_id: a node let in starts
// sessions there already, which run any program, so seeing a block and
// tapping it gives it nothing it lacked. A phone shows the blocks of the
// computer it shows so.

var errPluginPeer = errors.New("a node's plugins are its own front-ends' alone")

// blockSource serves the blocks of a namespace to the front-ends: a
// plugin's, plugin:<id>/<block>, or an agent's, agent:<session>/<id>. Both
// are trees of one vocabulary (internal/uiblock), sent whole and then as
// patches; the window draws them alike, framed with where they come from.
type blockSource interface {
	Watch(ctx context.Context, name, scope, locale string, send func(*protocol.BlockUpdate) error) error
	Event(ctx context.Context, ev *protocol.BlockEvent) (*protocol.PluginDone, error)
}

// pluginBlocks adapts *plugin.Manager to blockSource, forwarding the watch
// request's canonical scope key and locale to WatchScopedLocale.
type pluginBlocks struct{ m *plugin.Manager }

func (p pluginBlocks) Watch(ctx context.Context, name, scope, locale string, send func(*protocol.BlockUpdate) error) error {
	return p.m.WatchScopedLocale(ctx, name, scope, locale, send)
}

func (p pluginBlocks) Event(ctx context.Context, ev *protocol.BlockEvent) (*protocol.PluginDone, error) {
	return p.m.Event(ctx, ev)
}

// agentBlocks makes the agents' blocks a blockSource: an event goes to
// the agent as a message. Agent blocks are already session-addressed by name
// so scope and locale are safely ignored.
type agentBlocks struct{ d *Daemon }

func (a agentBlocks) Watch(ctx context.Context, name, _, _ string, send func(*protocol.BlockUpdate) error) error {
	return a.d.agentUI.Watch(ctx, name, send)
}

func (a agentBlocks) Event(_ context.Context, ev *protocol.BlockEvent) (*protocol.PluginDone, error) {
	return a.d.agentUIEvent(ev)
}

// blockSource is the source of the block of the name.
func (d *Daemon) blockSource(name string) (blockSource, error) {
	switch {
	case strings.HasPrefix(name, "plugin:") && d.plugins != nil:
		return pluginBlocks{d.plugins}, nil
	case strings.HasPrefix(name, "agent:"):
		return agentBlocks{d}, nil
	case strings.HasPrefix(name, "plugin:"):
		return nil, &protocol.CodedError{Code: protocol.CodeNoPlugin, Message: "this node runs no plugins"}
	}
	return nil, fmt.Errorf("%q is no plugin's or agent's block", name)
}

// startPlugins makes the manager of the plugins in dir, and runs it until
// ctx ends.
func (d *Daemon) startPlugins(ctx context.Context, dir string) (done <-chan struct{}) {
	d.pluginsDir = filepath.Join(dir, "plugins")
	d.store = newPluginStore(filepath.Join(dir, "plugin-data"), d.log)
	// Plugins' secrets go in the system's keychain; a node without one
	// keeps them in a file of each plugin's, and says so.
	kc, why := keychain.System()
	note := ""
	if why != nil {
		note = why.Error()
		d.log.Info("plugins' secrets are kept in files: no keychain", "why", note)
	}
	d.initDecisionStore(dir, kc, note)
	d.plugins = plugin.New(plugin.Config{
		Keychain:     kc,
		KeychainNote: note,
		Dir:          d.pluginsDir,
		DataDir:      filepath.Join(dir, "plugin-data"),
		Log:          d.log,
		Sessions:     d.listSessions,
		Transcript:   d.pluginTranscript,
		Projects:     d.pluginProjects,
		ResolveScope: d.resolvePluginScope,
		Agents:       pluginAgents{d},
		Layout:       pluginLayout{d},
		NodeID:       d.cfg.NodeID,
		SyncChanged:  func() { d.pokeStatus() },
		Changed:      func() { d.changes.firePlugins(); d.syncProjectAgents() },
		Installs:     d.store,
		NetTest:      testhooks.NetTest(),
	})
	d.store.Attach(d.plugins)
	// What a daemon stopped midway through an install left is settled
	// before the folder is first read.
	d.store.Reconcile()
	ch := make(chan struct{})
	d.projectAgents.messages.added = d.pluginMessage
	go func() {
		defer close(ch)
		go d.store.Run(ctx)
		go d.feedPluginEvents(ctx)
		d.plugins.Run(ctx)
	}()
	return ch
}

// pluginDir is where a plugin in development is written: a folder of
// the daemon's, made if need be, so that an agent can write one into it.
func (d *Daemon) pluginDir() string {
	if d.pluginsDir == "" {
		return ""
	}
	_ = os.MkdirAll(d.pluginsDir, 0o755)
	return d.pluginsDir
}

// blockCatalog is every block a layout document may place on this node:
// the window's own and the plugins'.
func (d *Daemon) blockCatalog() layoutdoc.Catalog { return catalog{d} }

type catalog struct{ d *Daemon }

func (c catalog) BlockProps(name string) (map[string]any, bool) {
	if props, ok := layoutdoc.BuiltinCatalog().BlockProps(name); ok {
		return props, true
	}
	if props, ok := c.d.agentUI.BlockProps(name); ok {
		return props, true
	}
	if c.d.plugins != nil {
		return c.d.plugins.BlockProps(name)
	}
	return nil, false
}

// blocks is the catalog as list_blocks gives it.
func (d *Daemon) blocks() []layoutdoc.Block {
	out := layoutdoc.Builtin()
	if d.plugins != nil {
		out = append(out, d.plugins.Blocks()...)
	}
	return out
}

func (d *Daemon) handleListPlugins(ctx context.Context, stream *transport.Stream, env *protocol.Envelope, fromPeer bool) error {
	if fromPeer || d.plugins == nil {
		return errPluginPeer
	}
	store, note := d.plugins.SecretStore()
	return stream.Send(protocol.TypePlugins, env.GetId(), &protocol.Plugins{Plugins: d.plugins.List(), Secrets: store, SecretsNote: note})
}

func (d *Daemon) handlePluginAction(ctx context.Context, stream *transport.Stream, env *protocol.Envelope, fromPeer bool) error {
	if fromPeer || d.plugins == nil {
		return errPluginPeer
	}
	req, err := protocol.Body[*protocol.PluginAction](env)
	if err != nil {
		return err
	}
	done, err := d.plugins.Act(ctx, req)
	if err != nil {
		return err
	}
	return stream.Send(protocol.TypePluginDone, env.GetId(), done)
}

// handleBlockEvent gives the block's source the user's action on a node
// of its tree, on the node the block is on.
func (d *Daemon) handleBlockEvent(ctx context.Context, stream *transport.Stream, env *protocol.Envelope, _ bool) error {
	return serve(d, ctx, stream, env, protocol.TypePluginDone, func(req *protocol.BlockEvent) (*protocol.PluginDone, error) {
		src, err := d.blockSource(req.GetBlock())
		if err != nil {
			return nil, err
		}
		return src.Event(ctx, req)
	})
}

// handleWatchBlock streams a block's tree until the front-end hangs up: a
// block of this node's, or, relayed, of the node the request names.
func (d *Daemon) handleWatchBlock(ctx context.Context, stream *transport.Stream, env *protocol.Envelope, _ bool) error {
	req, err := protocol.Body[*protocol.WatchBlock](env)
	if err != nil {
		return err
	}
	if !d.local(req.GetNodeId()) {
		return d.relayToPeer(ctx, protocol.TypeWatchBlock, req, stream)
	}
	src, err := d.blockSource(req.GetBlock())
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	gone := hungUp(stream)
	go func() {
		<-gone
		cancel()
	}()
	err = src.Watch(ctx, req.GetBlock(), req.GetScope(), req.GetLocale(), func(u *protocol.BlockUpdate) error {
		return stream.Send(protocol.TypeBlockUpdate, env.GetId(), u)
	})
	if ctx.Err() != nil {
		return context.Canceled
	}
	return err
}

// resolvePluginScope enriches a canonical plugin scope key ("space:<id>",
// "project:<id>", "session:<id>") with metadata from the daemon's workspace
// state, projects, and sessions.
func (d *Daemon) resolvePluginScope(scopeKey string) plugin.ScopeContext {
	if scopeKey == "" {
		return plugin.ScopeContext{Scope: protocol.ScopeGlobal, Roots: []string{}}
	}
	kind, id, ok := strings.Cut(scopeKey, ":")
	if !ok || id == "" {
		return plugin.ScopeContext{Scope: protocol.ScopeGlobal, Roots: []string{}}
	}
	st := d.workspace.get(false).GetState()
	switch kind {
	case protocol.ScopeSpace:
		sc := plugin.ScopeContext{
			Scope:    protocol.ScopeSpace,
			ScopeKey: scopeKey,
			SpaceID:  id,
			Roots:    []string{},
		}
		for _, sp := range workspace.Spaces(st) {
			if sp.GetId() == id {
				sc.SpaceName = sp.GetName().GetValue()
				break
			}
		}
		return sc
	case protocol.ScopeProject:
		sc := plugin.ScopeContext{
			Scope:     protocol.ScopeProject,
			ScopeKey:  scopeKey,
			ProjectID: id,
			Roots:     []string{},
		}
		name, roots := d.projectMeta(st, id)
		sc.ProjectName = name
		if len(roots) > 0 {
			sc.Roots = roots
		}
		sc.SpaceID, sc.SpaceName = d.spaceForProject(st, id)
		return sc
	case protocol.ScopeSession:
		var info *protocol.SessionInfo
		if d.sessions != nil {
			if s, ok := d.sessions.get(id); ok {
				info = s.Info()
			}
		}
		if info == nil {
			d.sessionMu.Lock()
			if saved, ok := d.saved[id]; ok {
				info = saved.Info
			}
			d.sessionMu.Unlock()
		}
		return d.sessionScopeContext(id, info)
	default:
		return plugin.ScopeContext{Scope: protocol.ScopeGlobal, Roots: []string{}}
	}
}

// sessionScopeContext derives a strict ScopeContext for a session from its
// SessionInfo, matching project roots and owning space.
func (d *Daemon) sessionScopeContext(sessionID string, info *protocol.SessionInfo) plugin.ScopeContext {
	sc := plugin.ScopeContext{
		Scope:     protocol.ScopeSession,
		ScopeKey:  protocol.ScopeSession + ":" + sessionID,
		SessionID: sessionID,
		Roots:     []string{},
	}
	if info == nil || info.GetProject() == "" {
		return sc
	}
	st := d.workspace.get(false).GetState()
	repo := cmp.Or(info.GetWorktree().GetRepo(), info.GetRepo())
	proj, _ := d.projectFor(info.GetProject(), repo)
	if proj == nil && st != nil && d.cfg != nil {
		var tmp projectStore
		tmp.resolveLocked(workspace.Scopes(st, d.cfg.NodeID))
		r := repo
		if r == "" {
			r = d.heads.of(info.GetProject()).repo
		}
		if r != "" {
			r = cleanPath(r)
		}
		proj, _ = tmp.projectOf(cleanPath(info.GetProject()), r)
	}
	if proj != nil {
		sc.ProjectID = proj.GetId()
		name, roots := d.projectMeta(st, proj.GetId())
		sc.ProjectName = cmp.Or(proj.GetName(), name)
		if len(proj.GetPaths()) > 0 {
			sc.Roots = slices.Clone(proj.GetPaths())
		} else if len(roots) > 0 {
			sc.Roots = roots
		}
		sc.SpaceID, sc.SpaceName = d.spaceForProject(st, proj.GetId())
		return sc
	}
	sc.Roots = []string{info.GetProject()}
	return sc
}

// projectMeta looks up a project's display name and local roots from the
// daemon's project store, falling back to workspace state when needed.
func (d *Daemon) projectMeta(st *protocol.WorkspaceState, projectID string) (name string, roots []string) {
	for _, p := range d.projects.get() {
		if p.GetId() == projectID {
			name = p.GetName()
			roots = slices.Clone(p.GetPaths())
			break
		}
	}
	if st != nil {
		for _, wp := range workspace.Projects(st) {
			if wp.GetId() == projectID {
				if name == "" {
					name = wp.GetName().GetValue()
				}
				if len(roots) == 0 && d.cfg != nil {
					for _, r := range workspace.Roots(wp) {
						for _, key := range workspace.Members(r.GetLocations()) {
							if n, path, ok := workspace.SplitLocation(key); ok && n == d.cfg.NodeID && path != "" && !slices.Contains(roots, path) {
								roots = append(roots, path)
							}
						}
					}
				}
				break
			}
		}
	}
	return name, roots
}

// spaceForProject returns the ID and name of the visible space containing
// projectID, or empty strings if the project is not assigned to a space.
func (d *Daemon) spaceForProject(st *protocol.WorkspaceState, projectID string) (spaceID, spaceName string) {
	if st == nil || projectID == "" {
		return "", ""
	}
	for _, sp := range workspace.Spaces(st) {
		if slices.Contains(workspace.SpaceProjects(st, sp), projectID) {
			return sp.GetId(), sp.GetName().GetValue()
		}
	}
	return "", ""
}
