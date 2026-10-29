package transport

import (
	"errors"
	"fmt"
	"io"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/protocol"
)

// Stream is a bidirectional stream carrying protocol frames: a front-end's
// connection to its daemon's socket, or a libp2p stream from a peer.
type Stream struct {
	io.ReadWriteCloser
	pack   bool // frames that compress well go compressed
	hailed bool // a hello was sent whose welcome is not read yet
}

// NewStream carries frames over rw.
func NewStream(rw io.ReadWriteCloser) *Stream { return &Stream{ReadWriteCloser: rw} }

// Packs reports whether the stream's frames go compressed: whether it is
// between daemons.
func (s *Stream) Packs() bool { return s.pack }

// NewPeerStream carries frames over rw, a stream between daemons, where
// the frames worth it go compressed. A front-end's socket is not worth
// it: its frames never leave the machine.
func NewPeerStream(rw io.ReadWriteCloser) *Stream { return &Stream{ReadWriteCloser: rw, pack: true} }

// Hangup stops reading the stream as well as writing it, so a reader
// blocked on it returns at once.
func (s *Stream) Hangup() error {
	switch rw := s.ReadWriteCloser.(type) {
	case interface{ CloseRead() error }:
		_ = rw.CloseRead()
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
	return protocol.WriteFrame(s, env, s.pack)
}

// Recv reads one frame, after the welcome to a hail.
func (s *Stream) Recv() (*protocol.Envelope, error) {
	if _, err := s.Welcomed(); err != nil {
		return nil, err
	}
	return protocol.ReadFrame(s)
}

// Hail sends hello without waiting for its welcome, so a request can
// follow it at once, saving a round trip on a stream across the mesh.
// The welcome is read before the first reply, or by Welcomed.
func (s *Stream) Hail(hello *protocol.Hello) error {
	if err := s.Send(protocol.TypeHello, "", hello); err != nil {
		return err
	}
	s.hailed = true
	return nil
}

// Welcomed reads the welcome to a hail, if it is still to be read, and
// returns the node it names. A stream whose bytes are copied without
// reading frames reads it first.
func (s *Stream) Welcomed() (*protocol.Node, error) {
	if !s.hailed {
		return nil, nil
	}
	s.hailed = false
	env, err := protocol.ReadFrame(s)
	if err != nil {
		return nil, err
	}
	welcome, err := Reply[*protocol.Welcome](env)
	if err != nil {
		return nil, err
	}
	if welcome.GetNode() == nil {
		return nil, errors.New("transport: a welcome without its node")
	}
	return welcome.GetNode(), nil
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

// SetReadDeadline bounds the next reads, where the stream can be bounded,
// as sockets and libp2p streams can.
func (s *Stream) SetReadDeadline(t time.Time) error {
	if rw, ok := s.ReadWriteCloser.(interface{ SetReadDeadline(time.Time) error }); ok {
		return rw.SetReadDeadline(t)
	}
	return errors.ErrUnsupported
}
