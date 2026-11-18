package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/ironpark/mako/internal/plugin/store"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transport"
)

// The node's registries, and the plugins installed from them
// (internal/plugin/store), are its own front-ends' to change: installing
// is each node's own, so a peer is refused, as it is for plugins.

var errStorePeer = errors.New("a node's plugins are installed by its own front-ends alone")

// registryTestEnv lets registries and packages be on loopback, by http,
// and leaves the default registry out: for end-to-end tests alone, which
// must reach no registry on the internet.
const registryTestEnv = "MAKO_PLUGIN_REGISTRY_TEST"

// Within how long the store's requests are answered.
const (
	searchWithin  = 30 * time.Second
	installWithin = 2 * time.Minute
)

func newPluginStore(dataDir string, log *slog.Logger) *store.Store {
	cfg := store.Config{DataDir: dataDir, Default: store.DefaultRegistry, Log: log}
	if os.Getenv(registryTestEnv) == "1" {
		cfg.Loopback, cfg.Default = true, ""
	}
	return store.New(cfg)
}

func (d *Daemon) handlePluginStore(ctx context.Context, stream *transport.Stream, env *protocol.Envelope, fromPeer bool) error {
	if fromPeer || d.store == nil {
		return errStorePeer
	}
	switch env.Type() {
	case protocol.TypeListRegistries:
		return stream.Send(protocol.TypeRegistries, env.GetId(), &protocol.Registries{Registries: d.store.Registries()})
	case protocol.TypeRegistryAction:
		req, err := protocol.Body[*protocol.RegistryAction](env)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(ctx, searchWithin)
		defer cancel()
		switch req.GetOp() {
		case protocol.RegistryAdd:
			err = d.store.AddRegistry(ctx, req.GetUrl())
		case protocol.RegistryRemove:
			err = d.store.RemoveRegistry(req.GetUrl())
		case protocol.RegistryRefresh:
			d.store.Refresh(ctx)
		default:
			err = fmt.Errorf("unknown op %q", req.GetOp())
		}
		if err != nil {
			return err
		}
		return stream.Send(protocol.TypeRegistries, env.GetId(), &protocol.Registries{Registries: d.store.Registries()})
	case protocol.TypeSearchPlugins:
		req, err := protocol.Body[*protocol.SearchPlugins](env)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(ctx, searchWithin)
		defer cancel()
		return stream.Send(protocol.TypePluginListings, env.GetId(),
			&protocol.PluginListings{Listings: d.store.Search(ctx, req.GetQuery(), req.GetId())})
	case protocol.TypeInstallPlugin:
		req, err := protocol.Body[*protocol.InstallPlugin](env)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(ctx, installWithin)
		defer cancel()
		var done *protocol.PluginInstalled
		switch req.GetOp() {
		case protocol.InstallOp, protocol.UpdateOp:
			done, err = d.store.Install(ctx, req.GetOp(), req.GetId(), req.GetRegistry())
		case protocol.RemoveOp:
			err = d.store.Remove(req.GetId())
			done = &protocol.PluginInstalled{Id: req.GetId()}
		default:
			err = fmt.Errorf("unknown op %q", req.GetOp())
		}
		if err != nil {
			return err
		}
		return stream.Send(protocol.TypePluginInstalled, env.GetId(), done)
	}
	return fmt.Errorf("unexpected %s", env.Type())
}
