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
	stream, _, err := d.peerStreamNode(ctx, node)
	return stream, err
}

// peerStreamNode opens a greeted stream and reports the peer as it just
// described itself, which is fresher than the discovered record.
func (d *Daemon) peerStreamNode(ctx context.Context, node protocol.Node) (*transport.Stream, protocol.Node, error) {
	conn, err := d.peerConn(ctx, node)
	if err != nil {
		return nil, protocol.Node{}, err
	}
	stream, remote, err := transport.Handshake(ctx, conn, d.hello(true))
	if err != nil {
		d.dropPeerConn(node.ID)
		return nil, protocol.Node{}, err
	}
	return stream, remote, nil
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
	return relayBoth(ctx,
		func() error { return relay(peer, client) }, // peer output -> front-end
		func() error { return relay(client, peer) }, // front-end input -> peer
	)
}

// forwardAttachBinary relays a binary front-end stream onto a peer. A peer
// that advertises the framing gets the bytes copied through untouched;
// otherwise they are translated to and from envelopes, so a new daemon still
// works with peers that know nothing of the binary framing.
func (d *Daemon) forwardAttachBinary(ctx context.Context, client *transport.Stream, req *protocol.Attach) error {
	node, err := d.node(req.NodeID)
	if err != nil {
		return err
	}
	peer, remote, err := d.peerStreamNode(ctx, node)
	if err != nil {
		return err
	}
	defer peer.Close()

	remoteReq := *req
	remoteReq.NodeID = ""
	remoteReq.Binary = remote.HasFeature(protocol.FeatureAttachBinary)
	if err := peer.Send(protocol.TypeAttach, "", remoteReq); err != nil {
		return err
	}
	if remoteReq.Binary {
		// Both ends speak the same framing; nothing in between needs
		// decoding.
		return relayBoth(ctx,
			func() error { _, err := io.Copy(peer, client); return err },
			func() error { _, err := io.Copy(client, peer); return err },
		)
	}
	return relayBoth(ctx,
		func() error { return relayPeerToStream(peer, client) },
		func() error { return relayStreamToPeer(client, peer, req.SessionID) },
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

// relayPeerToStream translates envelopes from a peer into binary frames for a
// front-end.
func relayPeerToStream(peer, client *transport.Stream) error {
	for {
		env, err := peer.Recv()
		if err != nil {
			return err
		}
		switch env.Type {
		case protocol.TypeSnapshot:
			var snap protocol.Snapshot
			if err := protocol.Decode(env, &snap); err != nil {
				return err
			}
			if err := sendSnapshot(frameWire{client}, snap); err != nil {
				return err
			}
		case protocol.TypeOutput:
			var out protocol.Output
			if err := protocol.Decode(env, &out); err != nil {
				return err
			}
			if err := protocol.WriteStreamFrame(client, protocol.FrameOutput, out.Data); err != nil {
				return err
			}
		case protocol.TypeExited:
			var ex protocol.Exited
			_ = protocol.Decode(env, &ex)
			return protocol.WriteStreamJSON(client, protocol.FrameExited, ex)
		case protocol.TypeError:
			var e protocol.Error
			_ = protocol.Decode(env, &e)
			return protocol.WriteStreamJSON(client, protocol.FrameError, e)
		}
	}
}

// relayStreamToPeer translates a front-end's binary frames into envelopes for
// a peer.
func relayStreamToPeer(client, peer *transport.Stream, sessionID string) error {
	var buf []byte
	for {
		kind, payload, err := protocol.ReadStreamFrame(client, buf)
		if err != nil {
			return err
		}
		buf = payload
		switch kind {
		case protocol.FrameInput:
			if err := peer.Send(protocol.TypeInput, "", protocol.Input{SessionID: sessionID, Data: payload}); err != nil {
				return err
			}
		case protocol.FrameResize:
			cols, rows, ok := protocol.DecodeResize(payload)
			if !ok {
				continue
			}
			if err := peer.Send(protocol.TypeResize, "", protocol.Resize{SessionID: sessionID, Cols: cols, Rows: rows}); err != nil {
				return err
			}
		}
	}
}

// relay copies frames from src to dst until either side ends.
func relay(src, dst *transport.Stream) error {
	for {
		env, err := src.Recv()
		if err != nil {
			return err
		}
		if err := protocol.WriteFrame(dst, env); err != nil {
			return err
		}
	}
}

// transportProbe dials addr and returns the node answering there.
func transportProbe(ctx context.Context, addr string, hello protocol.Hello) (protocol.Node, error) {
	return transport.Probe(ctx, addr, hello, 5*time.Second)
}
