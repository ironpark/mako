package daemon

import (
	"context"
	"fmt"
	"hash/fnv"

	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
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
		// The conversation is the cache's, shared: a shallow copy carries
		// the usage reported over the session's own.
		var reply *protocol.Chat
		if r.GetAgent() != "" {
			chat, err := d.chatForAgent(ctx, s, r.GetAgent(), span)
			if err != nil {
				return nil, err
			}
			reply = shallow(chat)
		} else {
			chat, err := d.chatForSession(ctx, s, span)
			if err != nil {
				return nil, err
			}
			reply = shallow(chat)
			reply.Usage = withReported(chat.GetUsage(), s.Reported())
		}
		reply.Digest = digest(reply)
		if r.GetSeen() == reply.GetDigest() {
			// A reader polling a quiet conversation has it already.
			return &protocol.Chat{Digest: reply.GetDigest(), Unchanged: true}, nil
		}
		return reply, nil
	})
}

// chatForAgent reads span of the conversation of session s's subagent
// id, by the log the session's own conversation names for it: as the
// cache last read it, or read again for one started since.
func (d *Daemon) chatForAgent(ctx context.Context, s *session.Session, id string, span transcript.Span) (*protocol.Chat, error) {
	cache := d.conversations.chatCache(s)
	var a *protocol.SubAgent
	if cache != nil {
		a = cache.Agent(id)
	}
	if a == nil {
		chat, err := d.chatForSession(ctx, s, transcript.Span{})
		if err != nil {
			return nil, err
		}
		for _, c := range chat.GetAgents() {
			if c.GetId() == id {
				a = c
			}
		}
	}
	if a.GetLog() == "" {
		return nil, fmt.Errorf("no subagent %s in this session", id)
	}
	if cache != nil {
		return cache.ReadAgent(s.Info(), a.GetLog(), span)
	}
	return transcript.ReadAgent(s.Info(), a.GetLog(), span)
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
