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
	req, _ := protocol.Body[*protocol.ListSessions](env) // older front-ends send nothing
	if req.GetApprovals() {
		d.approvals.watch()
	}
	sessions := d.listSessions()
	// A peer asking is already fanning out itself; answering with local
	// sessions only keeps a mesh cycle from recursing.
	if fromPeer {
		return stream.Send(protocol.TypeSessions, env.GetId(), &protocol.Sessions{Sessions: sessions})
	}
	// A front-end asking a single daemon for sessions expects the whole mesh,
	// so fold in what each known peer reports.
	for _, node := range d.peers.list() {
		remote, err := d.peerSessions(ctx, node, req)
		if err != nil {
			d.log.Debug("peer session list failed", "node", node.GetName(), "err", err)
			continue
		}
		sessions = append(sessions, remote...)
	}
	return stream.Send(protocol.TypeSessions, env.GetId(), &protocol.Sessions{Sessions: sessions})
}

func (d *Daemon) handleOpenSession(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeSessionInfo, func(r *protocol.OpenSession) (*protocol.SessionInfo, error) {
		s, err := d.openSession(ctx, r)
		if err != nil {
			return nil, err
		}
		return s.Info(), nil
	})
}

func (d *Daemon) handleCloseSession(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeExited, func(r *protocol.CloseSession) (*protocol.Exited, error) {
		if err := d.closeSession(r.GetSessionId()); err != nil {
			return nil, err
		}
		return &protocol.Exited{SessionId: r.GetSessionId()}, nil
	})
}

func (d *Daemon) handleAnswerApproval(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeSessionInfo, func(r *protocol.AnswerApproval) (*protocol.SessionInfo, error) {
		s, ok := d.sessions.get(r.GetSessionId())
		if !ok {
			return nil, fmt.Errorf("unknown session %q", r.GetSessionId())
		}
		if err := d.approvals.answer(r.GetSessionId(), r); err != nil {
			return nil, err
		}
		return s.Info(), nil
	})
}

// maxSessionName caps a session's name, which every listing carries.
const maxSessionName = 200

func (d *Daemon) handleRenameSession(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeSessionInfo, func(r *protocol.RenameSession) (*protocol.SessionInfo, error) {
		name := strings.Join(strings.Fields(r.GetName()), " ") // one line
		if len(name) > maxSessionName {
			return nil, fmt.Errorf("a session name is at most %d bytes", maxSessionName)
		}
		return d.renameSession(r.GetSessionId(), name)
	})
}

// handleAttach turns the stream into a live session pipe: session output flows
// out, and input frames from the front-end flow into the PTY.
func (d *Daemon) handleAttach(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	req, err := protocol.Body[*protocol.Attach](env)
	if err != nil {
		return err
	}
	if !d.local(req.GetNodeId()) {
		return d.forwardAttach(ctx, req, stream)
	}
	return d.attachLocal(ctx, req, attachWire{stream: stream, id: env.GetId(), sessionID: req.GetSessionId()})
}

// attachLocal pipes a session of this daemon through w.
func (d *Daemon) attachLocal(ctx context.Context, req *protocol.Attach, w attachWire) error {
	s, err := d.attachSession(ctx, req.GetSessionId())
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
	if req.GetReplay() {
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
			if info.GetState() != protocol.StateExited {
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
			return w.exited(&protocol.Exited{SessionId: req.GetSessionId(), ExitCode: info.GetExitCode()})
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
func sendSnapshot(w attachWire, snap *protocol.Snapshot) error {
	text := snap.GetText()
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

// attachWire is an attached stream's messages: the session's screen,
// output and end toward the front-end, its input and resizes back.
type attachWire struct {
	stream    *transport.Stream
	id        string
	sessionID string
}

func (w attachWire) snapshot(snap *protocol.Snapshot) error {
	return w.stream.Send(protocol.TypeSnapshot, w.id, snap)
}

func (w attachWire) output(p []byte) error {
	return w.stream.Send(protocol.TypeOutput, w.id, &protocol.Output{SessionId: w.sessionID, Data: p})
}

func (w attachWire) exited(ex *protocol.Exited) error {
	return w.stream.Send(protocol.TypeExited, w.id, ex)
}

// readInput feeds the front-end's input and resizes to s until the stream
// ends.
func (w attachWire) readInput(s *session.Session) {
	for {
		in, err := w.stream.Recv()
		if err != nil {
			return
		}
		switch in.Type() {
		case protocol.TypeInput:
			_, _ = s.Write(in.GetInput().GetData())
		case protocol.TypeResize:
			r := in.GetResize()
			_ = s.Resize(uint16(r.GetCols()), uint16(r.GetRows()))
		}
	}
}
