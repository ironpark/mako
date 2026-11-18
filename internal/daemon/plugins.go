package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ironpark/mako/internal/layoutdoc"
	"github.com/ironpark/mako/internal/plugin"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transport"
)

// The node's plugins run here (internal/plugin), each a folder of
// plugins/ beside config.json; what each keeps on this node, and what the
// user allowed them, is in plugin-data/. A node's front-ends list them,
// act on them, and watch their blocks; a peer may not: plugins are a
// node's own, as its sessions' processes are.

var (
	errPluginPeer = errors.New("a node's plugins are its own front-ends' alone")
	errBlocksPeer = errors.New("a node's blocks are its own front-ends' alone")
)

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
	}
	return nil, fmt.Errorf("%q is no plugin's or agent's block", name)
}

// startPlugins makes the manager of the plugins in dir, and runs it until
// ctx ends.
func (d *Daemon) startPlugins(ctx context.Context, dir string) (done <-chan struct{}) {
	d.pluginsDir = filepath.Join(dir, "plugins")
	d.plugins = plugin.New(plugin.Config{
		Dir:      d.pluginsDir,
		DataDir:  filepath.Join(dir, "plugin-data"),
		Log:      d.log,
		Sessions: d.listSessions,
		Changed:  d.changes.firePlugins,
		NetTest:  netTestHosts(os.Getenv("MAKO_PLUGIN_NET_TEST")),
	})
	ch := make(chan struct{})
	go func() {
		defer close(ch)
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

// netTestHosts reads MAKO_PLUGIN_NET_TEST, host=base URL pairs separated
// by commas, which send a granted host's requests to a test's server: a
// development aid, logged as it is used.
func netTestHosts(v string) map[string]string {
	out := map[string]string{}
	for _, pair := range strings.Split(v, ",") {
		if host, base, ok := strings.Cut(strings.TrimSpace(pair), "="); ok && host != "" && base != "" {
			out[host] = base
		}
	}
	return out
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
	return stream.Send(protocol.TypePlugins, env.GetId(), &protocol.Plugins{Plugins: d.plugins.List()})
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

func (d *Daemon) handleBlockEvent(ctx context.Context, stream *transport.Stream, env *protocol.Envelope, fromPeer bool) error {
	if fromPeer {
		return errBlocksPeer
	}
	req, err := protocol.Body[*protocol.BlockEvent](env)
	if err != nil {
		return err
	}
	src, err := d.blockSource(req.GetBlock())
	if err != nil {
		return err
	}
	done, err := src.Event(ctx, req)
	if err != nil {
		return err
	}
	return stream.Send(protocol.TypePluginDone, env.GetId(), done)
}

// handleWatchBlock streams a block's tree until the front-end hangs up.
func (d *Daemon) handleWatchBlock(ctx context.Context, stream *transport.Stream, env *protocol.Envelope, fromPeer bool) error {
	if fromPeer {
		return errBlocksPeer
	}
	req, err := protocol.Body[*protocol.WatchBlock](env)
	if err != nil {
		return err
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
