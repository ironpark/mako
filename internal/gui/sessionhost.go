package gui

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ironpark/ggui"
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
// goroutine.
type hostShare struct {
	// others are the session's other front-ends, as its host says.
	others *ggui.StateValue[[]*protocol.HostClient]
	// draft is the draft as the host had it last, and edited when the
	// composer was last written in here.
	draft  string
	edited time.Time
	// send is the draft due to go to the host, stopped by the next key.
	send *time.Timer
}

// hostable reports whether session id's host can be followed: its node,
// and this window's own, which passes the stream on, know it.
func (m *model) hostable(id string) bool {
	self := ggui.Peek(m.Conn).Self
	return self.GetRevision() >= protocol.RevisionSessionHost && m.nodeHas(id, protocol.RevisionSessionHost)
}

// followHost follows session id's host into state until the function it
// returns is called, on the UI goroutine.
func (m *model) followHost(id string, state *chatState) func() {
	info, ok := m.session(id)
	post := m.post
	if !ok || post == nil || !m.hostable(id) {
		return func() {}
	}
	req := &protocol.SubscribeSession{
		NodeId: m.remoteID(info.GetNodeId()), SessionId: id,
		ClientId: hostClient.id, ClientName: hostClient.name,
		Approvals: m.link.approveInApp.Load(),
	}
	ctx, cancel := context.WithCancel(context.Background())
	onUI := func(fn func()) {
		post(func() {
			if ctx.Err() == nil {
				fn()
			}
		})
	}
	// The watch runs at once too, with what the composer held before: not
	// a word written, which would empty the host's draft.
	primed := false
	stopWatch := ggui.Watch(state.draft, func(text string) {
		if primed {
			m.draftWritten(id, state, text)
		}
		primed = true
	})
	go func() {
		var held *protocol.HostState
		for ctx.Err() == nil {
			if c, err := m.link.client(ctx); err == nil {
				first := true
				held, _ = c.FollowSession(ctx, req, held, func(st *protocol.HostState, ev *protocol.HostEvent) {
					// The follower changes its state in place, by fields
					// replaced whole: what the UI takes is read here.
					draft, clients, whole := st.GetDraft(), st.GetClients().GetClients(), ev == nil && first
					first = false
					onUI(func() { m.tookHost(id, state, draft, clients, whole) })
				})
			}
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second): // the stream broke: again
			}
		}
	}()
	return func() {
		cancel()
		stopWatch()
		if h := &state.host; h.send != nil {
			h.send.Stop()
			h.send = nil
		}
		state.host.others.Set(nil)
	}
}

// tookHost shows what the host says of the session: who else has it open,
// and the draft another front-end wrote, unless the composer is being
// written in here. On first, a draft written here before the host was
// followed goes to it rather than being replaced.
func (m *model) tookHost(id string, state *chatState, draft *protocol.HostDraft, clients []*protocol.HostClient, first bool) {
	h := &state.host
	h.others.Set(slices.DeleteFunc(slices.Clone(clients), func(c *protocol.HostClient) bool { return c.GetId() == hostClient.id }))
	text := draft.GetText()
	if text == h.draft && !first {
		return
	}
	h.draft = text
	local := m.composerDraft(id)
	if local == text {
		return
	}
	if first && local != "" {
		m.draftWritten(id, state, local)
		return
	}
	if draft.GetClientId() == hostClient.id || local != "" && time.Since(h.edited) < hostDraftTyping {
		return
	}
	m.setComposerDraft(id, text)
}

// draftWritten sends what the composer holds to the host once the typing
// stops. A draft as the host has it, as one taken from it, is not this
// window's writing.
func (m *model) draftWritten(id string, state *chatState, text string) {
	h := &state.host
	if h.send != nil {
		h.send.Stop()
		h.send = nil
	}
	if text == h.draft {
		return
	}
	h.edited = time.Now()
	info, ok := m.session(id)
	if !ok || !m.hostable(id) {
		return
	}
	node := info.GetNodeId()
	post := m.post
	var send *time.Timer
	send = time.AfterFunc(hostDraftDelay, func() {
		if post != nil {
			post(func() {
				if h.send == send {
					h.send = nil
				}
			})
		}
		// One draft at a time, in the order written.
		prev, done := submitAfter("draft:" + id)
		defer submitDone("draft:"+id, done)
		<-prev
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		c, err := m.link.client(ctx)
		if err != nil {
			return
		}
		_, _ = sendHostAction(ctx, c, &protocol.SessionAction{
			NodeId: node, SessionId: id,
			Action: &protocol.SessionAction_SetDraft{SetDraft: &protocol.HostSetDraft{Text: text}},
		})
	})
	h.send = send
}

// hostPresence says which other front-ends have the session open, over
// the composer: what is written here shows there too.
func hostPresence(state *chatState) ggui.Widget {
	return ggui.View(state.host.others, func(others []*protocol.HostClient) ggui.Widget {
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
			widget.Text(label).Size(12).Color(skin.Muted).NoWrap().Ellipsis(),
		).Gap(6).Align(widget.AlignCenter)
	})
}

// hostActor is what takes a session host's actions: a front-end's client.
type hostActor interface {
	SessionAction(context.Context, *protocol.SessionAction) (*protocol.SessionActionResult, error)
}

// sendHostAction sends req as this window's next action, and once more
// should no answer come: the host takes it once.
func sendHostAction(ctx context.Context, c hostActor, req *protocol.SessionAction) (*protocol.SessionActionResult, error) {
	req.ClientId, req.ClientSeq = hostClient.id, hostClient.seq.Add(1)
	res, err := c.SessionAction(ctx, req)
	var coded *protocol.CodedError
	if err != nil && !errors.As(err, &coded) && ctx.Err() == nil {
		res, err = c.SessionAction(ctx, req)
	}
	return res, err
}
