package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transport"
)

// Live replies: an agent that streams its reply to mako as it is written
// (agent.LiveReplies) posts the pieces to the live socket; a front-end
// following the session's conversation (WatchChat) gets what has streamed
// and is not yet logged, and word the moment the log grows, read on the
// session's node every liveTick. A block is dropped once the log has it:
// when the log grows after a later block began, or after its step ended.

// liveTick is how often a followed session's log is looked at.
const liveTick = 200 * time.Millisecond

// liveStale is how long a step that ended stays shown when its log does
// not grow, as when its turn was cut short.
const liveStale = 10 * time.Second

// livePost is what the mod sends: pieces of a step of the session's turn,
// named turn:index, the first post of a step with none, and end with the
// last.
type livePost struct {
	Session string      `json:"session"`
	Step    string      `json:"step"`
	End     bool        `json:"end"`
	Pieces  []livePiece `json:"pieces"`
}

type livePiece struct {
	Kind  string `json:"k"` // text or tool
	Index int32  `json:"i"`
	Text  string `json:"t"`
	Tool  string `json:"n"`
}

// liveHub keeps each session's reply streaming.
type liveHub struct {
	mu    sync.Mutex
	chats map[string]*liveChat
}

type liveChat struct {
	step     string
	blocks   []*protocol.LiveBlock
	ended    time.Time // when the step ended, zero while it streams
	logSize  int64     // the log's size when last looked at
	watchers map[chan struct{}]struct{}
}

func (h *liveHub) chat(session string) *liveChat {
	if h.chats == nil {
		h.chats = map[string]*liveChat{}
	}
	c := h.chats[session]
	if c == nil {
		c = &liveChat{watchers: map[chan struct{}]struct{}{}}
		h.chats[session] = c
	}
	return c
}

// take follows a post, and tells the session's watchers.
func (h *liveHub) take(p livePost) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.chat(p.Session)
	if p.Step != c.step {
		c.step, c.blocks, c.ended = p.Step, nil, time.Time{}
	}
	for _, piece := range p.Pieces {
		i := slices.IndexFunc(c.blocks, func(b *protocol.LiveBlock) bool { return b.GetIndex() == piece.Index })
		if i < 0 {
			c.blocks = append(c.blocks, &protocol.LiveBlock{Index: piece.Index, Kind: piece.Kind})
			i = len(c.blocks) - 1
		}
		b := c.blocks[i]
		b.Text += piece.Text
		if piece.Tool != "" {
			b.Tool = piece.Tool
		}
	}
	if p.End {
		c.ended = time.Now()
	}
	c.notify()
}

func (c *liveChat) notify() {
	for ch := range c.watchers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// logged notes the log's size: when it grew, the blocks it now holds are
// dropped, and grew says so; changed says the blocks changed, as a step
// left ended too long is dropped too.
func (h *liveHub) logged(session string, size int64) (grew, changed bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.chat(session)
	before := len(c.blocks)
	if size > c.logSize {
		grew = true
		if !c.ended.IsZero() {
			c.blocks = nil
		} else if len(c.blocks) > 0 {
			last := c.blocks[len(c.blocks)-1].GetIndex()
			c.blocks = slices.DeleteFunc(c.blocks, func(b *protocol.LiveBlock) bool { return b.GetIndex() < last })
		}
	}
	c.logSize = size
	if !c.ended.IsZero() && time.Since(c.ended) > liveStale {
		c.blocks = nil
	}
	return grew, len(c.blocks) != before
}

// blocks are a copy of the session's blocks.
func (h *liveHub) blocks(session string) []*protocol.LiveBlock {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []*protocol.LiveBlock
	for _, b := range h.chat(session).blocks {
		out = append(out, &protocol.LiveBlock{Index: b.GetIndex(), Kind: b.GetKind(), Text: b.GetText(), Tool: b.GetTool()})
	}
	return out
}

// watch is word of each change to the session's blocks, until stop.
func (h *liveHub) watch(session string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	h.chat(session).watchers[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if c := h.chats[session]; c != nil {
			delete(c.watchers, ch)
		}
	}
}

// forget drops a session's reply, as it closes.
func (h *liveHub) forget(session string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.chats, session)
}

// livePostLimit bounds one post.
const livePostLimit = 1 << 20

// listenLive serves the live socket in dir until ctx ends, setting
// liveSocket once it listens. Like the hook socket, it sits in the
// private run directory, so only this user's processes reach it.
func (d *Daemon) listenLive(ctx context.Context, dir string) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		d.log.Warn("live replies: no run directory", "err", err)
		return
	}
	path := filepath.Join(dir, "live.sock")
	_ = os.Remove(path) // left by a daemon that did not stop cleanly
	ln, err := net.Listen("unix", path)
	if err != nil {
		d.log.Warn("live replies: not listening", "err", err)
		return
	}
	srv := &http.Server{Handler: http.HandlerFunc(d.serveLive), ReadHeaderTimeout: 5 * time.Second}
	context.AfterFunc(ctx, func() { _ = srv.Close() })
	d.liveSocket = path
	go func() { _ = srv.Serve(ln) }()
}

func (d *Daemon) serveLive(w http.ResponseWriter, r *http.Request) {
	var p livePost
	if r.Method != http.MethodPost || json.NewDecoder(http.MaxBytesReader(w, r.Body, livePostLimit)).Decode(&p) != nil {
		http.Error(w, "a live post", http.StatusBadRequest)
		return
	}
	if _, ok := d.sessions.get(p.Session); !ok {
		http.Error(w, "no such session", http.StatusNotFound)
		return
	}
	d.live.take(p)
	w.WriteHeader(http.StatusNoContent)
}

// withLive has a launch of an agent that streams its replies load mako's
// mod, when its version loads mods: it is asked once for each program.
func (d *Daemon) withLive(ctx context.Context, l *agent.Launch, driver agent.Driver, dir string) {
	live, ok := driver.(agent.LiveReplies)
	if !ok || d.liveSocket == "" || len(l.Command) == 0 {
		return
	}
	folder, err := live.LiveMod(dir)
	if err != nil {
		d.log.Warn("live replies: no mod", "err", err)
		return
	}
	args, want := live.LiveCheck(folder)
	if !hasOutput(ctx, l.Command[0], args, want) {
		return
	}
	live.WithLive(l, folder)
	if l.Env == nil {
		l.Env = map[string]string{}
	}
	l.Env[agent.LiveSocketEnv] = d.liveSocket
}

// handleWatchChat follows a session's conversation for a front-end, on its
// node, until the front-end hangs up.
func (d *Daemon) handleWatchChat(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	req, err := protocol.Body[*protocol.WatchChat](env)
	if err != nil {
		return err
	}
	if !d.local(req.GetNodeId()) {
		return d.relayToPeer(ctx, protocol.TypeWatchChat, req, stream)
	}
	id := req.GetSessionId()
	s, ok := d.sessions.get(id)
	if !ok {
		return fmt.Errorf("unknown session %s", id)
	}
	size := func() int64 {
		fi, err := os.Stat(s.TranscriptPath())
		if err != nil {
			return 0
		}
		return fi.Size()
	}
	changes, stop := d.live.watch(id)
	defer stop()
	send := func(logged bool) error {
		return stream.Send(protocol.TypeChatLive, env.GetId(), &protocol.ChatLive{Logged: logged, Blocks: d.live.blocks(id)})
	}
	d.live.logged(id, size())
	if err := send(false); err != nil {
		return err
	}
	// The front-end sends nothing more; its closing the stream ends it.
	gone := make(chan error, 1)
	go func() {
		_, err := stream.Recv()
		gone <- err
	}()
	tick := time.NewTicker(liveTick)
	defer tick.Stop()
	for {
		var err error
		select {
		case <-changes:
			err = send(false)
		case <-tick.C:
			if grew, changed := d.live.logged(id, size()); grew || changed {
				err = send(grew)
			}
		case err := <-gone:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
		if err != nil {
			return err
		}
	}
}
