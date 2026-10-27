package daemon

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/ironpark/sanbo/internal/config"
	"github.com/ironpark/sanbo/internal/protocol"
	"github.com/ironpark/sanbo/internal/transport"
)

// hello is the greeting this daemon sends when it dials someone.
func (d *Daemon) hello(asPeer bool) *protocol.Hello {
	return &protocol.Hello{
		NodeId:  d.cfg.NodeID,
		Name:    d.cfg.Name,
		Version: config.Version,
		IsPeer:  asPeer,
		AuthTag: d.cfg.PSK,
	}
}

// peerConn returns a live connection to node, dialing and caching on demand.
func (d *Daemon) peerConn(ctx context.Context, node *protocol.Node) (*quic.Conn, error) {
	d.mu.Lock()
	conn, ok := d.outbound[node.GetId()]
	d.mu.Unlock()
	if ok && conn.Context().Err() == nil {
		return conn, nil
	}

	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := transport.Dial(dialCtx, node.GetAddr())
	if err != nil {
		return nil, err
	}

	d.mu.Lock()
	if prev, ok := d.outbound[node.GetId()]; ok && prev.Context().Err() == nil {
		d.mu.Unlock()
		conn.CloseWithError(0, "duplicate")
		return prev, nil
	}
	d.outbound[node.GetId()] = conn
	d.mu.Unlock()
	return conn, nil
}

// peerStream opens a greeted stream to node: over libp2p to a node with an
// identity there, which is held to it, and over QUIC to an older one.
func (d *Daemon) peerStream(ctx context.Context, node *protocol.Node) (*transport.Stream, error) {
	if node.GetPeerId() != "" {
		if d.p2p == nil {
			return nil, fmt.Errorf("%s is reached over libp2p, which is off here", node.GetName())
		}
		s, err := d.p2p.Open(ctx, node.GetPeerId(), node.GetMultiaddrs())
		if err != nil {
			return nil, err
		}
		stream := transport.NewStream(s)
		// The link has shown the key already; the hello does not carry it.
		hello := d.hello(true)
		hello.AuthTag = ""
		if _, err := transport.Greet(stream, hello); err != nil {
			_ = s.Reset()
			return nil, err
		}
		return stream, nil
	}
	conn, err := d.peerConn(ctx, node)
	if err != nil {
		return nil, err
	}
	stream, _, err := transport.Handshake(ctx, conn, d.hello(true))
	if err != nil {
		d.dropPeerConn(node.GetId())
		return nil, err
	}
	return stream, nil
}

func (d *Daemon) dropPeerConn(nodeID string) {
	d.mu.Lock()
	conn, ok := d.outbound[nodeID]
	delete(d.outbound, nodeID)
	d.mu.Unlock()
	if ok {
		conn.CloseWithError(0, "reset")
	}
}

// node resolves a node ID to its record.
func (d *Daemon) node(nodeID string) (*protocol.Node, error) {
	node, ok := d.peers.get(nodeID)
	if !ok {
		return nil, fmt.Errorf("unknown node %q", nodeID)
	}
	return node, nil
}

// peerSessions asks one peer for its sessions.
func (d *Daemon) peerSessions(ctx context.Context, node *protocol.Node, req *protocol.ListSessions) ([]*protocol.SessionInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	stream, err := d.peerStream(ctx, node)
	if err != nil {
		return nil, err
	}
	defer stream.Close()

	// The peer sees a peer hello and answers with its own sessions only.
	reply, err := transport.Call[*protocol.Sessions](stream, protocol.TypeListSess, req)
	if err != nil {
		return nil, err
	}
	out := reply.GetSessions()[:0:0]
	for _, info := range reply.GetSessions() {
		if info.GetNodeId() == node.GetId() {
			out = append(out, info)
		}
	}
	return out, nil
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

// transportProbe dials addr and returns the node answering there.
func transportProbe(ctx context.Context, addr string, hello *protocol.Hello) (*protocol.Node, error) {
	return transport.Probe(ctx, addr, hello, 5*time.Second)
}
