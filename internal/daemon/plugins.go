package daemon

import (
	"context"
	"errors"
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

var errPluginPeer = errors.New("a node's plugins are its own front-ends' alone")

// startPlugins makes the manager of the plugins in dir, and runs it until
// ctx ends.
func (d *Daemon) startPlugins(ctx context.Context, dir string) (done <-chan struct{}) {
	d.plugins = plugin.New(plugin.Config{
		Dir:      filepath.Join(dir, "plugins"),
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
	if fromPeer || d.plugins == nil {
		return errPluginPeer
	}
	req, err := protocol.Body[*protocol.BlockEvent](env)
	if err != nil {
		return err
	}
	done, err := d.plugins.Event(ctx, req)
	if err != nil {
		return err
	}
	return stream.Send(protocol.TypePluginDone, env.GetId(), done)
}

// handleWatchBlock streams a block's tree until the front-end hangs up.
func (d *Daemon) handleWatchBlock(ctx context.Context, stream *transport.Stream, env *protocol.Envelope, fromPeer bool) error {
	if fromPeer || d.plugins == nil {
		return errPluginPeer
	}
	req, err := protocol.Body[*protocol.WatchBlock](env)
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
	err = d.plugins.Watch(ctx, req.GetBlock(), func(u *protocol.BlockUpdate) error {
		return stream.Send(protocol.TypeBlockUpdate, env.GetId(), u)
	})
	if ctx.Err() != nil {
		return context.Canceled
	}
	return err
}
