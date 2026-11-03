package daemon

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agents"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
	"github.com/ironpark/mako/internal/transport"
)

// Live replies: an agent that streams its reply to mako as it is written
// (agent.LiveReplies) posts the pieces to the live socket, the session's
// own or a subagent's, each post carrying the session's token. While a
// conversation is followed (WatchChat), one look-out for it watches its
// log, every liveTick while a reply streams and every liveIdle otherwise,
// and hands every follower the same ChatLive: what has streamed and is not
// yet logged, at most once a tick, and word the moment the log grows. A
// block is dropped once the log has it: when the log grows after a later
// block began, or after its step ended.

// liveTick and liveIdle are how often a followed log is looked at, while a
// reply streams and while none has lately; a tick is also as often as the
// followers hear of what streamed.
const (
	liveTick = 100 * time.Millisecond
	liveIdle = time.Second
)

// liveStale is how long a step that ended stays shown when its log does
// not grow, as when its turn was cut short.
const liveStale = 10 * time.Second

// liveArgs bounds what is kept of a tool call's arguments: enough to say
// what it does, not a file it writes.
const liveArgs = 2 << 10

// liveTokenEnv names, in a session's environment, what its posts carry to
// be taken as its own.
const liveTokenEnv = "MAKO_LIVE_TOKEN"

// livePost is what the mod sends: pieces of a step of the turn of the
// session's agent, or of its subagent agent, named turn:index, the first
// post of a step with none, and end with the last.
type livePost struct {
	Session string      `json:"session"`
	Token   string      `json:"token"`
	Agent   string      `json:"agent"`
	Step    string      `json:"step"`
	End     bool        `json:"end"`
	Pieces  []livePiece `json:"pieces"`
}

type livePiece struct {
	Kind  string `json:"k"` // text, thinking, tool, or input: a tool's arguments
	Index int32  `json:"i"`
	Text  string `json:"t"`
	Tool  string `json:"n"`
}

// liveHub keeps each conversation's reply streaming: a session's, and
// each of its subagents', by liveKey.
type liveHub struct {
	mu    sync.Mutex
	chats map[string]*liveChat
	seen  map[string]bool // the sessions that posted
}

type liveChat struct {
	step    string
	blocks  []*liveBlock
	posted  time.Time // when the last post came
	ended   time.Time // when the step ended, zero while it streams
	logSize int64     // the log's size when last looked at
	looked  time.Time
	dirty   bool // a post came since the followers last heard
	subs    map[chan *protocol.ChatLive]struct{}
	stop    context.CancelFunc // the look-out's, while followed
}

type liveBlock struct {
	index      int32
	kind, tool string
	text       strings.Builder
}

// liveKey names the conversation of session, or of its subagent agent.
func liveKey(session, agent string) string { return session + "\x00" + agent }

func (h *liveHub) chat(key string) *liveChat {
	if h.chats == nil {
		h.chats, h.seen = map[string]*liveChat{}, map[string]bool{}
	}
	c := h.chats[key]
	if c == nil {
		c = &liveChat{subs: map[chan *protocol.ChatLive]struct{}{}}
		h.chats[key] = c
	}
	return c
}

// take follows a post; first says it is the session's first.
func (h *liveHub) take(p livePost) (first bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.chat(liveKey(p.Session, p.Agent))
	first, h.seen[p.Session] = !h.seen[p.Session], true
	if p.Step != c.step {
		c.step, c.blocks, c.ended = p.Step, nil, time.Time{}
	}
	for _, piece := range p.Pieces {
		b := c.block(piece.Index, piece.Kind)
		switch piece.Kind {
		case "text":
			b.text.WriteString(piece.Text)
		case "input":
			if room := liveArgs - b.text.Len(); room > 0 {
				b.text.WriteString(piece.Text[:min(room, len(piece.Text))])
			}
		case "tool":
			b.kind, b.tool = "tool", piece.Tool
		}
	}
	c.posted, c.dirty = time.Now(), true
	if p.End {
		c.ended = c.posted
	}
	return first
}

// block is the step's block index, begun as kind if it is new.
func (c *liveChat) block(index int32, kind string) *liveBlock {
	for _, b := range c.blocks {
		if b.index == index {
			return b
		}
	}
	b := &liveBlock{index: index, kind: kind}
	c.blocks = append(c.blocks, b)
	return b
}

// look notes the log's size: when it grew, the blocks it now holds are
// dropped; a step left ended too long is dropped too. It reports whether
// the log grew, and marks the followers due to hear of any change.
func (c *liveChat) look(size int64) (grew bool) {
	before := len(c.blocks)
	if size > c.logSize {
		grew = true
		switch {
		case !c.ended.IsZero():
			c.blocks = nil
		case len(c.blocks) > 0:
			c.blocks = c.blocks[len(c.blocks)-1:]
		}
	}
	c.logSize, c.looked = size, time.Now()
	if !c.ended.IsZero() && time.Since(c.ended) > liveStale {
		c.blocks = nil
	}
	c.dirty = c.dirty || len(c.blocks) != before
	return grew
}

// streaming reports whether a reply streams, or did a moment ago.
func (c *liveChat) streaming() bool {
	return len(c.blocks) > 0 || time.Since(c.posted) < liveIdle
}

// live is what the followers hear: the blocks as they stand.
func (c *liveChat) live(logged bool) *protocol.ChatLive {
	out := &protocol.ChatLive{Logged: logged}
	for _, b := range c.blocks {
		out.Blocks = append(out.Blocks, &protocol.LiveBlock{Index: b.index, Kind: b.kind, Text: b.text.String(), Tool: b.tool})
	}
	return out
}

// tell hands every follower l, merged into one it has not taken yet.
func (c *liveChat) tell(l *protocol.ChatLive) {
	c.dirty = false
	for ch := range c.subs {
		select {
		case old := <-ch:
			l = &protocol.ChatLive{Logged: l.GetLogged() || old.GetLogged(), Blocks: l.GetBlocks()}
		default:
		}
		ch <- l
	}
}

// follow is what the conversation key's followers hear, the first at
// once, until stop; size says how large its log is now. The first
// follower starts its look-out, and the last stops it.
func (h *liveHub) follow(key string, size func() int64) (feed <-chan *protocol.ChatLive, stop func()) {
	ch := make(chan *protocol.ChatLive, 1)
	now := size()
	h.mu.Lock()
	c := h.chat(key)
	c.look(now)
	ch <- c.live(false)
	c.subs[ch] = struct{}{}
	if c.stop == nil {
		ctx, cancel := context.WithCancel(context.Background())
		c.stop = cancel
		go h.lookOut(ctx, key, size)
	}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		delete(c.subs, ch)
		if len(c.subs) == 0 && c.stop != nil {
			c.stop()
			c.stop = nil
		}
	}
}

// lookOut watches the log of the conversation key and tells its followers
// what changed, until ctx ends.
func (h *liveHub) lookOut(ctx context.Context, key string, size func() int64) {
	tick := time.NewTicker(liveTick)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		h.mu.Lock()
		c := h.chats[key]
		due := c != nil && (c.streaming() || time.Since(c.looked) >= liveIdle)
		h.mu.Unlock()
		if c == nil {
			return // forgotten
		}
		var now int64
		if due {
			now = size() // not under the lock: it reads the disk
		}
		h.mu.Lock()
		grew := due && c.look(now)
		if grew || c.dirty {
			c.tell(c.live(grew))
		}
		h.mu.Unlock()
	}
}

// blocks are what the conversation key's followers would hear now.
func (h *liveHub) blocks(key string) []*protocol.LiveBlock {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c := h.chats[key]; c != nil {
		return c.live(false).GetBlocks()
	}
	return nil
}

// forget drops a session's replies, its subagents' too, as it closes.
func (h *liveHub) forget(session string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for key, c := range h.chats {
		if strings.HasPrefix(key, session+"\x00") {
			if c.stop != nil {
				c.stop()
			}
			delete(h.chats, key)
		}
	}
	delete(h.seen, session)
}

// livePostLimit bounds one post.
const livePostLimit = 1 << 20

// listenLive serves the live socket in dir until ctx ends, setting
// liveSocket once it listens.
func (d *Daemon) listenLive(ctx context.Context, dir string) {
	d.liveSecret = make([]byte, 32)
	if _, err := rand.Read(d.liveSecret); err != nil {
		d.log.Warn("live replies: no secret", "err", err)
		return
	}
	ln, path, err := listenUnix(dir, "live.sock")
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
	if !hmac.Equal([]byte(p.Token), []byte(d.liveToken(p.Session))) {
		d.log.Warn("live replies: a post for a session not its own", "session", p.Session)
		http.Error(w, "not this session's", http.StatusForbidden)
		return
	}
	if d.live.take(p) {
		d.log.Info("live replies: streaming", "session", p.Session)
	}
	w.WriteHeader(http.StatusNoContent)
}

// liveToken is what session's posts carry: its own, of this daemon's
// secret, so a process of another session cannot post as it.
func (d *Daemon) liveToken(session string) string {
	mac := hmac.New(sha256.New, d.liveSecret)
	mac.Write([]byte(session))
	return hex.EncodeToString(mac.Sum(nil))
}

// withLive has session id's launch of an agent that streams its replies
// load mako's mod, when its version loads mods, with what its posts go to
// and carry.
func (d *Daemon) withLive(ctx context.Context, l *agent.Launch, agentName, id string) {
	live, ok := agents.As[agent.LiveReplies](agentName)
	if !ok || d.liveSocket == "" || len(l.Command) == 0 {
		return
	}
	run, err := config.RunDir(d.dir)
	if err != nil {
		return
	}
	folder, err := live.LiveMod(run)
	if err != nil {
		d.log.Warn("live replies: no mod", "err", err)
		return
	}
	args, want := live.LiveCheck(folder)
	if !probed(ctx, l.Command[0], args, want, 15*time.Second, func(out string) bool { return strings.Contains(out, want) }) {
		return
	}
	live.WithLive(l, folder)
	if l.Env == nil {
		l.Env = map[string]string{}
	}
	l.Env[agent.LiveSocketEnv], l.Env[liveTokenEnv] = d.liveSocket, d.liveToken(id)
}

// liveLog is the log of session s's conversation, or of its subagent
// agent's as the session's conversation names it; "" for none yet.
func (d *Daemon) liveLog(s *session.Session, agent string) string {
	if agent == "" {
		return s.TranscriptPath()
	}
	if cache := d.conversations.chatCache(s); cache != nil {
		return cache.Agent(agent).GetLog()
	}
	return ""
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
	id, agent := req.GetSessionId(), req.GetAgent()
	s, ok := d.sessions.get(id)
	if !ok {
		return fmt.Errorf("unknown session %s", id)
	}
	feed, stop := d.live.follow(liveKey(id, agent), func() int64 {
		fi, err := os.Stat(d.liveLog(s, agent))
		if err != nil {
			return 0
		}
		return fi.Size()
	})
	defer stop()
	gone := hungUp(stream)
	for {
		select {
		case l := <-feed:
			if err := stream.Send(protocol.TypeChatLive, env.GetId(), l); err != nil {
				return err
			}
		case err := <-gone:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
