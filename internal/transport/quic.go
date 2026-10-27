package transport

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/quic-go/quic-go"
	"google.golang.org/protobuf/proto"

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

// Stream is a bidirectional stream carrying protocol frames: a QUIC
// stream from a front-end or an older peer, or a libp2p stream from a
// peer on the mesh.
type Stream struct {
	io.ReadWriteCloser
}

// NewStream carries frames over rw.
func NewStream(rw io.ReadWriteCloser) *Stream { return &Stream{ReadWriteCloser: rw} }

// OpenStream opens a new outbound stream on conn.
func OpenStream(ctx context.Context, conn *quic.Conn) (*Stream, error) {
	s, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	return NewStream(s), nil
}

// AcceptStream waits for the peer to open a stream.
func AcceptStream(ctx context.Context, conn *quic.Conn) (*Stream, error) {
	s, err := conn.AcceptStream(ctx)
	if err != nil {
		return nil, err
	}
	return NewStream(s), nil
}

// Hangup stops reading the stream as well as writing it, so a reader
// blocked on it returns at once.
func (s *Stream) Hangup() error {
	switch rw := s.ReadWriteCloser.(type) {
	case *quic.Stream:
		rw.CancelRead(0)
	case interface{ Reset() error }:
		return rw.Reset()
	}
	return s.Close()
}

// Send writes one frame: msg as a message of type t.
func (s *Stream) Send(t protocol.Type, id string, msg proto.Message) error {
	env, err := protocol.Encode(t, id, msg)
	if err != nil {
		return err
	}
	return protocol.WriteFrame(s, env)
}

// Recv reads one frame.
func (s *Stream) Recv() (*protocol.Envelope, error) {
	return protocol.ReadFrame(s)
}

// Call sends req as a message of type t and returns the reply's message as
// R, failing on an Error.
func Call[R proto.Message](s *Stream, t protocol.Type, req proto.Message) (R, error) {
	var zero R
	if err := s.Send(t, "", req); err != nil {
		return zero, err
	}
	env, err := s.Recv()
	if err != nil {
		return zero, err
	}
	return Reply[R](env)
}

// Reply is the message env carries as R, or the Error it carries.
func Reply[R proto.Message](env *protocol.Envelope) (R, error) {
	var zero R
	if e := env.GetError(); e != nil {
		if e.GetCode() != "" {
			return zero, &protocol.CodedError{Code: e.GetCode(), Message: "remote: " + e.GetMessage()}
		}
		return zero, fmt.Errorf("remote: %s", e.GetMessage())
	}
	return protocol.Body[R](env)
}

// quicConn names the QUIC connection type used across this package.
type quicConn = quic.Conn

// SetReadDeadline bounds the next reads, where the stream can be bounded,
// as QUIC and libp2p streams can.
func (s *Stream) SetReadDeadline(t time.Time) error {
	if rw, ok := s.ReadWriteCloser.(interface{ SetReadDeadline(time.Time) error }); ok {
		return rw.SetReadDeadline(t)
	}
	return errors.ErrUnsupported
}
