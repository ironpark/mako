package daemon

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/ironpark/sanbo/internal/protocol"
	"github.com/ironpark/sanbo/internal/session"
	"github.com/ironpark/sanbo/internal/transport"
)

// local reports whether nodeID addresses this daemon.
func (d *Daemon) local(nodeID string) bool {
	return nodeID == "" || nodeID == d.cfg.NodeID
}

func (d *Daemon) handleListSessions(ctx context.Context, stream *transport.Stream, env *protocol.Envelope, fromPeer bool) error {
	sessions := d.listSessions()
	// A peer asking is already fanning out itself; answering with local
	// sessions only keeps a mesh cycle from recursing.
	if fromPeer {
		return stream.Send(protocol.TypeSessions, env.ID, protocol.Sessions{Sessions: sessions})
	}
	// A front-end asking a single daemon for sessions expects the whole mesh,
	// so fold in what each known peer reports.
	for _, node := range d.peers.list() {
		remote, err := d.peerSessions(ctx, node)
		if err != nil {
			d.log.Debug("peer session list failed", "node", node.Name, "err", err)
			continue
		}
		sessions = append(sessions, remote...)
	}
	return stream.Send(protocol.TypeSessions, env.ID, protocol.Sessions{Sessions: sessions})
}

func (d *Daemon) handleOpenSession(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	var req protocol.OpenSession
	if err := protocol.Decode(env, &req); err != nil {
		return err
	}
	if !d.local(req.NodeID) {
		info, err := d.forwardOpen(ctx, req)
		if err != nil {
			return err
		}
		return stream.Send(protocol.TypeSessionInfo, env.ID, info)
	}

	s, err := d.openSession(ctx, req)
	if err != nil {
		return err
	}

	return stream.Send(protocol.TypeSessionInfo, env.ID, s.Info())
}

func (d *Daemon) handleCloseSession(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	var req protocol.CloseSession
	if err := protocol.Decode(env, &req); err != nil {
		return err
	}
	if !d.local(req.NodeID) {
		return d.forwardClose(ctx, req, stream, env)
	}
	if err := d.closeSession(req.SessionID); err != nil {
		return err
	}
	return stream.Send(protocol.TypeExited, env.ID, protocol.Exited{SessionID: req.SessionID})
}

// maxSessionName caps a session's name, which every listing carries.
const maxSessionName = 200

func (d *Daemon) handleRenameSession(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	var req protocol.RenameSession
	if err := protocol.Decode(env, &req); err != nil {
		return err
	}
	if !d.local(req.NodeID) {
		remote := req
		remote.NodeID = ""
		var info protocol.SessionInfo
		if err := d.forward(ctx, req.NodeID, protocol.TypeRenameSess, remote, &info); err != nil {
			return err
		}
		return stream.Send(protocol.TypeSessionInfo, env.ID, info)
	}
	name := strings.Join(strings.Fields(req.Name), " ") // one line
	if len(name) > maxSessionName {
		return fmt.Errorf("a session name is at most %d bytes", maxSessionName)
	}
	info, err := d.renameSession(req.SessionID, name)
	if err != nil {
		return err
	}
	return stream.Send(protocol.TypeSessionInfo, env.ID, info)
}

// handleAttach turns the stream into a live session pipe: session output flows
// out, and input frames from the front-end flow into the PTY.
func (d *Daemon) handleAttach(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	var req protocol.Attach
	if err := protocol.Decode(env, &req); err != nil {
		return err
	}
	if req.Binary {
		// The stream speaks framed binary from here on, errors included, so
		// the front-end's next read is never a JSON envelope it cannot
		// expect.
		if err := d.attachBinary(ctx, stream, &req); err != nil && !isClosed(err) {
			_ = protocol.WriteStreamJSON(stream, protocol.FrameError, protocol.Error{Message: err.Error()})
		}
		return nil
	}
	if !d.local(req.NodeID) {
		return d.forwardAttach(ctx, req, stream)
	}
	return d.attachLocal(ctx, &req, envelopeWire{stream: stream, id: env.ID, sessionID: req.SessionID})
}

// attachWire is how an attach stream is encoded: JSON envelopes, or the
// binary framing newer front-ends ask for.
type attachWire interface {
	snapshot(protocol.Snapshot) error
	output([]byte) error
	exited(protocol.Exited) error
	// readInput feeds the front-end's input and resizes to s until the
	// stream ends.
	readInput(s *session.Session)
}

// attachLocal pipes a session of this daemon through w.
func (d *Daemon) attachLocal(ctx context.Context, req *protocol.Attach, w attachWire) error {
	s, err := d.attachSession(ctx, req.SessionID)
	if err != nil {
		return err
	}
	sub := s.Subscribe()
	defer func() { sub.Close() }()

	resync := func() error {
		snap, err := s.Snapshot(true)
		if err != nil {
			return err
		}
		return sendSnapshot(w, snap)
	}
	if req.Replay {
		if err := resync(); err != nil {
			return err
		}
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Inbound frames on an attached stream are input and resize only.
	go func() {
		defer cancel()
		w.readInput(s)
	}()

	for {
		chunk, ok := sub.Next(ctx)
		if !ok {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			info := s.Info()
			if info.State != protocol.StateExited {
				// The session dropped us for falling behind. Resubscribe
				// and resync from the current screen instead of
				// reporting an exit.
				sub.Close()
				sub = s.Subscribe()
				if err := resync(); err != nil {
					return err
				}
				continue
			}
			code := 0
			if info.ExitCode != nil {
				code = *info.ExitCode
			}
			return w.exited(protocol.Exited{SessionID: req.SessionID, ExitCode: code})
		}
		if err := w.output(chunk); err != nil {
			if err == io.EOF || isClosed(err) {
				return nil
			}
			return err
		}
	}
}

// snapshotChunk is how much of a replay's screen text one piece of output
// carries, so a long scrollback neither exceeds the frame limit nor arrives
// as one piece.
const snapshotChunk = 256 << 10

// sendSnapshot sends snap without its text, then the screen as output the
// front-end feeds to its terminal like live output.
func sendSnapshot(w attachWire, snap protocol.Snapshot) error {
	text := snap.Text
	snap.Text = ""
	if err := w.snapshot(snap); err != nil {
		return err
	}
	for len(text) > 0 {
		n := min(len(text), snapshotChunk)
		if err := w.output([]byte(text[:n])); err != nil {
			return err
		}
		text = text[n:]
	}
	return nil
}

// envelopeWire is the JSON envelope encoding of an attach stream.
type envelopeWire struct {
	stream    *transport.Stream
	id        string
	sessionID string
}

func (w envelopeWire) snapshot(snap protocol.Snapshot) error {
	return w.stream.Send(protocol.TypeSnapshot, w.id, snap)
}

func (w envelopeWire) output(p []byte) error {
	return w.stream.Send(protocol.TypeOutput, w.id, protocol.Output{SessionID: w.sessionID, Data: p})
}

func (w envelopeWire) exited(ex protocol.Exited) error {
	return w.stream.Send(protocol.TypeExited, w.id, ex)
}

func (w envelopeWire) readInput(s *session.Session) {
	for {
		in, err := w.stream.Recv()
		if err != nil {
			return
		}
		switch in.Type {
		case protocol.TypeInput:
			var payload protocol.Input
			if protocol.Decode(in, &payload) == nil {
				_, _ = s.Write(payload.Data)
			}
		case protocol.TypeResize:
			var payload protocol.Resize
			if protocol.Decode(in, &payload) == nil {
				_ = s.Resize(payload.Cols, payload.Rows)
			}
		}
	}
}
