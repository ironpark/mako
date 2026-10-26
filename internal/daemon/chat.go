package daemon

import (
	"context"
	"fmt"

	"github.com/ironpark/sanbo/internal/protocol"
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
		if req.UsageOnly {
			chat.Usage, err = d.usageForSession(s)
		} else {
			chat, err = d.chatForSession(ctx, s)
		}
		if err != nil {
			return err
		}
		chat.Usage = withReported(chat.Usage, s.Reported())
	}
	return stream.Send(protocol.TypeChat, env.ID, chat)
}

// withReported is a conversation's usage with what its status line said
// over it. It copies, since u may be a cached read's.
func withReported(u *protocol.Usage, r protocol.Usage) *protocol.Usage {
	if r == (protocol.Usage{}) {
		return u
	}
	var out protocol.Usage
	if u != nil {
		out = *u
	}
	out.Merge(r)
	return &out
}
