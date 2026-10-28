package daemon

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transport"
)

// A tunnel carries a TCP connection from a front-end to a port on a node's
// loopback, so a web server a session started there, which serves only
// its own machine, opens in the front-end's browser. A front-end's daemon
// relays it to the node as it relays an attached session; the node dials
// the port and the stream carries the bytes from then on.

// tunnelDial is how long the node tries to reach the port.
const tunnelDial = 5 * time.Second

func (d *Daemon) handleTunnel(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	req, err := protocol.Body[*protocol.Tunnel](env)
	if err != nil {
		return err
	}
	if p := req.GetPort(); p <= 0 || p > 65535 {
		return fmt.Errorf("%d is not a TCP port", p)
	}
	if !d.local(req.GetNodeId()) {
		return d.relayToPeer(ctx, protocol.TypeTunnel, req, stream)
	}
	conn, err := dialLoopback(ctx, int(req.GetPort()))
	if err != nil {
		return err
	}
	if err := stream.Send(protocol.TypeTunneled, env.GetId(), &protocol.Tunneled{Addr: conn.RemoteAddr().String()}); err != nil {
		conn.Close()
		return err
	}
	if err := transport.Splice(ctx, stream, conn); err != nil && !isClosed(err) {
		d.log.Debug("tunnel ended", "port", req.GetPort(), "err", err)
	}
	return nil
}

// dialLoopback connects to port on this machine's loopback, IPv4 first, as
// "localhost" means to most servers, then IPv6.
func dialLoopback(ctx context.Context, port int) (net.Conn, error) {
	dialer := net.Dialer{Timeout: tunnelDial}
	var first error
	for _, host := range []string{"127.0.0.1", "::1"} {
		conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
		if err == nil {
			return conn, nil
		}
		if first == nil {
			first = err
		}
	}
	return nil, fmt.Errorf("nothing answers on port %d: %w", port, first)
}
