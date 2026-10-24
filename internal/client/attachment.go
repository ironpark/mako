package client

import (
	"context"
	"sync"

	"github.com/ironpark/sanbo/internal/protocol"
	"github.com/ironpark/sanbo/internal/transport"
)

// Attachment is a live session stream for a front-end that renders the
// terminal itself, as the GUI does, rather than handing it to the local tty.
// Recv is for one reader goroutine; the senders may be called from any.
type Attachment struct {
	s         *transport.Stream
	sessionID string

	mu sync.Mutex // serializes frames written by Input and Resize
}

// AttachStream binds a new stream to a session and returns it unread: the
// caller receives Snapshot, Output and Exited frames with Recv.
func (c *Client) AttachStream(ctx context.Context, psk string, req protocol.Attach) (*Attachment, error) {
	s, err := c.stream(ctx, psk)
	if err != nil {
		return nil, err
	}
	if err := s.Send(protocol.TypeAttach, "", req); err != nil {
		s.Close()
		return nil, err
	}
	return &Attachment{s: s, sessionID: req.SessionID}, nil
}

// Recv reads the next frame from the session.
func (a *Attachment) Recv() (*protocol.Envelope, error) { return a.s.Recv() }

// Input sends keystrokes to the session PTY.
func (a *Attachment) Input(p []byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.s.Send(protocol.TypeInput, "", protocol.Input{SessionID: a.sessionID, Data: p})
}

// Resize changes the session window size.
func (a *Attachment) Resize(cols, rows uint16) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.s.Send(protocol.TypeResize, "", protocol.Resize{SessionID: a.sessionID, Cols: cols, Rows: rows})
}

// Close detaches; the session keeps running.
func (a *Attachment) Close() error {
	a.s.CancelRead(0)
	return a.s.Close()
}
