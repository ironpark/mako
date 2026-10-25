package daemon

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/ironpark/sanbo/internal/protocol"
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
	if !d.local(req.NodeID) {
		return d.forwardAttach(ctx, req, stream)
	}

	s, err := d.attachSession(ctx, req.SessionID)
	if err != nil {
		return err
	}
	out, unsubscribe := s.Subscribe()
	defer func() { unsubscribe() }()

	sendSnapshot := func() error {
		snap, err := s.Snapshot(true)
		if err != nil {
			return err
		}
		return stream.Send(protocol.TypeSnapshot, env.ID, snap)
	}
	if req.Replay {
		if err := sendSnapshot(); err != nil {
			return err
		}
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Inbound frames on an attached stream are input and resize only.
	go func() {
		defer cancel()
		for {
			in, err := stream.Recv()
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
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case chunk, ok := <-out:
			if !ok {
				info := s.Info()
				if info.State != protocol.StateExited {
					// The session dropped us for falling behind. Resubscribe
					// and resync from the current screen instead of
					// reporting an exit.
					unsubscribe()
					out, unsubscribe = s.Subscribe()
					if err := sendSnapshot(); err != nil {
						return err
					}
					continue
				}
				code := 0
				if info.ExitCode != nil {
					code = *info.ExitCode
				}
				return stream.Send(protocol.TypeExited, env.ID, protocol.Exited{
					SessionID: req.SessionID,
					ExitCode:  code,
				})
			}
			err := stream.Send(protocol.TypeOutput, env.ID, protocol.Output{
				SessionID: req.SessionID,
				Data:      chunk,
			})
			if err != nil {
				if err == io.EOF || isClosed(err) {
					return nil
				}
				return err
			}
		}
	}
}
