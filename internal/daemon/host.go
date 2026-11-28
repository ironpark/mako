package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
	"github.com/ironpark/mako/internal/transcript"
	"github.com/ironpark/mako/internal/transport"
	"github.com/ironpark/mako/pkg/fsutil"
)

// The session host: for each session its front-ends subscribe to, the
// daemon running it keeps what they share of it, its info, conversation,
// streaming reply, draft, prompt queue and the front-ends themselves, and
// numbers each change, so that a front-end that comes back is sent what
// it missed, and one that takes over from another starts where it was
// left. A front-end follows it with subscribe_session and changes it with
// session_action, on any node: the owning node's daemon is the host.

const (
	// hostRing and hostRingBytes bound the events a host keeps to replay.
	hostRing      = 2048
	hostRingBytes = 4 << 20
	// hostLinger is how long a host outlives its last subscriber, for one
	// that comes back to be replayed what it missed.
	hostLinger = 10 * time.Minute
	// hostTick is how often a host looks at its session for what no
	// change of its tells, and at its queue.
	hostTick = time.Second
	// hostApprovalGrace is how long a session's approvals still wait for
	// an answer in a window after its last subscriber that shows them
	// left: a front-end handing the session to another.
	hostApprovalGrace = 2 * time.Minute
	// hostRemembered is how many actions of each front-end a host knows
	// it took, to take each once.
	hostRemembered = 256
	// hostQueueMax bounds a session's prompt queue, and hostDraftMax its
	// draft.
	hostQueueMax = 32
	hostDraftMax = 64 << 10
	// hostSaveDelay gathers drafts written in a burst into one save.
	hostSaveDelay = time.Second
)

// hostHub holds the session hosts of this daemon, the sequence their
// events are numbered from, and the sessions' drafts, which outlast the
// daemon.
type hostHub struct {
	mu     sync.Mutex
	epoch  uint64 // the run's number, in the seq's high half
	n      uint64 // the events numbered this run
	hosts  map[string]*sessionHost
	drafts map[string]*protocol.HostDraft
	path   string      // host.json, "" before load
	save   *time.Timer // a save due
}

// hostFile is host.json.
type hostFile struct {
	Epoch  uint64               `json:"epoch"`
	Drafts map[string]hostDraft `json:"drafts,omitempty"`
}

type hostDraft struct {
	Text     string    `json:"text"`
	ClientID string    `json:"client_id,omitempty"`
	At       time.Time `json:"at"`
}

// load reads the drafts saved in dir and starts a new run of seqs, one
// past the last saved, so a front-end's seq of an earlier run is never
// taken for one of this.
func (hub *hostHub) load(dir string, keep func(id string) bool) error {
	path := filepath.Join(dir, "host.json")
	var f hostFile
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &f); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	hub.path = path
	hub.epoch = (f.Epoch + 1) << 32
	hub.n = 0
	hub.drafts = map[string]*protocol.HostDraft{}
	for id, dr := range f.Drafts {
		if keep(id) && dr.Text != "" {
			hub.drafts[id] = &protocol.HostDraft{Text: dr.Text, ClientId: dr.ClientID, At: timestamppb.New(dr.At)}
		}
	}
	return hub.saveLocked()
}

// saveLocked writes host.json now.
func (hub *hostHub) saveLocked() error {
	if hub.path == "" {
		return nil
	}
	f := hostFile{Epoch: hub.epoch >> 32, Drafts: map[string]hostDraft{}}
	for id, dr := range hub.drafts {
		f.Drafts[id] = hostDraft{Text: dr.GetText(), ClientID: dr.GetClientId(), At: dr.GetAt().AsTime()}
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.Write(hub.path, data)
}

// setDraft keeps session id's draft, saved soon.
func (hub *hostHub) setDraft(id string, dr *protocol.HostDraft, log func(error)) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.drafts == nil {
		hub.drafts = map[string]*protocol.HostDraft{}
	}
	if dr.GetText() == "" {
		delete(hub.drafts, id)
	} else {
		hub.drafts[id] = dr
	}
	if hub.path == "" || hub.save != nil {
		return
	}
	hub.save = time.AfterFunc(hostSaveDelay, func() {
		hub.mu.Lock()
		defer hub.mu.Unlock()
		hub.save = nil
		if err := hub.saveLocked(); err != nil {
			log(err)
		}
	})
}

// flush writes a save that is due now.
func (hub *hostHub) flush() error {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.save == nil {
		return nil
	}
	hub.save.Stop()
	hub.save = nil
	return hub.saveLocked()
}

// next numbers an event. Seqs grow across all of this daemon's hosts.
func (hub *hostHub) next() uint64 {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.epoch == 0 {
		hub.epoch = 1 << 32 // never loaded, as in a test
	}
	hub.n++
	return hub.epoch | hub.n
}

// now is the seq of the last event numbered, the epoch for none.
func (hub *hostHub) now() (seq, epoch uint64) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.epoch == 0 {
		hub.epoch = 1 << 32
	}
	return hub.epoch | hub.n, hub.epoch
}

// host is session s's host, started for it if it has none.
func (d *Daemon) host(ctx context.Context, s *session.Session) *sessionHost {
	hub := &d.hosts
	hub.mu.Lock()
	h := hub.hosts[s.ID()]
	if h != nil {
		hub.mu.Unlock()
		<-h.ready
		return h
	}
	if hub.hosts == nil {
		hub.hosts = map[string]*sessionHost{}
	}
	if hub.epoch == 0 {
		hub.epoch = 1 << 32
	}
	// The host outlives the request that started it: the daemon's run
	// bounds it.
	run, stop := context.WithCancel(d.ctx)
	h = &sessionHost{
		d: d, id: s.ID(), s: s, stop: stop,
		state:   &protocol.HostState{Seq: hub.epoch | hub.n, Draft: hub.drafts[s.ID()], Queue: &protocol.HostQueue{}, Clients: &protocol.HostClients{}},
		floor:   hub.epoch | hub.n,
		subs:    map[*hostSub]struct{}{},
		clients: map[string]*hostClient{},
		done:    map[string]*hostDone{},
		ready:   make(chan struct{}),
		stopped: make(chan struct{}),
	}
	hub.hosts[s.ID()] = h
	hub.mu.Unlock()

	h.refreshInfo(s)
	h.refreshChat(ctx, s)
	h.mu.Lock()
	h.lingerLocked()
	h.mu.Unlock()
	close(h.ready)
	go h.run(run)
	return h
}

// forgetHost stops session id's host, as the session closes for good,
// and drops its draft.
func (d *Daemon) forgetHost(id string) {
	hub := &d.hosts
	hub.mu.Lock()
	h := hub.hosts[id]
	delete(hub.hosts, id)
	hub.mu.Unlock()
	// Not waiting for it to be ready: the session's close holds what its
	// start may wait on.
	if h != nil {
		h.mu.Lock()
		h.shutLocked()
		h.mu.Unlock()
	}
	hub.setDraft(id, nil, func(err error) { d.log.Warn("session host: drafts not saved", "err", err) })
}

// sessionHost is one session's host.
type sessionHost struct {
	d  *Daemon
	id string

	actMu sync.Mutex // one action, or queued prompt typed, at a time

	mu        sync.Mutex
	s         *session.Session    // the session as it runs now
	state     *protocol.HostState // its fields are replaced, never changed in place
	infoKey   []byte              // what of state.Info an event is sent for
	ring      []*protocol.HostEvent
	ringBytes int
	floor     uint64 // every event after it is in ring
	subs      map[*hostSub]struct{}
	clients   map[string]*hostClient
	done      map[string]*hostDone // by client
	queue     []hostQueued
	queuedN   uint64
	delivered time.Time // when a queued prompt was last typed
	linger    *time.Timer
	stop      context.CancelFunc
	ready     chan struct{} // closed once the host has its state
	stopped   chan struct{} // closed when it stops
	shut      bool
}

type hostSub struct {
	client string
	cursor uint64        // the seq of the last event it was sent
	wake   chan struct{} // there is more for it
}

type hostClient struct {
	name      string
	since     time.Time
	streams   int
	approvals int
}

type hostDone struct {
	results map[uint64]*protocol.SessionActionResult
	order   []uint64
}

type hostQueued struct {
	item *protocol.HostQueued
	keys []byte
}

func (h *sessionHost) session() *session.Session {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.s
}

// emitLocked numbers ev, keeps it and wakes the subscribers: the state is
// already as ev leaves it.
func (h *sessionHost) emitLocked(origin *protocol.HostOrigin, ev *protocol.HostEvent) uint64 {
	ev.Seq = h.d.hosts.next()
	ev.Origin = origin
	h.state.Seq = ev.Seq
	h.ring = append(h.ring, ev)
	h.ringBytes += proto.Size(ev)
	for len(h.ring) > 1 && (len(h.ring) > hostRing || h.ringBytes > hostRingBytes) {
		h.floor = h.ring[0].GetSeq()
		h.ringBytes -= proto.Size(h.ring[0])
		h.ring[0] = nil
		h.ring = h.ring[1:]
	}
	for sub := range h.subs {
		select {
		case sub.wake <- struct{}{}:
		default:
		}
	}
	return ev.Seq
}

// stateLocked is a copy of the state to send: its fields are shared, as
// they are never changed in place.
func (h *sessionHost) stateLocked() *protocol.HostState {
	st := h.state
	return &protocol.HostState{Seq: st.Seq, Info: st.Info, Chat: st.Chat, Live: st.Live, Draft: st.Draft, Queue: st.Queue, Clients: st.Clients}
}

// afterLocked is the events after seq, those that one after them makes
// moot left out: of info, the streaming reply, the draft, the queue and
// the clients, each event carries the whole, so the last is enough.
func (h *sessionHost) afterLocked(seq uint64) []*protocol.HostEvent {
	i := sort.Search(len(h.ring), func(i int) bool { return h.ring[i].GetSeq() > seq })
	events := h.ring[i:]
	last := map[string]int{}
	for j, ev := range events {
		if k := wholeKind(ev); k != "" {
			last[k] = j
		}
	}
	out := make([]*protocol.HostEvent, 0, len(events))
	for j, ev := range events {
		if k := wholeKind(ev); k != "" && last[k] != j {
			continue
		}
		out = append(out, ev)
	}
	return out
}

// wholeKind names the kind of an event that carries its part of the
// state whole, "" for one that does not.
func wholeKind(ev *protocol.HostEvent) string {
	switch ev.GetBody().(type) {
	case *protocol.HostEvent_Info:
		return "info"
	case *protocol.HostEvent_Live:
		return "live"
	case *protocol.HostEvent_Draft:
		return "draft"
	case *protocol.HostEvent_Queue:
		return "queue"
	case *protocol.HostEvent_Clients:
		return "clients"
	}
	return ""
}

// subscribe adds a subscriber for req and says what it is sent first:
// what it missed after req's last_seen_seq, if the host has all of it,
// else the host's state.
func (h *sessionHost) subscribe(req *protocol.SubscribeSession) (*hostSub, *protocol.HostSubscribed, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.shut {
		return nil, nil, errors.New("the session closed")
	}
	_, epoch := h.d.hosts.now()
	first := &protocol.HostSubscribed{Epoch: epoch}
	if last := req.GetLastSeenSeq(); last != 0 && last >= h.floor && last <= h.state.GetSeq() {
		first.Replay = h.afterLocked(last)
	} else {
		first.State = h.stateLocked()
	}
	sub := &hostSub{client: req.GetClientId(), cursor: h.state.GetSeq(), wake: make(chan struct{}, 1)}
	h.subs[sub] = struct{}{}
	if h.linger != nil {
		h.linger.Stop()
		h.linger = nil
	}
	c := h.clients[sub.client]
	if c == nil {
		c = &hostClient{name: req.GetClientName(), since: time.Now()}
		h.clients[sub.client] = c
	}
	c.streams++
	if req.GetApprovals() {
		c.approvals++
	}
	if req.GetClientName() != "" {
		c.name = req.GetClientName()
	}
	h.clientsChangedLocked()
	return sub, first, nil
}

// unsubscribe takes sub away.
func (h *sessionHost) unsubscribe(sub *hostSub, approvals bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subs, sub)
	if c := h.clients[sub.client]; c != nil {
		c.streams--
		if approvals {
			c.approvals--
		}
		if c.streams <= 0 {
			delete(h.clients, sub.client)
		}
	}
	if !h.shut {
		h.clientsChangedLocked()
		h.lingerLocked()
	}
}

// clientsChangedLocked sends who is subscribed, if it changed.
func (h *sessionHost) clientsChangedLocked() {
	out := &protocol.HostClients{}
	for id, c := range h.clients {
		out.Clients = append(out.Clients, &protocol.HostClient{Id: id, Name: c.name, Since: timestamppb.New(c.since), Approvals: c.approvals > 0})
	}
	slices.SortFunc(out.Clients, func(a, b *protocol.HostClient) int {
		if c := a.GetSince().AsTime().Compare(b.GetSince().AsTime()); c != 0 {
			return c
		}
		return compareStrings(a.GetId(), b.GetId())
	})
	if proto.Equal(out, h.state.GetClients()) {
		return
	}
	h.state.Clients = out
	h.emitLocked(nil, &protocol.HostEvent{Body: &protocol.HostEvent_Clients{Clients: out}})
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// lingerLocked stops the host hostLinger after its last subscriber left,
// unless one comes back, or its queue still waits.
func (h *sessionHost) lingerLocked() {
	if len(h.subs) > 0 || h.linger != nil || h.shut {
		return
	}
	h.linger = time.AfterFunc(hostLinger, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.linger = nil
		if len(h.subs) > 0 || h.shut {
			return
		}
		if len(h.queue) > 0 {
			h.lingerLocked()
			return
		}
		hub := &h.d.hosts
		hub.mu.Lock()
		if hub.hosts[h.id] == h {
			delete(hub.hosts, h.id)
		}
		hub.mu.Unlock()
		h.shutLocked()
	})
}

// shutLocked stops the host: its subscribers are told the session is no
// longer hosted, and start over with a new host if it still runs.
func (h *sessionHost) shutLocked() {
	if h.shut {
		return
	}
	h.shut = true
	if h.linger != nil {
		h.linger.Stop()
		h.linger = nil
	}
	if h.stop != nil {
		h.stop()
	}
	close(h.stopped)
}

// pending is what sub is sent next: the events after its cursor, or the
// state again when it fell so far behind that the host no longer has
// them.
func (h *sessionHost) pending(sub *hostSub) ([]*protocol.HostEvent, *protocol.HostSubscribed) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if sub.cursor < h.floor {
		_, epoch := h.d.hosts.now()
		sub.cursor = h.state.GetSeq()
		return nil, &protocol.HostSubscribed{State: h.stateLocked(), Epoch: epoch}
	}
	i := sort.Search(len(h.ring), func(i int) bool { return h.ring[i].GetSeq() > sub.cursor })
	events := slices.Clone(h.ring[i:])
	sub.cursor = h.state.GetSeq()
	return events, nil
}

// run follows the host's session, and the one that takes its place when
// it sleeps and wakes, until ctx ends.
func (h *sessionHost) run(ctx context.Context) {
	tick := time.NewTicker(hostTick)
	defer tick.Stop()
	for {
		s := h.session()
		h.follow(ctx, s)
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			if next, ok := h.d.sessions.get(h.id); ok && next != s {
				h.mu.Lock()
				h.s = next
				h.mu.Unlock()
				break
			}
		}
	}
}

// follow sends what changes of s until it ends or ctx does.
func (h *sessionHost) follow(ctx context.Context, s *session.Session) {
	h.refreshInfo(s)
	h.refreshChat(ctx, s)
	size := func() int64 {
		fi, err := os.Stat(s.TranscriptPath())
		if err != nil {
			return 0
		}
		return fi.Size()
	}
	makePoll, screen := screenPollerFactory(&h.d.live, s)
	feed, stop := h.d.live.followPolled(liveKey(h.id, ""), size, screen, makePoll)
	defer stop()
	tick := time.NewTicker(hostTick)
	defer tick.Stop()
	for {
		changed := s.Changed()
		select {
		case <-ctx.Done():
			return
		case <-s.Done():
			h.refreshInfo(s)
			return
		case <-changed:
			if h.refreshInfo(s) {
				h.refreshChat(ctx, s)
			}
		case l := <-feed:
			if l.GetLogged() {
				h.refreshChat(ctx, s)
			}
			h.setLive(l)
		case <-tick.C:
			h.refreshInfo(s)
			h.deliver(ctx, s)
		}
	}
}

// refreshInfo sends s's info if it changed, and reports whether its
// conversation may have: it is another, or a turn ended.
func (h *sessionHost) refreshInfo(s *session.Session) (chat bool) {
	info := s.Info()
	if entry, ok := h.d.savedEntry(h.id); ok {
		info.ParentSessionId, info.ParentNodeId = entry.ParentID, entry.ParentNodeID
	}
	h.d.withHead(info)
	// The last output moves with every byte: it is sent with the rest,
	// not for itself.
	last := info.LastOutput
	info.LastOutput = nil
	key, _ := proto.MarshalOptions{Deterministic: true}.Marshal(info)
	info.LastOutput = last
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.shut || bytes.Equal(key, h.infoKey) {
		return false
	}
	prev := h.state.GetInfo()
	chat = prev == nil || prev.GetConversationId() != info.GetConversationId() || prev.GetTurns() != info.GetTurns()
	h.infoKey = key
	h.state.Info = info
	h.emitLocked(nil, &protocol.HostEvent{Body: &protocol.HostEvent_Info{Info: info}})
	return chat
}

// refreshChat reads s's conversation and sends what changed of it.
func (h *sessionHost) refreshChat(ctx context.Context, s *session.Session) {
	chat, err := h.d.chatForSession(ctx, s, transcript.Span{})
	if err != nil {
		h.d.log.Debug("session host: conversation not read", "session", h.id, "err", err)
		return
	}
	// The conversation is the cache's, shared: a shallow copy carries the
	// usage reported over the session's own.
	reply := shallow(chat)
	reply.Usage = withReported(chat.GetUsage(), s.Reported())
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.shut {
		return
	}
	p := chatPatch(h.state.GetChat(), reply)
	if p == nil {
		return
	}
	h.state.Chat = reply
	h.emitLocked(nil, &protocol.HostEvent{Body: &protocol.HostEvent_Chat{Chat: p}})
}

// chatPatch is what makes cur of old, nil when they are the same.
func chatPatch(old, cur *protocol.Chat) *protocol.HostChatPatch {
	p := &protocol.HostChatPatch{
		Conversation: cur.GetConversation(), Truncated: cur.GetTruncated(), Start: cur.GetStart(),
		Usage: cur.GetUsage(), Agents: cur.GetAgents(), Supported: cur.GetSupported(),
	}
	msgs := cur.GetMessages()
	if old == nil || old.GetConversation() != cur.GetConversation() {
		p.Whole, p.Messages = true, msgs
		return p
	}
	prev := old.GetMessages()
	drop, keep := 0, 0
	if len(msgs) > 0 {
		if drop = slices.IndexFunc(prev, func(m *protocol.ChatMessage) bool { return sameMessage(m, msgs[0]) }); drop < 0 {
			p.Whole, p.Messages = true, msgs
			return p
		}
		for drop+keep < len(prev) && keep < len(msgs) && sameMessage(prev[drop+keep], msgs[keep]) {
			keep++
		}
	}
	if drop == 0 && keep == len(prev) && keep == len(msgs) &&
		old.GetTruncated() == cur.GetTruncated() && old.GetStart() == cur.GetStart() && old.GetSupported() == cur.GetSupported() &&
		proto.Equal(old.GetUsage(), cur.GetUsage()) && slices.EqualFunc(old.GetAgents(), cur.GetAgents(), func(a, b *protocol.SubAgent) bool { return proto.Equal(a, b) }) {
		return nil
	}
	p.Drop, p.Keep, p.Messages = int32(drop), int32(keep), msgs[keep:]
	return p
}

// setLive sends what streams of the reply, if it changed.
func (h *sessionHost) setLive(l *protocol.ChatLive) {
	live := &protocol.ChatLive{Blocks: l.GetBlocks()}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.shut || proto.Equal(live, h.state.GetLive()) {
		return
	}
	if h.state.GetLive() == nil && len(live.GetBlocks()) == 0 {
		h.state.Live = live
		return
	}
	h.state.Live = live
	h.emitLocked(nil, &protocol.HostEvent{Body: &protocol.HostEvent_Live{Live: live}})
}

// deliver types the first queued prompt once the agent has settled at its
// prompt, as an agent's messages are.
func (h *sessionHost) deliver(ctx context.Context, s *session.Session) {
	h.mu.Lock()
	if len(h.queue) == 0 {
		h.mu.Unlock()
		return
	}
	first, delivered := h.queue[0], h.delivered
	h.mu.Unlock()
	at, since := s.AtPrompt()
	now := time.Now()
	if !at || now.Sub(since) < deliverySettle || now.Sub(s.LastInput()) < deliveryQuiet ||
		!since.After(delivered) && now.Sub(delivered) < deliveryRetry {
		return
	}
	// An action typing meanwhile goes first: the next tick tries again.
	if !h.actMu.TryLock() {
		return
	}
	defer h.actMu.Unlock()
	h.mu.Lock()
	if len(h.queue) == 0 || h.queue[0].item != first.item {
		h.mu.Unlock()
		return // cancelled meanwhile
	}
	h.mu.Unlock()
	if err := h.d.sendToSession(ctx, s, first.item.GetText(), first.keys); err != nil {
		h.d.log.Warn("session host: queued prompt not typed", "session", h.id, "err", err)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.delivered = time.Now()
	h.queue = slices.DeleteFunc(h.queue, func(q hostQueued) bool { return q.item == first.item })
	h.queueChangedLocked(first.item.GetOrigin())
	h.emitLocked(first.item.GetOrigin(), &protocol.HostEvent{Body: &protocol.HostEvent_Prompt{Prompt: &protocol.HostPrompt{Text: first.item.GetText(), QueuedId: first.item.GetId()}}})
}

func (h *sessionHost) queueChangedLocked(origin *protocol.HostOrigin) uint64 {
	q := &protocol.HostQueue{}
	for _, item := range h.queue {
		q.Items = append(q.Items, item.item)
	}
	h.state.Queue = q
	return h.emitLocked(origin, &protocol.HostEvent{Body: &protocol.HostEvent_Queue{Queue: q}})
}

// act takes a front-end's action, once for each of its client_seq.
func (h *sessionHost) act(ctx context.Context, r *protocol.SessionAction, fromPeer bool) (*protocol.SessionActionResult, error) {
	client, seq := r.GetClientId(), r.GetClientSeq()
	if client == "" {
		return nil, errors.New("a session action names its front-end (client_id)")
	}
	h.actMu.Lock()
	defer h.actMu.Unlock()
	h.mu.Lock()
	shut := h.shut
	var took *protocol.SessionActionResult
	if done := h.done[client]; done != nil && seq != 0 {
		took = done.results[seq]
	}
	h.mu.Unlock()
	if shut {
		return nil, errors.New("the session closed")
	}
	if took != nil {
		out := proto.CloneOf(took)
		out.Duplicate = true
		return out, nil
	}
	origin := &protocol.HostOrigin{ClientId: client, ClientSeq: seq}
	var res *protocol.SessionActionResult
	var err error
	switch a := r.GetAction().(type) {
	case *protocol.SessionAction_SetDraft:
		res, err = h.setDraft(origin, a.SetDraft.GetText())
	case *protocol.SessionAction_SendPrompt:
		res, err = h.sendPrompt(ctx, origin, a.SendPrompt)
	case *protocol.SessionAction_CancelQueued:
		res, err = h.cancelQueued(origin, a.CancelQueued.GetId())
	case *protocol.SessionAction_AnswerApproval:
		res, err = h.answerApproval(origin, a.AnswerApproval, fromPeer)
	default:
		err = errors.New("a session action names what to do")
	}
	if err != nil {
		return nil, err
	}
	if seq != 0 {
		h.mu.Lock()
		h.rememberLocked(client, seq, res)
		h.mu.Unlock()
	}
	return res, nil
}

func (h *sessionHost) rememberLocked(client string, seq uint64, res *protocol.SessionActionResult) {
	done := h.done[client]
	if done == nil {
		done = &hostDone{results: map[uint64]*protocol.SessionActionResult{}}
		h.done[client] = done
	}
	done.results[seq] = res
	done.order = append(done.order, seq)
	for len(done.order) > hostRemembered {
		delete(done.results, done.order[0])
		done.order = done.order[1:]
	}
}

func (h *sessionHost) setDraft(origin *protocol.HostOrigin, text string) (*protocol.SessionActionResult, error) {
	if len(text) > hostDraftMax {
		return nil, fmt.Errorf("a draft is at most %d bytes", hostDraftMax)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.state.GetDraft().GetText() == text {
		return &protocol.SessionActionResult{}, nil
	}
	return &protocol.SessionActionResult{Seq: h.draftLocked(origin, text)}, nil
}

func (h *sessionHost) draftLocked(origin *protocol.HostOrigin, text string) uint64 {
	dr := &protocol.HostDraft{Text: text, ClientId: origin.GetClientId(), At: timestamppb.Now()}
	h.state.Draft = dr
	h.d.hosts.setDraft(h.id, dr, func(err error) { h.d.log.Warn("session host: drafts not saved", "err", err) })
	return h.emitLocked(origin, &protocol.HostEvent{Body: &protocol.HostEvent_Draft{Draft: dr}})
}

// clearDraftLocked empties the draft origin's front-end wrote last.
func (h *sessionHost) clearDraftLocked(origin *protocol.HostOrigin) uint64 {
	dr := h.state.GetDraft()
	if dr.GetText() == "" || dr.GetClientId() != origin.GetClientId() {
		return 0
	}
	return h.draftLocked(origin, "")
}

func (h *sessionHost) sendPrompt(ctx context.Context, origin *protocol.HostOrigin, p *protocol.HostSendPrompt) (*protocol.SessionActionResult, error) {
	text, keys := p.GetText(), p.GetKeys()
	if p.GetQueue() {
		if text == "" {
			return nil, errors.New("a queued prompt has text")
		}
		if len(keys) == 0 {
			keys = []byte{'\r'}
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		if len(h.queue) >= hostQueueMax {
			return nil, fmt.Errorf("the queue holds %d prompts at most", hostQueueMax)
		}
		h.queuedN++
		item := &protocol.HostQueued{Id: "q" + strconv.FormatUint(h.queuedN, 10), Text: text, Origin: origin, At: timestamppb.Now()}
		h.queue = append(h.queue, hostQueued{item: item, keys: keys})
		res := &protocol.SessionActionResult{Queued: true, Seq: h.queueChangedLocked(origin)}
		if p.GetClearDraft() {
			if seq := h.clearDraftLocked(origin); seq != 0 {
				res.Seq = seq
			}
		}
		return res, nil
	}
	if text == "" && len(keys) == 0 {
		return nil, errors.New("a prompt has text or keys")
	}
	if err := h.d.sendToSession(ctx, h.session(), text, keys); err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	res := &protocol.SessionActionResult{Seq: h.emitLocked(origin, &protocol.HostEvent{Body: &protocol.HostEvent_Prompt{Prompt: &protocol.HostPrompt{Text: text}}})}
	if p.GetClearDraft() {
		if seq := h.clearDraftLocked(origin); seq != 0 {
			res.Seq = seq
		}
	}
	return res, nil
}

func (h *sessionHost) cancelQueued(origin *protocol.HostOrigin, id string) (*protocol.SessionActionResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	i := slices.IndexFunc(h.queue, func(q hostQueued) bool { return q.item.GetId() == id })
	if i < 0 {
		return nil, fmt.Errorf("no queued prompt %q: it went to the agent, or was cancelled", id)
	}
	h.queue = slices.Delete(h.queue, i, i+1)
	return &protocol.SessionActionResult{Seq: h.queueChangedLocked(origin)}, nil
}

func (h *sessionHost) answerApproval(origin *protocol.HostOrigin, r *protocol.AnswerApproval, fromPeer bool) (*protocol.SessionActionResult, error) {
	if err := h.d.answerApproval(h.session(), r, fromPeer); err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return &protocol.SessionActionResult{Seq: h.emitLocked(origin, &protocol.HostEvent{Body: &protocol.HostEvent_ApprovalAnswered{
		ApprovalAnswered: &protocol.HostApprovalAnswered{ApprovalId: r.GetApprovalId(), Decision: r.GetDecision()},
	}})}, nil
}

// handleSubscribeSession feeds a front-end one session's host until it
// hangs up, the session closes, or the daemon stops.
func (d *Daemon) handleSubscribeSession(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) (err error) {
	req, err := protocol.Body[*protocol.SubscribeSession](env)
	if err != nil {
		return err
	}
	if !d.local(req.GetNodeId()) && !d.isDirectSSH(req.GetNodeId()) {
		return d.relayToPeer(ctx, protocol.TypeSubscribeSession, req, stream)
	}
	if req.GetClientId() == "" {
		return errors.New("a subscription names its front-end (client_id)")
	}
	s, ok := d.sessions.get(req.GetSessionId())
	if !ok {
		return fmt.Errorf("unknown session %s", req.GetSessionId())
	}
	h := d.host(ctx, s)
	sub, first, err := h.subscribe(req)
	if err != nil {
		return err
	}
	defer h.unsubscribe(sub, req.GetApprovals())
	if req.GetApprovals() {
		defer d.approvals.holdSession(s.ID(), hostApprovalGrace)()
	}
	hang := watchStreamHangup(stream, nil)
	defer func() {
		err = hang.finish(ctx, env.GetId(), err)
	}()
	if err := stream.Send(protocol.TypeHostSubscribed, env.GetId(), first); err != nil {
		return err
	}
	for {
		select {
		case <-sub.wake:
			events, again := h.pending(sub)
			if again != nil {
				if err := stream.Send(protocol.TypeHostSubscribed, env.GetId(), again); err != nil {
					return err
				}
			}
			for _, ev := range events {
				if err := stream.Send(protocol.TypeHostEvent, env.GetId(), ev); err != nil {
					return err
				}
			}
		case <-h.stopped:
			return errors.New("the session is no longer hosted: subscribe again")
		case err := <-hang.gone:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// handleSessionAction takes a front-end's action on a session's host.
func (d *Daemon) handleSessionAction(ctx context.Context, stream *transport.Stream, env *protocol.Envelope, fromPeer bool) error {
	return serveWithDirect(d, ctx, stream, env, protocol.TypeSessionActionResult, func(r *protocol.SessionAction) (*protocol.SessionActionResult, error) {
		s, ok := d.sessions.get(r.GetSessionId())
		if !ok {
			return nil, fmt.Errorf("unknown session %q", r.GetSessionId())
		}
		return d.host(ctx, s).act(ctx, r, fromPeer)
	})
}
