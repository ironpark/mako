package daemon

import (
	"context"
	"fmt"

	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transport"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func (d *Daemon) handleReadChat(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeChat, func(r *protocol.ReadChat) (*protocol.Chat, error) {
		s, ok := d.sessions.get(r.GetSessionId())
		if !ok {
			return nil, fmt.Errorf("unknown session %s", r.GetSessionId())
		}
		if r.GetUsageOnly() {
			u, err := d.usageForSession(s)
			if err != nil {
				return nil, err
			}
			return &protocol.Chat{Usage: withReported(u, s.Reported())}, nil
		}
		chat, err := d.chatForSession(ctx, s)
		if err != nil {
			return nil, err
		}
		// The conversation is the cache's, shared: a shallow copy carries
		// the usage reported over it.
		reply := shallow(chat)
		reply.Usage = withReported(chat.GetUsage(), s.Reported())
		return reply, nil
	})
}

// shallow copies m's fields, not what they hold: a repeated field's
// copy shares its elements.
func shallow[M proto.Message](m M) M {
	src := m.ProtoReflect()
	out := src.Type().New()
	src.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		out.Set(fd, v)
		return true
	})
	return out.Interface().(M)
}

// withReported is a conversation's usage with what its status line said
// over it. It copies, since u may be a cached read's.
func withReported(u *protocol.Usage, r *protocol.Usage) *protocol.Usage {
	if r.IsZero() {
		return u
	}
	out := &protocol.Usage{}
	if u != nil {
		out = proto.CloneOf(u)
	}
	out.Merge(r)
	return out
}
