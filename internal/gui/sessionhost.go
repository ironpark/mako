package gui

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ironpark/ggui"
	uitheme "github.com/ironpark/ggui/ui/theme"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// The session host (protocol.SubscribeSession): while a chat shows, the
// window follows its session's host on the session's node, which keeps
// the draft for all its front-ends, and says which others have it open.
// What is written in the composer goes there a moment after the typing
// stops, and what another front-end writes shows here unless this one is
// being written in. A prompt sent goes as an action of the host's, which
// the node takes once even when it is sent again after a lost reply.

// hostDraftDelay is how long the composer stands still before its draft
// goes to the host, and hostDraftTyping how long after the last key here
// the composer keeps what is written in it over another front-end's.
const (
	hostDraftDelay  = 400 * time.Millisecond
	hostDraftTyping = 3 * time.Second
)

// hostClient is this window as session hosts know it: an ID of its own,
// and the seq of its actions, which grows from the time it started so a
// window started again does not reuse one.
var hostClient = struct {
	id, name string
	seq      atomic.Uint64
}{id: "desktop-" + randomID(), name: desktopName()}

func init() { hostClient.seq.Store(uint64(time.Now().UnixNano())) }

func randomID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func desktopName() string {
	if host, err := os.Hostname(); err == nil && host != "" {
		return "desktop (" + host + ")"
	}
	return "desktop"
}

// hostShare is what a chat keeps of its session's host, on the UI
// goroutine. The chat's views share it: the first to show follows the
// host, and the last to go stops.
type hostShare struct {
	views int
	stop  func()
	// others are the session's other front-ends, as its host says, and
	// note what went wrong with the last draft or message sent to it.
	others *ggui.StateValue[[]*protocol.HostClient]
	note   *ggui.StateValue[string]
	// host is the draft as the host's state said last, at seq.
	host *protocol.HostDraft
	seq  uint64
	// hostText is what the host has as its draft, or will once the drafts
	// this window sent are taken; base is what the composer and the host
	// last agreed on, known once they have.
	hostText string
	base     string
	known    bool
	// flying says a draft this window sent may not be the host's yet: its
	// action, wantSeq, unanswered, or answered with the event ackSeq not
	// yet seen. Another front-end's draft meanwhile is older than it.
	flying          bool
	wantSeq, ackSeq uint64
	// edited is when the composer was last written in here, and applied
	// the host's draft it was given, which its watch then sees.
	edited   time.Time
	applied  string
	applying bool
	// send is the draft due to go to the host, sendText, stopped by the
	// next key.
	send     *time.Timer
	sendText string
}

// hostable reports whether session id's host can be followed: its node,
// and this window's own, which passes the stream on, know it.
func (m *model) hostable(id string) bool {
	self := ggui.Peek(m.Conn).Self
	return self.GetRevision() >= protocol.RevisionSessionHost && m.nodeHas(id, protocol.RevisionSessionHost)
}

// hostRequest is the subscription to session id's host, nil while it
// cannot be followed: the session not listed yet, as before the daemon
// answers, or its node too old.
func (m *model) hostRequest(id string) *protocol.SubscribeSession {
	info, ok := m.session(id)
	if !ok || !m.hostable(id) {
		return nil
	}
	return &protocol.SubscribeSession{
		NodeId: m.remoteID(info.GetNodeId()), SessionId: id,
		ClientId: hostClient.id, ClientName: hostClient.name,
		Approvals: m.link.approveInApp.Load(),
	}
}

// followHost follows session id's host into state until the function it
// returns is called, on the UI goroutine: views of one chat share the
// follow, which ends, its draft due sent, once the last has let it go.
func (m *model) followHost(id string, state *chatState) func() {
	h := &state.host
	h.views++
	if h.views == 1 {
		h.stop = m.startHost(id, state)
	}
	let := false
	return func() {
		if let {
			return
		}
		let = true
		if h.views--; h.views > 0 {
			return
		}
		m.flushDraft(id, state)
		if h.stop != nil {
			h.stop()
			h.stop = nil
		}
		h.others.Set(nil)
	}
}

// startHost follows session id's host until the function it returns is
// called: as soon as the session can be followed, as a tab restored at
// start is once the daemon lists it, and again each time the stream
// breaks, or whether this window shows approvals changes.
func (m *model) startHost(id string, state *chatState) func() {
	// The watch runs at once too, with what the composer held before: not
	// a word written, which would empty the host's draft.
	primed := false
	stopWatch := ggui.Watch(state.draft, func(text string) {
		if primed {
			m.draftWritten(id, state, text)
		}
		primed = true
	})
	post := m.post
	if post == nil {
		return stopWatch
	}
	ctx, cancel := context.WithCancel(context.Background())
	onUI := func(fn func()) {
		post(func() {
			if ctx.Err() == nil {
				fn()
			}
		})
	}
	go func() {
		var held *protocol.HostState
		for ctx.Err() == nil {
			req, _ := uiValue(ctx, post, func() *protocol.SubscribeSession { return m.hostRequest(id) })
			if req != nil {
				held = m.followHostOnce(ctx, req, held, func(seen hostSeen) {
					onUI(func() { m.tookHost(id, state, seen) })
				})
				if m.link.approveInApp.Load() != req.GetApprovals() {
					continue // subscribed again at once, saying so
				}
			}
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second): // the stream broke, or the session cannot be followed yet: again
			}
		}
	}()
	return func() {
		cancel()
		stopWatch()
	}
}

// followHostOnce follows the host req names, from held, until the stream
// breaks, ctx ends, or whether this window shows approvals is no longer
// as req says, and returns the state it held last.
func (m *model) followHostOnce(ctx context.Context, req *protocol.SubscribeSession, held *protocol.HostState, took func(hostSeen)) *protocol.HostState {
	c, err := m.link.client(ctx)
	if err != nil {
		return held
	}
	sub, end := context.WithCancel(ctx)
	defer end()
	go func() {
		tick := time.NewTicker(2 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-sub.Done():
				return
			case <-tick.C:
			}
			if m.link.approveInApp.Load() != req.GetApprovals() {
				end()
				return
			}
		}
	}()
	held, _ = c.FollowSession(sub, req, held, func(st *protocol.HostState, ev *protocol.HostEvent) {
		// The follower changes its state in place, by fields replaced
		// whole: what the UI takes is read here.
		took(hostSeenOf(st, ev))
	})
	return held
}

// uiValue is what fn makes on the UI goroutine, waited for as long as ctx
// lets it.
func uiValue[T any](ctx context.Context, post func(func()), fn func() T) (T, bool) {
	ch := make(chan T, 1)
	post(func() { ch <- fn() })
	select {
	case v := <-ch:
		return v, true
	case <-ctx.Done():
		var zero T
		return zero, false
	}
}

// hostSeen is what the window takes of a host's state as it changes.
type hostSeen struct {
	draft   *protocol.HostDraft
	clients []*protocol.HostClient
	seq     uint64
	whole   bool                 // the state came whole, as a follow starts
	origin  *protocol.HostOrigin // of the event that made it
	drafted bool                 // the event was of the draft
}

func hostSeenOf(st *protocol.HostState, ev *protocol.HostEvent) hostSeen {
	return hostSeen{
		draft: st.GetDraft(), clients: st.GetClients().GetClients(), seq: st.GetSeq(),
		whole: ev == nil, origin: ev.GetOrigin(), drafted: ev.GetDraft() != nil,
	}
}

// tookHost shows what the host says of the session: who else has it open,
// and the draft another front-end wrote, unless the composer is being
// written in here, holds files the draft does not, or a draft sent from
// here is still on its way, which the host takes after it.
func (m *model) tookHost(id string, state *chatState, seen hostSeen) {
	h := &state.host
	h.others.Set(slices.DeleteFunc(slices.Clone(seen.clients), func(c *protocol.HostClient) bool { return c.GetId() == hostClient.id }))
	h.host, h.seq = seen.draft, seen.seq
	if h.flying && (h.ackSeq != 0 && seen.seq >= h.ackSeq ||
		seen.origin.GetClientId() == hostClient.id && seen.origin.GetClientSeq() == h.wantSeq) {
		h.flying = false
	}
	if h.flying {
		return
	}
	if seen.whole {
		m.mergeHost(id, state)
		return
	}
	if !seen.drafted || seen.draft.GetClientId() == hostClient.id {
		return // this window's own draft, as the host says it back
	}
	m.tookDraft(id, state, seen.draft)
}

// tookDraft takes dr, another front-end's draft and the host's latest,
// into the composer, unless it is being written in.
func (m *model) tookDraft(id string, state *chatState, dr *protocol.HostDraft) {
	h := &state.host
	text := dr.GetText()
	h.hostText = text
	if m.composerDraft(id) == text {
		h.base, h.known = text, true
		return
	}
	if m.writingHere(state) {
		return // what is written here goes to the host once the typing stops
	}
	m.takeDraft(id, state, text)
}

// mergeHost settles the composer and the host's draft as a follow starts,
// or starts over: the one written since they last agreed wins; when both
// were, the later; and when that cannot be told, each keeps its own.
func (m *model) mergeHost(id string, state *chatState) {
	h := &state.host
	theirs, local := h.host.GetText(), m.composerDraft(id)
	h.hostText = theirs
	if local == theirs {
		h.base, h.known = local, true
		return
	}
	if time.Since(h.edited) < hostDraftTyping {
		m.sendDraftSoon(id, state, local)
		return
	}
	mineChanged, theirsChanged := local != "", theirs != ""
	if h.known {
		mineChanged, theirsChanged = local != h.base, theirs != h.base
	}
	switch {
	case mineChanged && !theirsChanged:
		m.commitDraft(id, state, local)
	case theirsChanged && !mineChanged:
		if len(ggui.Peek(state.attached)) == 0 {
			m.takeDraft(id, state, theirs)
		}
	case h.edited.IsZero():
		// Written here before the host was followed, and there since:
		// which came later is not known.
	case h.edited.After(h.host.GetAt().AsTime()):
		m.commitDraft(id, state, local)
	case len(ggui.Peek(state.attached)) == 0:
		m.takeDraft(id, state, theirs)
	}
}

// writingHere reports whether the composer is being written in, or holds
// files that another front-end's text would not go with.
func (m *model) writingHere(state *chatState) bool {
	return time.Since(state.host.edited) < hostDraftTyping || len(ggui.Peek(state.attached)) > 0
}

// takeDraft puts the host's draft, text, in the composer.
func (m *model) takeDraft(id string, state *chatState, text string) {
	h := &state.host
	if h.send != nil {
		h.send.Stop()
		h.send = nil
	}
	h.base, h.known = text, true
	h.applied, h.applying = text, true
	m.setComposerDraft(id, text)
}

// draftWritten sends what the composer holds to the host once the typing
// stops. A draft as the host has it, as one taken from it, is not this
// window's writing.
func (m *model) draftWritten(id string, state *chatState, text string) {
	h := &state.host
	if h.applying {
		h.applying = false
		if text == h.applied {
			return
		}
	}
	h.edited = time.Now()
	m.sendDraftSoon(id, state, text)
}

// sendDraftSoon sends text to the host once the composer stands still,
// unless the host has it.
func (m *model) sendDraftSoon(id string, state *chatState, text string) {
	h := &state.host
	if h.send != nil {
		h.send.Stop()
		h.send = nil
	}
	if text == h.hostText {
		return
	}
	post := m.post
	if post == nil {
		return
	}
	var send *time.Timer
	send = time.AfterFunc(hostDraftDelay, func() {
		post(func() {
			if h.send == send {
				m.commitDraft(id, state, text)
			}
		})
	})
	h.send, h.sendText = send, text
}

// flushDraft sends the draft due now, as the chat stops following the
// host.
func (m *model) flushDraft(id string, state *chatState) {
	h := &state.host
	if h.send == nil {
		return
	}
	h.send.Stop()
	h.send = nil
	m.commitDraft(id, state, h.sendText)
}

// commitDraft sends text to the host as the session's draft, after what
// was sent to the session before it.
func (m *model) commitDraft(id string, state *chatState, text string) {
	h := &state.host
	h.send = nil
	info, ok := m.session(id)
	if text == h.hostText || !ok || !m.hostable(id) {
		return
	}
	seq := hostClient.seq.Add(1)
	h.hostText, h.base, h.known = text, text, true
	h.flying, h.wantSeq, h.ackSeq = true, seq, 0
	req := &protocol.SessionAction{
		NodeId: info.GetNodeId(), SessionId: id, ClientId: hostClient.id, ClientSeq: seq,
		Action: &protocol.SessionAction_SetDraft{SetDraft: &protocol.HostSetDraft{Text: text}},
	}
	post, dial := m.post, m.hostDialer()
	// One action at a time, in the order made: a draft sent before a
	// message goes before it, so the message's clear_draft clears it.
	prev, done := submitAfter(id)
	go func() {
		defer submitDone(id, done)
		<-prev
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		res, err := sendHostAction(ctx, dial, req)
		if post != nil {
			post(func() { m.draftSent(id, state, seq, res, err) })
		}
	}()
}

// draftSent takes the host's answer to the draft sent as seq.
func (m *model) draftSent(id string, state *chatState, seq uint64, res *protocol.SessionActionResult, err error) {
	h := &state.host
	if err != nil {
		h.note.Set(Tf("The draft was not shared: %s", err.Error()))
	} else {
		h.note.Set("")
	}
	if seq != h.wantSeq || !h.flying {
		return
	}
	switch {
	case err != nil:
		// The host has its draft as it said last, and what the composer
		// holds was not taken: written since they agreed.
		h.flying = false
		h.hostText, h.base = h.host.GetText(), h.host.GetText()
	case res.GetSeq() == 0:
		h.flying = false // the host had it already
	case h.seq >= res.GetSeq():
		// The host's state has said it since: the draft it holds now is
		// this window's, or another's written after it.
		h.flying = false
		if h.host.GetClientId() != hostClient.id {
			m.tookDraft(id, state, h.host)
		}
	default:
		h.ackSeq = res.GetSeq()
	}
}

// sendingPrompt readies the host's share of session id for the composer's
// message about to be sent, which empties the host's draft too, if this
// window wrote it last: the draft due goes no more, and the composer
// emptied after it is no writing.
func (m *model) sendingPrompt(state *chatState) {
	h := &state.host
	if h.send != nil {
		h.send.Stop()
		h.send = nil
	}
	if h.flying || h.host.GetClientId() == hostClient.id {
		h.hostText, h.base, h.known = "", "", true
	}
}

// composerCleared marks the composer emptied as a message went: no
// writing of this window's.
func (m *model) composerCleared(state *chatState) {
	h := &state.host
	if h.views > 0 {
		h.applied, h.applying = "", true
	}
}

// hostPresence says which other front-ends have the session open, over
// the composer: what is written here shows there too; and what went wrong
// with the last draft or message sent to the host.
func hostPresence(state *chatState) ggui.Widget {
	return ggui.View(state.host.note, func(note string) ggui.Widget {
		presence := ggui.View(state.host.others, func(others []*protocol.HostClient) ggui.Widget {
			if len(others) == 0 {
				return widget.Box()
			}
			names := make([]string, 0, len(others))
			for _, c := range others {
				names = append(names, cmp.Or(c.GetName(), c.GetId()))
			}
			label := Tf("Also open on %s", strings.Join(names, ", "))
			return widget.Row(
				icon("smartphone").Size(13).Color(skin.Muted),
				widget.Flexible(widget.Text(label).Size(12).Color(skin.Muted).NoWrap().Ellipsis()),
			).Gap(6).Align(widget.AlignCenter)
		})
		if note == "" {
			return presence
		}
		return widget.Column(
			presence,
			widget.Text(note).Size(12).Color(uitheme.Destructive),
		).Gap(4)
	})
}

// hostActor is what takes a session host's actions: a front-end's client.
type hostActor interface {
	SessionAction(context.Context, *protocol.SessionAction) (*protocol.SessionActionResult, error)
}

// hostDialer is what gives the client a session host's actions go
// through: the daemon link's, as it is at each try.
func (m *model) hostDialer() func(context.Context) (hostActor, error) {
	if m.hostDial != nil {
		return m.hostDial
	}
	link := m.link
	return func(ctx context.Context) (hostActor, error) {
		c, err := link.client(ctx)
		if err != nil {
			return nil, err
		}
		return c, nil
	}
}

// sendHostAction sends req, which names this window's client_seq for it,
// until the host answers or ctx ends: a try that fails on the way, which
// the host may have taken, is made again through the client as the link
// has it then, the same action, which the host takes once. What the host
// refused (protocol.CodeNotTaken), or a daemon does not know, is not
// tried again.
func sendHostAction(ctx context.Context, dial func(context.Context) (hostActor, error), req *protocol.SessionAction) (*protocol.SessionActionResult, error) {
	wait := 250 * time.Millisecond
	for {
		c, err := dial(ctx)
		if err == nil {
			var res *protocol.SessionActionResult
			if res, err = c.SessionAction(ctx, req); err == nil {
				return res, nil
			}
			if protocol.HasCode(err, protocol.CodeNotTaken) || protocol.HasCode(err, protocol.CodeUnsupported) {
				return nil, err
			}
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(wait):
		}
		wait = min(2*wait, 4*time.Second)
	}
}
