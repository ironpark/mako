// Package transport carries mako's protocol frames: between a front-end
// and the daemon of its machine on the daemon's Unix socket, and between
// daemons on libp2p streams.
package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/ironpark/mako/internal/protocol"
)

// ErrInUse is a socket another daemon listens on.
var ErrInUse = errors.New("a daemon already listens there")

// Listen listens on the Unix socket at path, which only this user may
// reach. A socket left behind by a daemon that is gone is replaced; one a
// daemon still answers on is not.
func Listen(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
		c.Close()
		return nil, fmt.Errorf("%s: %w", path, ErrInUse)
	}
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// Dial opens a greeted stream to the daemon on the socket at path, and
// returns the daemon's node record from its welcome.
func Dial(ctx context.Context, path string, hello *protocol.Hello) (*Stream, *protocol.Node, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, nil, fmt.Errorf("dial %s: %w", path, err)
	}
	s := NewStream(conn)
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	node, err := Greet(s, hello)
	_ = conn.SetDeadline(time.Time{})
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	return s, node, nil
}

// Greet sends hello on a fresh stream and returns the node that welcomes
// it.
func Greet(stream *Stream, hello *protocol.Hello) (*protocol.Node, error) {
	welcome, err := Call[*protocol.Welcome](stream, protocol.TypeHello, hello)
	if err != nil {
		return nil, err
	}
	if welcome.GetNode() == nil {
		return nil, errors.New("transport: a welcome without its node")
	}
	return welcome.GetNode(), nil
}

// CloseWith hangs s up when ctx is done, if it is still open then.
func CloseWith(s *Stream, ctx context.Context) {
	stop := context.AfterFunc(ctx, func() { _ = s.ReadWriteCloser.Close() })
	s.ReadWriteCloser = &closing{s.ReadWriteCloser, stop}
}

// closing is a connection that stops watching for its hang-up on Close,
// keeping its own methods, as Hangup and SetReadDeadline want them.
type closing struct {
	io.ReadWriteCloser
	stop func() bool
}

func (c *closing) Close() error {
	c.stop()
	return c.ReadWriteCloser.Close()
}

func (c *closing) CloseRead() error {
	if r, ok := c.ReadWriteCloser.(interface{ CloseRead() error }); ok {
		return r.CloseRead()
	}
	return c.Close()
}

func (c *closing) SetReadDeadline(t time.Time) error {
	if r, ok := c.ReadWriteCloser.(interface{ SetReadDeadline(time.Time) error }); ok {
		return r.SetReadDeadline(t)
	}
	return errors.ErrUnsupported
}
