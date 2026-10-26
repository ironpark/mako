package transport

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/ironpark/sanbo/internal/protocol"
)

// quicConfig is shared by listeners and dialers. Keep-alives hold NAT and
// tailnet paths open so idle peer links survive.
func quicConfig() *quic.Config {
	return &quic.Config{
		MaxIdleTimeout:  60 * time.Second,
		KeepAlivePeriod: 15 * time.Second,
		// quic-go starts at 1280-byte datagrams, which with their headers
		// overflow a 1280 MTU, as Tailscale's interface has, and are dropped
		// before the handshake. 1200, QUIC's minimum, fits; path MTU
		// discovery grows it where the path allows.
		InitialPacketSize: 1200,
	}
}

// Listener accepts sanbo QUIC connections.
type Listener struct {
	ln *quic.Listener
}

// Listen starts a QUIC listener on addr.
func Listen(addr string, tlsConf *tls.Config) (*Listener, error) {
	ln, err := quic.ListenAddr(addr, tlsConf, quicConfig())
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", addr, err)
	}
	return &Listener{ln: ln}, nil
}

// Accept returns the next connection.
func (l *Listener) Accept(ctx context.Context) (*quic.Conn, error) {
	return l.ln.Accept(ctx)
}

// Addr reports the local listen address.
func (l *Listener) Addr() net.Addr { return l.ln.Addr() }

// Close stops the listener.
func (l *Listener) Close() error { return l.ln.Close() }

// Dial opens a QUIC connection to a sanbo daemon.
func Dial(ctx context.Context, addr string) (*quic.Conn, error) {
	conn, err := quic.DialAddr(ctx, addr, ClientTLS(), quicConfig())
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	return conn, nil
}

// Stream is a bidirectional QUIC stream carrying protocol frames.
type Stream struct {
	*quic.Stream
}

// OpenStream opens a new outbound stream on conn.
func OpenStream(ctx context.Context, conn *quic.Conn) (*Stream, error) {
	s, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	return &Stream{Stream: s}, nil
}

// AcceptStream waits for the peer to open a stream.
func AcceptStream(ctx context.Context, conn *quic.Conn) (*Stream, error) {
	s, err := conn.AcceptStream(ctx)
	if err != nil {
		return nil, err
	}
	return &Stream{Stream: s}, nil
}

// Send writes one frame.
func (s *Stream) Send(t protocol.Type, id string, payload any) error {
	env, err := protocol.Encode(t, id, payload)
	if err != nil {
		return err
	}
	return protocol.WriteFrame(s, env)
}

// Recv reads one frame.
func (s *Stream) Recv() (*protocol.Envelope, error) {
	return protocol.ReadFrame(s)
}

// Request sends a frame and waits for the reply, failing on TypeError.
func (s *Stream) Request(t protocol.Type, payload any, out any) error {
	if err := s.Send(t, "", payload); err != nil {
		return err
	}
	env, err := s.Recv()
	if err != nil {
		return err
	}
	if env.Type == protocol.TypeError {
		var e protocol.Error
		if err := protocol.Decode(env, &e); err != nil {
			return err
		}
		if e.Code != "" {
			return &protocol.CodedError{Code: e.Code, Message: "remote: " + e.Message}
		}
		return fmt.Errorf("remote: %s", e.Message)
	}
	if out == nil {
		return nil
	}
	return protocol.Decode(env, out)
}

// quicConn names the QUIC connection type used across this package.
type quicConn = quic.Conn
