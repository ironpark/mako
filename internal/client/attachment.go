package client

import (
	"context"
	"sync"

	"github.com/ironpark/sanbo/internal/protocol"
	"github.com/ironpark/sanbo/internal/transport"
)

// Attachment is a live session stream for a front-end that renders the
// terminal itself, as the GUI does, rather than handing it to the local tty.
// It speaks the compact framing of protocol.StreamFrame; Recv is for one
// reader goroutine, and the senders may be called from any.
type Attachment struct {
	s *transport.Stream

	mu sync.Mutex // serializes frames written by Input and Resize

	buf []byte
	ev  protocol.StreamEvent
}

// AttachStream binds a new stream to a session and returns it unread: the
// caller receives Snapshot, Output and Exited frames with Recv.
func (c *Client) AttachStream(ctx context.Context, psk string, req protocol.Attach) (*Attachment, error) {
	s, err := c.stream(ctx, psk)
	if err != nil {
		return nil, err
	}
	req.Binary = true
	if err := s.Send(protocol.TypeAttach, "", req); err != nil {
		s.Close()
		return nil, err
	}
	return &Attachment{s: s}, nil
}

// Recv reads the next frame from the session. The returned event and its Data
// are valid only until the next call.
func (a *Attachment) Recv() (*protocol.StreamEvent, error) {
	kind, payload, err := protocol.ReadStreamFrame(a.s, a.buf)
	if err != nil {
		return nil, err
	}
	a.buf = payload
	a.ev = protocol.StreamEvent{Kind: kind, Data: payload}
	if err := protocol.DecodeStreamEvent(&a.ev); err != nil {
		return nil, err
	}
	return &a.ev, nil
}

// Input sends keystrokes to the session PTY.
func (a *Attachment) Input(p []byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return protocol.WriteStreamFrame(a.s, protocol.FrameInput, p)
}

// Resize changes the session window size.
func (a *Attachment) Resize(cols, rows uint16) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return protocol.WriteStreamResize(a.s, cols, rows)
}

// Close detaches; the session keeps running.
func (a *Attachment) Close() error {
	a.s.CancelRead(0)
	return a.s.Close()
}
