package daemon

import (
	"context"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transport"
)

// routed is a request addressed to a node.
type routed interface {
	proto.Message
	GetNodeId() string
}

func (d *Daemon) isDirectSSH(nodeID string) bool {
	return d.ssh != nil && d.ssh.isDirect(nodeID)
}

// serveWithDirect is serve, also routing direct-SSH nodes to local work (while
// daemon-tunneled SSH nodes still forward through peerStream).
func serveWithDirect[Req routed, Reply proto.Message](d *Daemon, ctx context.Context, stream *transport.Stream, env *protocol.Envelope,
	replyType protocol.Type, work func(Req) (Reply, error)) error {
	req, err := protocol.Body[Req](env)
	if err != nil {
		return err
	}
	var reply Reply
	if d.local(req.GetNodeId()) || d.isDirectSSH(req.GetNodeId()) {
		reply, err = work(req)
	} else {
		if within := transport.Within(env); within > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, within)
			defer cancel()
		}
		reply, err = forward[Reply](ctx, d, env.Type(), req)
	}
	if err != nil {
		return err
	}
	return stream.Send(replyType, env.GetId(), reply)
}

// serve answers a request this node's own work answers, or a peer does for
// another node: the decode, forward and reply every routed request shares.
func serve[Req routed, Reply proto.Message](d *Daemon, ctx context.Context, stream *transport.Stream, env *protocol.Envelope,
	replyType protocol.Type, work func(Req) (Reply, error)) error {
	req, err := protocol.Body[Req](env)
	if err != nil {
		return err
	}
	var reply Reply
	if d.local(req.GetNodeId()) {
		reply, err = work(req)
	} else {
		// The asker's bound goes on with it.
		if within := transport.Within(env); within > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, within)
			defer cancel()
		}
		reply, err = forward[Reply](ctx, d, env.Type(), req)
	}
	if err != nil {
		return err
	}
	return stream.Send(replyType, env.GetId(), reply)
}

// forwardWithin bounds the wait for a peer's reply to a request passed on
// for an asker that set no bound, as an older front-end: a peer that takes
// the request and never answers would otherwise hold the request, and the
// stream to it, for as long as the daemon runs.
const forwardWithin = 2 * time.Minute

// forward makes a request of a peer for the node it is addressed to,
// telling the peer how long it is waited for.
func forward[R proto.Message](ctx context.Context, d *Daemon, typ protocol.Type, req routed) (R, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, forwardWithin)
		defer cancel()
	}
	var zero R
	stream, err := d.streamFor(ctx, req)
	if err != nil {
		return zero, err
	}
	defer stream.Close()
	wireReq := req
	if d.ssh != nil && d.ssh.isDaemonTunnel(req.GetNodeId()) {
		wireReq = clearRoutedNodeID(req)
	}
	return transport.Ask[R](ctx, stream, typ, wireReq)
}

// open sends req to the node it names, as forward does, and keeps the
// stream for what follows the reply on it, as a claim or a device's
// connection. ctx bounds the wait for the reply.
func open[R proto.Message](ctx context.Context, d *Daemon, typ protocol.Type, req routed) (R, *transport.Stream, error) {
	var zero R
	stream, err := d.streamFor(ctx, req)
	if err != nil {
		return zero, nil, err
	}
	wireReq := req
	if d.ssh != nil && d.ssh.isDaemonTunnel(req.GetNodeId()) {
		wireReq = clearRoutedNodeID(req)
	}
	reply, err := transport.CallContext[R](ctx, stream, typ, wireReq)
	if err != nil {
		stream.Close()
		return zero, nil, err
	}
	return reply, stream, nil
}

// streamFor opens a stream to a peer for the node req is addressed to.
func (d *Daemon) streamFor(ctx context.Context, req routed) (*transport.Stream, error) {
	node, err := d.node(req.GetNodeId())
	if err != nil {
		return nil, err
	}
	return d.peerStream(ctx, node)
}
