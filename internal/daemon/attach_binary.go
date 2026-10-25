package daemon

import (
	"context"

	"github.com/ironpark/sanbo/internal/protocol"
	"github.com/ironpark/sanbo/internal/session"
	"github.com/ironpark/sanbo/internal/transport"
)

// attachBinary is an attach stream that speaks protocol.StreamFrame after the
// Attach request. Output is the hot path, so it is carried as raw bytes
// instead of base64 inside JSON.
func (d *Daemon) attachBinary(ctx context.Context, stream *transport.Stream, req *protocol.Attach) error {
	if !d.local(req.NodeID) {
		return d.forwardAttachBinary(ctx, stream, req)
	}
	return d.attachLocal(ctx, req, frameWire{stream})
}

// frameWire is the binary encoding of an attach stream.
type frameWire struct {
	stream *transport.Stream
}

func (w frameWire) snapshot(snap protocol.Snapshot) error {
	return protocol.WriteStreamJSON(w.stream, protocol.FrameSnapshot, snap)
}

func (w frameWire) output(p []byte) error {
	return protocol.WriteStreamFrame(w.stream, protocol.FrameOutput, p)
}

func (w frameWire) exited(ex protocol.Exited) error {
	return protocol.WriteStreamJSON(w.stream, protocol.FrameExited, ex)
}

func (w frameWire) readInput(s *session.Session) {
	var buf []byte
	for {
		kind, payload, err := protocol.ReadStreamFrame(w.stream, buf)
		if err != nil {
			return
		}
		buf = payload
		switch kind {
		case protocol.FrameInput:
			_, _ = s.Write(payload)
		case protocol.FrameResize:
			if cols, rows, ok := protocol.DecodeResize(payload); ok {
				_ = s.Resize(cols, rows)
			}
		}
	}
}
