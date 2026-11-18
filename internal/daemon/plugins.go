package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ironpark/mako/internal/keychain"
	"github.com/ironpark/mako/internal/layoutdoc"
	"github.com/ironpark/mako/internal/plugin"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/testhooks"
	"github.com/ironpark/mako/internal/transport"
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
	Watch(ctx context.Context, name string, send func(*protocol.BlockUpdate) error) error
	Event(ctx context.Context, ev *protocol.BlockEvent) (*protocol.PluginDone, error)
}

// agentBlocks makes the agents' blocks a blockSource: an event goes to
// the agent as a message.
type agentBlocks struct{ d *Daemon }

func (a agentBlocks) Watch(ctx context.Context, name string, send func(*protocol.BlockUpdate) error) error {
	return a.d.agentUI.Watch(ctx, name, send)
}

func (a agentBlocks) Event(_ context.Context, ev *protocol.BlockEvent) (*protocol.PluginDone, error) {
	return a.d.agentUIEvent(ev)
}

// blockSource is the source of the block of the name.
func (d *Daemon) blockSource(name string) (blockSource, error) {
	switch {
	case strings.HasPrefix(name, "plugin:") && d.plugins != nil:
		return d.plugins, nil
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
	d.plugins = plugin.New(plugin.Config{
		Keychain:     kc,
		KeychainNote: note,
		Dir:          d.pluginsDir,
		DataDir:      filepath.Join(dir, "plugin-data"),
		Log:          d.log,
		Sessions:     d.listSessions,
		Transcript:   d.pluginTranscript,
		Projects:     d.pluginProjects,
		Agents:       pluginAgents{d},
		Layout:       pluginLayout{d},
		Changed:      d.changes.firePlugins,
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
	err = src.Watch(ctx, req.GetBlock(), func(u *protocol.BlockUpdate) error {
		return stream.Send(protocol.TypeBlockUpdate, env.GetId(), u)
	})
	if ctx.Err() != nil {
		return context.Canceled
	}
	return err
}
