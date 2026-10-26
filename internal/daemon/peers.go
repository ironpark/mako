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
func (d *Daemon) hello(asPeer bool) protocol.Hello {
	return protocol.Hello{
		NodeID:  d.cfg.NodeID,
		Name:    d.cfg.Name,
		Version: config.Version,
		IsPeer:  asPeer,
		AuthTag: d.cfg.PSK,
	}
}

// peerConn returns a live connection to node, dialing and caching on demand.
func (d *Daemon) peerConn(ctx context.Context, node protocol.Node) (*quic.Conn, error) {
	d.mu.Lock()
	conn, ok := d.outbound[node.ID]
	d.mu.Unlock()
	if ok && conn.Context().Err() == nil {
		return conn, nil
	}

	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := transport.Dial(dialCtx, node.Addr)
	if err != nil {
		return nil, err
	}

	d.mu.Lock()
	if prev, ok := d.outbound[node.ID]; ok && prev.Context().Err() == nil {
		d.mu.Unlock()
		conn.CloseWithError(0, "duplicate")
		return prev, nil
	}
	d.outbound[node.ID] = conn
	d.mu.Unlock()
	return conn, nil
}

// peerStream opens a greeted stream to node.
func (d *Daemon) peerStream(ctx context.Context, node protocol.Node) (*transport.Stream, error) {
	conn, err := d.peerConn(ctx, node)
	if err != nil {
		return nil, err
	}
	stream, _, err := transport.Handshake(ctx, conn, d.hello(true))
	if err != nil {
		d.dropPeerConn(node.ID)
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
func (d *Daemon) node(nodeID string) (protocol.Node, error) {
	node, ok := d.peers.get(nodeID)
	if !ok {
		return protocol.Node{}, fmt.Errorf("unknown node %q", nodeID)
	}
	return node, nil
}

// peerSessions asks one peer for its sessions.
func (d *Daemon) peerSessions(ctx context.Context, node protocol.Node, req protocol.ListSessions) ([]protocol.SessionInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	stream, err := d.peerStream(ctx, node)
	if err != nil {
		return nil, err
	}
	defer stream.Close()

	var reply protocol.Sessions
	// The peer sees a peer hello and answers with its own sessions only.
	if err := stream.Request(protocol.TypeListSess, req, &reply); err != nil {
		return nil, err
	}
	out := reply.Sessions[:0:0]
	for _, info := range reply.Sessions {
		if info.NodeID == node.ID {
			out = append(out, info)
		}
	}
	return out, nil
}

// forwardOpen starts a session on a remote node.
func (d *Daemon) forwardOpen(ctx context.Context, req protocol.OpenSession) (protocol.SessionInfo, error) {
	remote := req
	remote.NodeID = "" // the peer runs it locally
	var info protocol.SessionInfo
	if err := d.forward(ctx, req.NodeID, protocol.TypeOpenSession, remote, &info); err != nil {
		return protocol.SessionInfo{}, err
	}
	return info, nil
}

// forwardClose closes a session on a remote node.
func (d *Daemon) forwardClose(ctx context.Context, req protocol.CloseSession, out *transport.Stream, env *protocol.Envelope) error {
	remote := req
	remote.NodeID = ""
	var exited protocol.Exited
	if err := d.forward(ctx, req.NodeID, protocol.TypeCloseSess, remote, &exited); err != nil {
		return err
	}
	return out.Send(protocol.TypeExited, env.ID, exited)
}

// forwardAttach splices a front-end stream onto a peer's session, so the
// daemon a user is connected to acts as a relay for the whole mesh.
func (d *Daemon) forwardAttach(ctx context.Context, req protocol.Attach, client *transport.Stream) error {
	node, err := d.node(req.NodeID)
	if err != nil {
		return err
	}
	peer, err := d.peerStream(ctx, node)
	if err != nil {
		return err
	}
	defer peer.Close()

	remote := req
	remote.NodeID = ""
	if err := peer.Send(protocol.TypeAttach, "", remote); err != nil {
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
func transportProbe(ctx context.Context, addr string, hello protocol.Hello) (protocol.Node, error) {
	return transport.Probe(ctx, addr, hello, 5*time.Second)
}
