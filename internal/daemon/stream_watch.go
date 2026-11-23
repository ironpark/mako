package daemon

import (
	"context"
	"errors"
	"io"
	"net"

	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transport"
)

// streamHangup runs one goroutine reading from stream until EOF/error (or
// Hangup). Handlers defer finish so any domain error is sent before Hangup
// unblocks Recv, and the reader goroutine exits before serveStream resumes.
type streamHangup struct {
	gone   chan error
	done   chan struct{}
	stream *transport.Stream
}

func watchStreamHangup(stream *transport.Stream, onGone func()) *streamHangup {
	h := &streamHangup{
		gone:   make(chan error, 1),
		done:   make(chan struct{}),
		stream: stream,
	}
	go func() {
		defer close(h.done)
		_, err := stream.Recv()
		if onGone != nil {
			onGone()
		}
		h.gone <- err
	}()
	return h
}

func watchStreamFrames(stream *transport.Stream, onFrame func(*protocol.Envelope) bool) *streamHangup {
	h := &streamHangup{
		gone:   make(chan error, 1),
		done:   make(chan struct{}),
		stream: stream,
	}
	go func() {
		defer close(h.done)
		for {
			env, err := stream.Recv()
			if err != nil {
				h.gone <- err
				return
			}
			if onFrame != nil && !onFrame(env) {
				return
			}
		}
	}()
	return h
}

func (h *streamHangup) finish(parentCtx context.Context, reqID string, err error) error {
	if parentCtx.Err() == nil && err != nil && !isClosed(err) {
		_ = protocol.WriteError(h.stream, reqID, err)
	}
	_ = h.stream.Hangup()
	<-h.done
	if err == nil {
		return io.EOF
	}
	if !isClosed(err) {
		return errors.Join(err, net.ErrClosed)
	}
	return err
}
