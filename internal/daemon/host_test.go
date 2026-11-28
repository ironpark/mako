//go:build unix

package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
)

// hostFollower is a front-end following a session's host: each state it
// held, copied.
type hostFollower struct {
	t      *testing.T
	states chan *protocol.HostState
	cancel context.CancelFunc
	done   chan *protocol.HostState
}

func followHost(t *testing.T, c *client.Client, req *protocol.SubscribeSession, held *protocol.HostState) *hostFollower {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	f := &hostFollower{t: t, states: make(chan *protocol.HostState, 1024), cancel: cancel, done: make(chan *protocol.HostState, 1)}
	go func() {
		last, _ := c.FollowSession(ctx, req, held, func(st *protocol.HostState, _ *protocol.HostEvent) {
			f.states <- proto.CloneOf(st)
		})
		f.done <- last
	}()
	t.Cleanup(cancel)
	return f
}

// until is the first state the follower holds that ok takes.
func (f *hostFollower) until(what string, ok func(*protocol.HostState) bool) *protocol.HostState {
	f.t.Helper()
	timeout := time.After(10 * time.Second)
	var last *protocol.HostState
	for {
		select {
		case st := <-f.states:
			if ok(st) {
				return st
			}
			last = st
		case <-timeout:
			f.t.Fatalf("no state with %s; the last was %v", what, last)
		}
	}
}

// stop ends the follow and returns the state it held last.
func (f *hostFollower) stop() *protocol.HostState {
	f.cancel()
	return <-f.done
}

func clientIDs(st *protocol.HostState) []string {
	var out []string
	for _, c := range st.GetClients().GetClients() {
		out = append(out, c.GetId())
	}
	return out
}

func lastText(st *protocol.HostState) string {
	msgs := st.GetChat().GetMessages()
	if len(msgs) == 0 {
		return ""
	}
	return msgs[len(msgs)-1].GetText()
}

// claudeLog is a Claude log of session s, records appended to it in turn.
// It is bound to s once it has its first: its identity is read from it
// once.
type claudeLog struct {
	t     *testing.T
	s     *session.Session
	path  string
	at    time.Time
	n     int
	bound bool
}

func newClaudeLog(t *testing.T, s *session.Session) *claudeLog {
	t.Helper()
	path := filepath.Join(t.TempDir(), "c.jsonl")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return &claudeLog{t: t, s: s, path: path, at: time.Now().UTC()}
}

func (l *claudeLog) add(role, text string) {
	l.t.Helper()
	l.at = l.at.Add(time.Second)
	l.n++
	rec := map[string]any{"type": role, "sessionId": "c", "cwd": l.s.Info().GetProject(), "timestamp": l.at.Format(time.RFC3339Nano), "uuid": fmt.Sprintf("u%d", l.n)}
	if role == "user" {
		rec["message"] = map[string]any{"role": "user", "content": text}
	} else {
		rec["message"] = map[string]any{"id": fmt.Sprintf("m%d", l.n), "role": "assistant", "content": []map[string]any{{"type": "text", "text": text}}}
	}
	data, err := json.Marshal(rec)
	if err != nil {
		l.t.Fatal(err)
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		l.t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		l.t.Fatal(err)
	}
	if !l.bound {
		l.bound = true
		l.s.SetConversation("c", l.path)
	}
}

func act(t *testing.T, c *client.Client, req *protocol.SessionAction) *protocol.SessionActionResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	res, err := c.SessionAction(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// Two front-ends of one session share its host: who is there, the draft,
// the conversation and the queue. An action sent twice is taken once, and
// one that leaves and comes back is replayed what it missed.
func TestSessionHostIsSharedByItsFrontEnds(t *testing.T) {
	d := meshDaemon(t, "host")
	addSessionOf(t, d, "agent", "claude")
	s, _ := d.sessions.get("agent")
	log := newClaudeLog(t, s)
	log.add("user", "Look at the build")
	log.add("assistant", "Reading the logs.")

	desk := followHost(t, dialDaemon(t, d), &protocol.SubscribeSession{SessionId: "agent", ClientId: "desk", ClientName: "desktop", Approvals: true}, nil)
	first := desk.until("the session", func(st *protocol.HostState) bool {
		return st.GetInfo().GetId() == "agent" && lastText(st) == "Reading the logs."
	})
	if first.GetSeq() == 0 {
		t.Fatal("the state has no seq")
	}
	phone := followHost(t, dialDaemon(t, d), &protocol.SubscribeSession{SessionId: "agent", ClientId: "phone"}, nil)
	both := func(st *protocol.HostState) bool { return len(clientIDs(st)) == 2 }
	desk.until("both front-ends", both)
	st := phone.until("both front-ends", both)
	if c := st.GetClients().GetClients()[0]; c.GetId() != "desk" || c.GetName() != "desktop" || !c.GetApprovals() {
		t.Fatalf("first client: %v", c)
	}

	c := dialDaemon(t, d)
	res := act(t, c, &protocol.SessionAction{SessionId: "agent", ClientId: "desk", ClientSeq: 1, Action: &protocol.SessionAction_SetDraft{SetDraft: &protocol.HostSetDraft{Text: "Fix the build"}}})
	if res.GetSeq() == 0 || res.GetDuplicate() {
		t.Fatalf("set_draft: %v", res)
	}
	phone.until("the desk's draft", func(st *protocol.HostState) bool {
		return st.GetDraft().GetText() == "Fix the build" && st.GetDraft().GetClientId() == "desk"
	})
	again := act(t, c, &protocol.SessionAction{SessionId: "agent", ClientId: "desk", ClientSeq: 1, Action: &protocol.SessionAction_SetDraft{SetDraft: &protocol.HostSetDraft{Text: "Something else"}}})
	if !again.GetDuplicate() || again.GetSeq() != res.GetSeq() {
		t.Fatalf("the action sent again: %v", again)
	}
	if h := d.host(t.Context(), s); h.stateLocked().GetDraft().GetText() != "Fix the build" {
		t.Fatal("an action sent again was taken again")
	}

	log.add("user", "Fix the build")
	log.add("assistant", "Fixed it.")
	for _, f := range []*hostFollower{desk, phone} {
		f.until("the new turn", func(st *protocol.HostState) bool {
			return lastText(st) == "Fixed it." && len(st.GetChat().GetMessages()) == 4
		})
	}

	q := act(t, c, &protocol.SessionAction{SessionId: "agent", ClientId: "phone", ClientSeq: 1, Action: &protocol.SessionAction_SendPrompt{SendPrompt: &protocol.HostSendPrompt{Text: "Then run the tests", Queue: true}}})
	if !q.GetQueued() {
		t.Fatalf("send_prompt with queue: %v", q)
	}
	st = desk.until("the queued prompt", func(st *protocol.HostState) bool { return len(st.GetQueue().GetItems()) == 1 })
	item := st.GetQueue().GetItems()[0]
	if item.GetText() != "Then run the tests" || item.GetOrigin().GetClientId() != "phone" {
		t.Fatalf("queued: %v", item)
	}
	act(t, c, &protocol.SessionAction{SessionId: "agent", ClientId: "desk", ClientSeq: 2, Action: &protocol.SessionAction_CancelQueued{CancelQueued: &protocol.HostCancelQueued{Id: item.GetId()}}})
	phone.until("the queue emptied", func(st *protocol.HostState) bool { return len(st.GetQueue().GetItems()) == 0 })

	// The desk goes: the phone carries on with the session as it was, and
	// its approvals still wait for a window a while.
	held := desk.stop()
	phone.until("the phone alone", func(st *protocol.HostState) bool { return len(clientIDs(st)) == 1 && clientIDs(st)[0] == "phone" })
	if !d.approvals.watchingSession("agent") {
		t.Fatal("the approvals of a session handed over went to the terminal")
	}

	// It comes back, and is replayed what came while it was away.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var replay *protocol.HostSubscribed
	_ = dialDaemon(t, d).SubscribeSession(ctx, &protocol.SubscribeSession{SessionId: "agent", ClientId: "desk", LastSeenSeq: held.GetSeq()}, func(first *protocol.HostSubscribed, _ *protocol.HostEvent) error {
		replay = first
		return context.Canceled
	})
	if replay == nil || replay.GetState() != nil || len(replay.GetReplay()) == 0 {
		t.Fatalf("coming back: %v", replay)
	}
	for _, ev := range replay.GetReplay() {
		if !protocol.ApplyHostEvent(held, ev) {
			t.Fatalf("a replayed event does not apply: %v", ev)
		}
	}
	if ids := clientIDs(held); len(ids) != 1 || ids[0] != "phone" {
		t.Fatalf("replayed clients: %v", ids)
	}
	// A seq of an earlier run is sent the state whole.
	_ = dialDaemon(t, d).SubscribeSession(ctx, &protocol.SubscribeSession{SessionId: "agent", ClientId: "desk", LastSeenSeq: 7}, func(first *protocol.HostSubscribed, _ *protocol.HostEvent) error {
		replay = first
		return context.Canceled
	})
	if replay.GetState() == nil || replay.GetState().GetDraft().GetText() != "Fix the build" {
		t.Fatalf("an earlier run's seq: %v", replay)
	}
}

// A prompt queued waits while the agent works, and goes once it is at its
// prompt, the front-ends told who sent it.
func TestSessionHostDeliversItsQueue(t *testing.T) {
	d := meshDaemon(t, "host-queue")
	addSessionOf(t, d, "agent", "claude")
	s, _ := d.sessions.get("agent")
	newClaudeLog(t, s)
	s.SetStatus(protocol.AgentWorking)

	f := followHost(t, dialDaemon(t, d), &protocol.SubscribeSession{SessionId: "agent", ClientId: "desk"}, nil)
	f.until("the session", func(st *protocol.HostState) bool { return st.GetInfo().GetStatus() == protocol.AgentWorking })
	c := dialDaemon(t, d)
	act(t, c, &protocol.SessionAction{SessionId: "agent", ClientId: "phone", ClientSeq: 1, Action: &protocol.SessionAction_SendPrompt{SendPrompt: &protocol.HostSendPrompt{Text: "Then run the tests", Queue: true}}})
	f.until("the queued prompt", func(st *protocol.HostState) bool { return len(st.GetQueue().GetItems()) == 1 })
	time.Sleep(1500 * time.Millisecond)
	if h := d.host(t.Context(), s); len(h.stateLocked().GetQueue().GetItems()) != 1 {
		t.Fatal("a prompt went to an agent at work")
	}

	h := d.host(t.Context(), s)
	h.mu.Lock()
	sub := &hostSub{client: "probe", cursor: h.state.GetSeq(), wake: make(chan struct{}, 1)}
	h.subs[sub] = struct{}{}
	h.mu.Unlock()
	s.SetStatus(protocol.AgentIdle)
	f.until("the queue emptied", func(st *protocol.HostState) bool { return len(st.GetQueue().GetItems()) == 0 })
	events, _ := h.pending(sub)
	var prompt *protocol.HostEvent
	for _, ev := range events {
		if ev.GetPrompt() != nil {
			prompt = ev
		}
	}
	if prompt == nil || prompt.GetPrompt().GetText() != "Then run the tests" || prompt.GetPrompt().GetQueuedId() == "" || prompt.GetOrigin().GetClientId() != "phone" {
		t.Fatalf("the prompt sent: %v", prompt)
	}
}

// A front-end on one node hands a session of another to a front-end
// there: the draft written on the first is the second's.
func TestSessionHostAcrossNodes(t *testing.T) {
	a := meshDaemon(t, "host-a")
	b := meshDaemon(t, "host-b", p2pAddr(a))
	letIn(t, b, a)
	letIn(t, a, b)
	addSessionOf(t, b, "agent", "claude")
	s, _ := b.sessions.get("agent")
	log := newClaudeLog(t, s)
	log.add("user", "Look at the build")
	waitFor(t, "the other node", func() bool { return lists(t.Context(), dialDaemon(t, a), "agent") })

	node := b.config().NodeID
	laptop := followHost(t, dialDaemon(t, a), &protocol.SubscribeSession{NodeId: node, SessionId: "agent", ClientId: "laptop"}, nil)
	laptop.until("the session", func(st *protocol.HostState) bool { return lastText(st) == "Look at the build" })
	act(t, dialDaemon(t, a), &protocol.SessionAction{NodeId: node, SessionId: "agent", ClientId: "laptop", ClientSeq: 1, Action: &protocol.SessionAction_SetDraft{SetDraft: &protocol.HostSetDraft{Text: "and the tests"}}})
	laptop.until("its draft", func(st *protocol.HostState) bool { return st.GetDraft().GetText() == "and the tests" })
	laptop.stop()

	desk := followHost(t, dialDaemon(t, b), &protocol.SubscribeSession{SessionId: "agent", ClientId: "desk"}, nil)
	desk.until("the laptop's draft", func(st *protocol.HostState) bool {
		return st.GetDraft().GetText() == "and the tests" && st.GetDraft().GetClientId() == "laptop" && lastText(st) == "Look at the build"
	})
}

// A replay leaves out events a later one makes moot, and a front-end the
// ring left behind is sent the state whole.
func TestSessionHostReplay(t *testing.T) {
	d := New(&config.Config{NodeID: "n"}, nil)
	h := &sessionHost{d: d, state: &protocol.HostState{}, subs: map[*hostSub]struct{}{}, clients: map[string]*hostClient{}, done: map[string]*hostDone{}, stopped: make(chan struct{})}
	h.mu.Lock()
	start := h.state.GetSeq()
	for i := range 3 {
		h.state.Info = &protocol.SessionInfo{Name: fmt.Sprint(i)}
		h.emitLocked(nil, &protocol.HostEvent{Body: &protocol.HostEvent_Info{Info: h.state.Info}})
		h.emitLocked(nil, &protocol.HostEvent{Body: &protocol.HostEvent_Chat{Chat: &protocol.HostChatPatch{Messages: []*protocol.ChatMessage{{Text: fmt.Sprint(i)}}}}})
	}
	events := h.afterLocked(start)
	h.mu.Unlock()
	var infos, chats int
	for _, ev := range events {
		switch {
		case ev.GetInfo() != nil:
			infos++
			if ev.GetInfo().GetName() != "2" {
				t.Fatalf("the info kept is %q", ev.GetInfo().GetName())
			}
		case ev.GetChat() != nil:
			chats++
		}
	}
	if infos != 1 || chats != 3 {
		t.Fatalf("replayed %d infos, %d patches", infos, chats)
	}

	h.mu.Lock()
	for range hostRing {
		h.emitLocked(nil, &protocol.HostEvent{Body: &protocol.HostEvent_Live{Live: &protocol.ChatLive{}}})
	}
	h.mu.Unlock()
	_, first, err := h.subscribe(&protocol.SubscribeSession{ClientId: "c", LastSeenSeq: start + 1})
	if err != nil || first.GetState() == nil {
		t.Fatalf("behind the ring: %v %v", first, err)
	}
}

// A patch, applied to the conversation it was made from, makes the new.
func TestChatPatchApplies(t *testing.T) {
	m := func(texts ...string) []*protocol.ChatMessage {
		var out []*protocol.ChatMessage
		for _, s := range texts {
			out = append(out, &protocol.ChatMessage{Role: "assistant", Text: s})
		}
		return out
	}
	cases := []struct {
		name     string
		old, cur *protocol.Chat
		whole    bool
		sent     int
	}{
		{"first", nil, &protocol.Chat{Conversation: "c", Messages: m("a")}, true, 1},
		{"grown", &protocol.Chat{Conversation: "c", Messages: m("a", "b")}, &protocol.Chat{Conversation: "c", Messages: m("a", "b", "c")}, false, 1},
		{"last rewritten", &protocol.Chat{Conversation: "c", Messages: m("a", "b")}, &protocol.Chat{Conversation: "c", Messages: m("a", "b2")}, false, 1},
		{"window moved", &protocol.Chat{Conversation: "c", Messages: m("a", "b", "c")}, &protocol.Chat{Conversation: "c", Messages: m("b", "c", "d")}, false, 1},
		{"usage alone", &protocol.Chat{Conversation: "c", Messages: m("a")}, &protocol.Chat{Conversation: "c", Messages: m("a"), Usage: &protocol.Usage{Input: 3}}, false, 0},
		{"another conversation", &protocol.Chat{Conversation: "c", Messages: m("a")}, &protocol.Chat{Conversation: "d", Messages: m("a")}, true, 1},
		{"emptied", &protocol.Chat{Conversation: "c", Messages: m("a")}, &protocol.Chat{Conversation: "c"}, false, 0},
	}
	for _, c := range cases {
		p := chatPatch(c.old, c.cur)
		if p == nil {
			t.Fatalf("%s: no patch", c.name)
		}
		if p.GetWhole() != c.whole || len(p.GetMessages()) != c.sent {
			t.Fatalf("%s: whole %v, %d sent", c.name, p.GetWhole(), len(p.GetMessages()))
		}
		got := p.Apply(c.old)
		if got == nil || !proto.Equal(&protocol.Chat{Conversation: got.GetConversation(), Messages: got.GetMessages(), Usage: got.GetUsage()}, c.cur) {
			t.Fatalf("%s: applied %v, want %v", c.name, got, c.cur)
		}
	}
	same := &protocol.Chat{Conversation: "c", Messages: m("a")}
	if chatPatch(same, &protocol.Chat{Conversation: "c", Messages: m("a")}) != nil {
		t.Fatal("a patch for no change")
	}
	if (&protocol.HostChatPatch{Drop: 1, Keep: 1}).Apply(same) != nil {
		t.Fatal("a patch past the conversation held applied")
	}
}

// A session's approvals wait for a window while a subscriber shows them,
// and for the grace after the last left.
func TestApprovalsHeldForASession(t *testing.T) {
	var a approvals
	if a.watchingSession("s") {
		t.Fatal("watching with no one")
	}
	release := a.holdSession("s", 50*time.Millisecond)
	if !a.watchingSession("s") || a.watchingSession("other") {
		t.Fatal("the hold is not the session's")
	}
	release()
	release() // once is enough
	if !a.watchingSession("s") {
		t.Fatal("no grace")
	}
	time.Sleep(80 * time.Millisecond)
	if a.watchingSession("s") {
		t.Fatal("the grace outlasted itself")
	}
}
