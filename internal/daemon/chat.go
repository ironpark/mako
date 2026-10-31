package daemon

import (
	"context"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"slices"
	"sync"
	"weak"

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
		var chat *protocol.Chat
		var err error
		if r.GetAgent() != "" {
			chat, err = d.chatForAgent(ctx, s, r.GetAgent(), span)
		} else {
			chat, err = d.chatForSession(ctx, s, span)
		}
		if err != nil {
			return nil, err
		}
		reply := shallow(chat)
		if r.GetAgent() == "" {
			reply.Usage = withReported(chat.GetUsage(), s.Reported())
		}
		// The usage alone is digested anew: the conversation, shared and
		// unchanged while its log is, keeps its digest.
		reply.Digest = combine(chatDigests.of(chat), digest(reply.GetUsage()))
		if r.GetSeen() == reply.GetDigest() {
			// A reader polling a quiet conversation has it already.
			return &protocol.Chat{Digest: reply.GetDigest(), Unchanged: true}, nil
		}
		// The latest messages move on a few at a time: a reader that can
		// is sent what changed of the chat it has.
		if sent := d.conversations.sentChats(s); sent != nil && span == (transcript.Span{}) {
			if r.GetPatch() {
				if base := sent.find(r.GetAgent(), r.GetSeen()); base != nil {
					patch(reply, base, r.GetSeen())
				}
			}
			sent.add(r.GetAgent(), reply.GetDigest(), chat)
		}
		return reply, nil
	})
}

// sentChats are the latest chats sent of one session's conversation, and
// of its subagents', for a reader's next read to name and be patched from.
// Two are kept of each: a second reader may be a read behind the first.
type sentChats struct {
	mu   sync.Mutex
	last map[string][2]sentChat // by subagent, "" for the session's own
}

type sentChat struct {
	digest uint64
	chat   *protocol.Chat
}

// sentChatsKept bounds the conversations kept, the session's and the
// subagents' its readers looked at.
const sentChatsKept = 8

// find is the chat of agent sent under digest, nil for one not kept.
func (c *sentChats) find(agent string, digest uint64) *protocol.Chat {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.last[agent] {
		if s.chat != nil && s.digest == digest {
			return s.chat
		}
	}
	return nil
}

// add keeps chat, sent of agent under digest.
func (c *sentChats) add(agent string, digest uint64, chat *protocol.Chat) {
	c.mu.Lock()
	defer c.mu.Unlock()
	last, ok := c.last[agent]
	if last[0].digest == digest {
		return
	}
	if !ok && len(c.last) >= sentChatsKept {
		c.last = nil
	}
	if c.last == nil {
		c.last = map[string][2]sentChat{}
	}
	c.last[agent] = [2]sentChat{{digest, chat}, last[0]}
}

// patch cuts reply's messages to those that follow what it repeats of
// base's, which was sent under baseDigest: base's from drop on, keep of
// them. Messages are compared by their contents, as each read makes its
// own, so what a reader puts back together is reply's whole.
func patch(reply, base *protocol.Chat, baseDigest uint64) {
	msgs, old := reply.GetMessages(), base.GetMessages()
	if len(msgs) == 0 {
		return
	}
	drop := slices.IndexFunc(old, func(m *protocol.ChatMessage) bool { return sameMessage(m, msgs[0]) })
	if drop < 0 {
		return // nothing of base's is left: the whole is as short
	}
	keep := 0
	for drop+keep < len(old) && keep < len(msgs) && sameMessage(old[drop+keep], msgs[keep]) {
		keep++
	}
	reply.Messages = msgs[keep:]
	reply.Base, reply.Drop, reply.Keep = baseDigest, int32(drop), int32(keep)
}

func sameMessage(a, b *protocol.ChatMessage) bool { return a == b || proto.Equal(a, b) }

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

// chatDigests are the digests of the conversations readers were sent.
var chatDigests digests

// digests remembers each shared chat's digest, by the chat itself. A
// reader polls an open conversation every two seconds, and the cache hands
// back the same chat until its log changes: digesting it anew would
// marshal the whole window each time. The chats are held weakly, so a
// conversation read again releases the one it replaced.
type digests struct {
	mu  sync.Mutex
	sum map[weak.Pointer[protocol.Chat]]uint64
}

// of is chat's digest, the one already taken while chat is the same.
func (d *digests) of(chat *protocol.Chat) uint64 {
	key := weak.Make(chat)
	d.mu.Lock()
	sum, ok := d.sum[key]
	d.mu.Unlock()
	if ok {
		return sum
	}
	sum = digest(chat)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sum == nil {
		d.sum = map[weak.Pointer[protocol.Chat]]uint64{}
	}
	if len(d.sum) >= 256 {
		for k := range d.sum {
			if k.Value() == nil {
				delete(d.sum, k)
			}
		}
	}
	d.sum[key] = sum
	return sum
}

// combine is one digest of two.
func combine(a, b uint64) uint64 {
	h := fnv.New64a()
	h.Write(binary.LittleEndian.AppendUint64(binary.LittleEndian.AppendUint64(nil, a), b))
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
