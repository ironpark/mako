package daemon

import (
	"context"
	"fmt"

	"github.com/ironpark/sanbo/internal/protocol"
	"github.com/ironpark/sanbo/internal/transcript"
	"github.com/ironpark/sanbo/internal/transport"
)

func (d *Daemon) handleReadChat(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	var req protocol.ReadChat
	if err := protocol.Decode(env, &req); err != nil {
		return err
	}
	var chat protocol.Chat
	if !d.local(req.NodeID) {
		node := req.NodeID
		req.NodeID = ""
		if err := d.forward(ctx, node, protocol.TypeReadChat, req, &chat); err != nil {
			return err
		}
	} else {
		s, ok := d.sessions.get(req.SessionID)
		if !ok {
			return fmt.Errorf("unknown session %s", req.SessionID)
		}
		var err error
		chat, err = transcript.Read(ctx, s.Info(), s.TranscriptEnv(), req.Conversation)
		if err != nil {
			return err
		}
	}
	return stream.Send(protocol.TypeChat, env.ID, chat)
}
