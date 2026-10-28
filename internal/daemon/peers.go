package daemon

import (
	"context"
	"errors"
	"fmt"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/p2p"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transport"
)

// hello is the greeting this daemon sends when it dials someone.
func (d *Daemon) hello() *protocol.Hello {
	return &protocol.Hello{
		Name:    d.cfg.Name,
		Version: config.Version,
	}
}

// peerStream opens a greeted stream to node over libp2p, which holds the
// node to its identity there. A node that does not let this one in
// cannot be asked anything.
func (d *Daemon) peerStream(ctx context.Context, node *protocol.Node) (*transport.Stream, error) {
	if node.GetPeerId() == "" || d.p2p == nil {
		return nil, fmt.Errorf("%s cannot be reached: it is not linked over libp2p", node.GetName())
	}
	s, err := d.p2p.Open(ctx, node.GetPeerId(), node.GetMultiaddrs())
	if errors.Is(err, p2p.ErrNotTrusted) {
		return nil, fmt.Errorf("%s has not let this node in; ask it to", node.GetName())
	} else if err != nil {
		return nil, err
	}
	stream := transport.NewStream(s)
	if _, err := transport.Greet(stream, d.hello()); err != nil {
		_ = s.Reset()
		return nil, err
	}
	return stream, nil
}

// node resolves a node ID to its record.
func (d *Daemon) node(nodeID string) (*protocol.Node, error) {
	node, ok := d.peers.get(nodeID)
	if !ok {
		return nil, fmt.Errorf("unknown node %q", nodeID)
	}
	return node, nil
}

// relayToPeer hands a front-end's stream to the daemon of the node req
// names, as a request of type typ whose stream then carries what that
// daemon says: an attached session, or a tunnel. The relay copies the
// bytes both ways without reading them, as both legs speak the same.
func (d *Daemon) relayToPeer(ctx context.Context, typ protocol.Type, req routed, client *transport.Stream) error {
	node, err := d.node(req.GetNodeId())
	if err != nil {
		return err
	}
	peer, err := d.peerStream(ctx, node)
	if err != nil {
		return err
	}
	if err := peer.Send(typ, "", forPeer(req)); err != nil {
		peer.Close()
		return err
	}
	if err := transport.Splice(ctx, client, peer); err != nil && !isClosed(err) {
		d.log.Debug("relay ended", "node", node.GetName(), "type", typ, "err", err)
	}
	return nil
}
