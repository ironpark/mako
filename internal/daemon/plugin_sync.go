package daemon

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transport"
)

// Peer-to-peer plugin SQLite CRDT sync (revision 43).
//
// Each node advertises a 32-byte plugin_sync_digest inside its shared
// NodeState to mutually trusted peers while workspace sharing is enabled.
// When a peer's digest differs from ours, we open one bidirectional
// TypeSyncPluginDB exchange: we send our exported CRDT bundle, the peer merges
// it and replies with its merged bundle, and we merge that reply locally.

var errPluginSyncPeer = errors.New("plugin DB sync is only accepted from a mutually trusted peer")

func (d *Daemon) pluginSyncDigest() []byte {
	if d.plugins == nil || !d.syncsWorkspace() {
		return nil
	}
	return d.plugins.SyncDigest()
}

func (d *Daemon) takePluginSyncDigest(fromNodeID string, remoteDigest []byte) {
	if d.plugins == nil || !d.syncsWorkspace() {
		return
	}
	localDigest := d.plugins.SyncDigest()
	if len(remoteDigest) == 0 && len(localDigest) == 0 {
		return
	}
	if bytes.Equal(remoteDigest, localDigest) {
		return
	}
	d.syncPluginDBWithPeer(fromNodeID)
}

func (d *Daemon) syncPluginDBWithPeer(nodeID string) {
	d.mu.Lock()
	if d.syncingPluginDB == nil {
		d.syncingPluginDB = map[string]bool{}
	}
	if d.syncingPluginDB[nodeID] {
		d.mu.Unlock()
		return
	}
	d.syncingPluginDB[nodeID] = true
	d.mu.Unlock()

	go func() {
		defer func() {
			d.mu.Lock()
			delete(d.syncingPluginDB, nodeID)
			d.mu.Unlock()
		}()
		node, err := d.node(nodeID)
		if err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(d.ctx, 15*time.Second)
		defer cancel()
		stream, err := d.peerStream(ctx, node)
		if err != nil {
			return
		}
		defer stream.Close()

		localBundle := d.plugins.ExportSyncBundle()
		remoteBundle, err := transport.Ask[*protocol.PluginSyncBundle](ctx, stream, protocol.TypeSyncPluginDB, &protocol.SyncPluginDB{Bundle: localBundle})
		if err != nil {
			return
		}
		if d.plugins.MergeSyncBundle(remoteBundle) {
			d.changes.firePlugins()
		}
	}()
}

func (d *Daemon) handleSyncPluginDB(_ context.Context, stream *transport.Stream, env *protocol.Envelope, fromPeer bool) error {
	if !fromPeer || d.plugins == nil || !d.syncsWorkspace() {
		return errPluginSyncPeer
	}
	req, err := protocol.Body[*protocol.SyncPluginDB](env)
	if err != nil {
		return err
	}
	if d.plugins.MergeSyncBundle(req.GetBundle()) {
		d.changes.firePlugins()
	}
	return stream.Send(protocol.TypePluginSyncBundle, env.GetId(), d.plugins.ExportSyncBundle())
}
