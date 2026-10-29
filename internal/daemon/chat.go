package daemon

import (
	"context"
	"fmt"
	"hash/fnv"

	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transcript"
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
		span := transcript.Span{After: max(r.GetAfter(), 0), Before: max(r.GetBefore(), 0)}
		chat, err := d.chatForSession(ctx, s, span)
		if err != nil {
			return nil, err
		}
		// The conversation is the cache's, shared: a shallow copy carries
		// the usage reported over it.
		reply := shallow(chat)
		reply.Usage = withReported(chat.GetUsage(), s.Reported())
		reply.Digest = digest(reply)
		if r.GetSeen() == reply.GetDigest() {
			// A reader polling a quiet conversation has it already.
			return &protocol.Chat{Digest: reply.GetDigest(), Unchanged: true}, nil
		}
		return reply, nil
	})
}

// digest names m's contents: the same contents, the same digest, while
// the daemon runs.
func digest(m proto.Message) uint64 {
	b, _ := proto.MarshalOptions{Deterministic: true}.Marshal(m)
	h := fnv.New64a()
	h.Write(b)
	return max(h.Sum64(), 1) // 0 is a reader that has seen nothing
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
