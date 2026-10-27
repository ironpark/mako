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
}

// NewStream carries frames over rw.
func NewStream(rw io.ReadWriteCloser) *Stream { return &Stream{ReadWriteCloser: rw} }

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

// SetReadDeadline bounds the next reads, where the stream can be bounded,
// as sockets and libp2p streams can.
func (s *Stream) SetReadDeadline(t time.Time) error {
	if rw, ok := s.ReadWriteCloser.(interface{ SetReadDeadline(time.Time) error }); ok {
		return rw.SetReadDeadline(t)
	}
	return errors.ErrUnsupported
}
