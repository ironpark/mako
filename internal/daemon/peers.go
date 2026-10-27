package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/p2p"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transport"
)

// hello is the greeting this daemon sends when it dials someone.
func (d *Daemon) hello() *protocol.Hello {
	return &protocol.Hello{
		NodeId:  d.cfg.NodeID,
		Name:    d.cfg.Name,
		Version: config.Version,
		IsPeer:  true,
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

// forwardAttach splices a front-end stream onto a peer's session, so the
// daemon a user is connected to acts as a relay for the whole mesh.
func (d *Daemon) forwardAttach(ctx context.Context, req *protocol.Attach, client *transport.Stream) error {
	node, err := d.node(req.GetNodeId())
	if err != nil {
		return err
	}
	peer, err := d.peerStream(ctx, node)
	if err != nil {
		return err
	}
	defer peer.Close()

	if err := peer.Send(protocol.TypeAttach, "", forPeer(req)); err != nil {
		return err
	}
	// Both legs speak the same frames from here on, which the relay need
	// not read: the bytes are copied through.
	return relayBoth(ctx,
		func() error { _, err := io.Copy(client, peer); return err }, // output to the front-end
		func() error { _, err := io.Copy(peer, client); return err }, // input to the peer
	)
}

// relayBoth runs both directions of a relay until either ends.
func relayBoth(ctx context.Context, a, b func() error) error {
	errs := make(chan error, 2)
	go func() { errs <- a() }()
	go func() { errs <- b() }()
	select {
	case err := <-errs:
		if isClosed(err) {
			return nil
		}
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
