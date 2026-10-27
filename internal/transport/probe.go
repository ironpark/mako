package transport

import (
	"context"
	"time"

	"github.com/ironpark/sanbo/internal/protocol"
)

// Handshake opens a stream on conn, sends hello, and returns the peer's node
// record from its welcome.
func Handshake(ctx context.Context, conn *quicConn, hello *protocol.Hello) (*Stream, *protocol.Node, error) {
	stream, err := OpenStream(ctx, conn)
	if err != nil {
		return nil, nil, err
	}
	node, err := Greet(stream, hello)
	if err != nil {
		stream.Close()
		return nil, nil, err
	}
	return stream, node, nil
}

// Greet sends hello on a fresh stream and returns the node that welcomes
// it.
func Greet(stream *Stream, hello *protocol.Hello) (*protocol.Node, error) {
	welcome, err := Call[*protocol.Welcome](stream, protocol.TypeHello, hello)
	if err != nil {
		return nil, err
	}
	return welcome.GetNode(), nil
}

// Probe dials addr, completes the sanbo handshake, and reports the peer node.
// It is how discovery backends turn a reachable address into an identity.
func Probe(ctx context.Context, addr string, hello *protocol.Hello, timeout time.Duration) (*protocol.Node, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	conn, err := Dial(ctx, addr)
	if err != nil {
		return nil, err
	}
	defer conn.CloseWithError(0, "probe")

	stream, node, err := Handshake(ctx, conn, hello)
	if err != nil {
		return nil, err
	}
	if node == nil {
		node = &protocol.Node{}
	}
	stream.Close()
	node.Addr = addr
	return node, nil
}
