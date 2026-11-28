package ahp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gosuda/go-ahp/host"
	"github.com/microsoft/agent-host-protocol/clients/go/ahptypes"

	"github.com/ironpark/mako/internal/protocol"
)

// A session's conversation, as its host has it, is its latest messages:
// a user's message, then what the agent did of it, its text and its tool
// calls. Each user message starts a turn of the AHP chat, and what
// follows it are the turn's parts. A turn ends when the next starts, or
// when the agent is no longer at work. Parts only grow: text that grows is
// sent as a delta, a call that ends as its completion. A pending approval
// is a tool call that waits for a client's confirmation.

// approvalPrefix marks the tool calls that are approvals: the rest of the
// ID is the approval's.
const approvalPrefix = "approval:"

// resultMax bounds the result of a tool call sent.
const resultMax = 8 << 10

// clientTurnWait is how long a turn a client started waits, the agent idle,
// for its message to show in the log before it is ended empty.
const clientTurnWait = 20 * time.Second

type mirror struct {
	b              *Bridge
	id, node, uri  string
	chat           string
	cancel         context.CancelFunc
	mu             sync.Mutex
	turns          map[string]*mTurn // by the user message's key
	order          []string          // the keys, oldest first
	active         *mTurn
	pending        *host.Turn // a client's turn whose message is not yet in the log
	pendingAt      time.Time
	draft          string // the draft as last synced, either way
	approval       string // the approval shown as a tool call
	approvalTurn   string
	approvalClosed bool
	title          string
	n              int
	// approvals is whether the session's host is followed as showing its
	// approvals: while an AHP client is active on the session, so they
	// wait for it rather than going to the agent's terminal. restart
	// ends the stream for it to be followed again so.
	approvals bool
	restart   context.CancelFunc
	restarted bool
}

type mTurn struct {
	id    string
	parts []mPart
}

type mPart struct {
	id     string
	tool   bool
	text   string
	closed bool
}

func newMirror(b *Bridge, s *protocol.SessionInfo, chat string) *mirror {
	return &mirror{b: b, id: s.GetId(), node: s.GetNodeId(), uri: SessionURI(s.GetId()), chat: chat, turns: map[string]*mTurn{}, title: title(s)}
}

func (m *mirror) stop() {
	m.mu.Lock()
	cancel := m.cancel
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// run follows the session's host until stopped, coming back after a
// broken stream with what it missed.
func (m *mirror) run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	m.cancel = cancel
	m.mu.Unlock()
	defer cancel()
	var held *protocol.HostState
	for ctx.Err() == nil {
		sctx, scancel := context.WithCancel(ctx)
		m.mu.Lock()
		req := &protocol.SubscribeSession{NodeId: m.node, SessionId: m.id, ClientId: ClientID, ClientName: ClientName, Approvals: m.approvals}
		m.restart, m.restarted = scancel, false
		m.mu.Unlock()
		c, err := m.b.client(sctx)
		if err == nil {
			held, err = c.FollowSession(sctx, req, held, func(st *protocol.HostState, ev *protocol.HostEvent) {
				if ev == nil || wholeChanges(ev) {
					m.apply(st)
				}
			})
		}
		scancel()
		if ctx.Err() != nil {
			return
		}
		m.mu.Lock()
		again := m.restarted
		m.mu.Unlock()
		if again {
			continue // followed again at once, showing approvals or not
		}
		m.b.log.Debug("ahp: session host lost", "session", m.id, "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

// showApprovals follows the session's host anew, showing its approvals or
// not, if that changed.
func (m *mirror) showApprovals(on bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.approvals == on {
		return
	}
	m.approvals = on
	if m.restart != nil {
		m.restarted = true
		m.restart()
	}
}

// wholeChanges reports whether ev changes what the mirror shows.
func wholeChanges(ev *protocol.HostEvent) bool {
	switch ev.GetBody().(type) {
	case *protocol.HostEvent_Info, *protocol.HostEvent_Chat, *protocol.HostEvent_Draft:
		return true
	}
	return false
}

// dispatch sends a server action on the session's chat. An action the
// state already reflects is no error.
func (m *mirror) dispatch(a ahptypes.StateAction) error {
	_, err := m.b.host.Dispatch(m.chat, a)
	if errors.Is(err, host.ErrNoEffect) {
		return nil
	}
	return err
}

func (m *mirror) dispatchSession(a ahptypes.StateAction) error {
	_, err := m.b.host.Dispatch(m.uri, a)
	if errors.Is(err, host.ErrNoEffect) {
		return nil
	}
	return err
}

func now() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00") }

func userMessage(text string) ahptypes.Message {
	return ahptypes.Message{Text: text, Origin: ahptypes.MessageOrigin{Kind: ahptypes.MessageKindUser}}
}

// group is a user message and what followed it.
type group struct {
	key   string
	user  string
	parts []*protocol.ChatMessage
}

// groups are the conversation's turns, each keyed by its user message: its
// ID, or its text and how many before it had the same text. What comes
// before the first user message began before the window, and is left out.
func groups(msgs []*protocol.ChatMessage) []group {
	var out []group
	seen := map[string]int{}
	for _, msg := range msgs {
		if msg.GetRole() == "user" {
			key := msg.GetId()
			if key == "" {
				seen[msg.GetText()]++
				key = fmt.Sprintf("%d:%s", seen[msg.GetText()], msg.GetText())
			}
			out = append(out, group{key: key, user: msg.GetText()})
			continue
		}
		if len(out) > 0 {
			out[len(out)-1].parts = append(out[len(out)-1].parts, msg)
		}
	}
	return out
}

// atWork reports whether the session's agent is at a turn.
func atWork(info *protocol.SessionInfo) bool {
	switch info.GetStatus() {
	case protocol.AgentWorking, protocol.AgentWaiting:
		return true
	}
	return info.GetApproval() != nil
}

// apply brings the AHP session to st.
func (m *mirror) apply(st *protocol.HostState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	info := st.GetInfo()
	if t := title(info); info != nil && t != m.title {
		m.title = t
		_ = m.dispatchSession(ahptypes.StateAction{Value: &ahptypes.SessionTitleChangedAction{Title: t}})
	}
	if text := st.GetDraft().GetText(); text != m.draft {
		m.draft = text
		var draft *ahptypes.Message
		if text != "" {
			msg := userMessage(text)
			draft = &msg
		}
		_ = m.dispatch(ahptypes.StateAction{Value: &ahptypes.ChatDraftChangedAction{Draft: draft}})
	}
	gs := groups(st.GetChat().GetMessages())
	last := -1
	for i, g := range gs {
		if m.turns[g.key] != nil {
			last = i
		}
	}
	for i, g := range gs {
		t := m.turns[g.key]
		if t == nil {
			if i < last {
				continue // older than what is mirrored: before the window was
			}
			t = m.startTurn(g)
		}
		if t == m.active {
			m.syncParts(t, g.parts)
		}
	}
	working := atWork(info)
	m.syncApproval(info.GetApproval())
	if m.active != nil && !working {
		m.endTurn(m.active)
	}
	if m.pending != nil && m.active == nil && !working && time.Since(m.pendingAt) > clientTurnWait {
		_ = m.dispatch(ahptypes.StateAction{Value: &ahptypes.ChatTurnCompleteAction{TurnId: m.pending.ID}})
		m.pending = nil
	}
}

// startTurn starts the turn of g: a client's turn waiting for its message,
// or one of the agent's own, the active turn ended first.
func (m *mirror) startTurn(g group) *mTurn {
	if m.active != nil {
		m.endTurn(m.active)
	}
	var t *mTurn
	if p := m.pending; p != nil {
		t = &mTurn{id: p.ID}
		m.pending = nil
	} else {
		m.n++
		t = &mTurn{id: fmt.Sprintf("%s.%d.%d", m.id, time.Now().UnixMilli(), m.n)}
		if err := m.dispatch(ahptypes.StateAction{Value: &ahptypes.ChatTurnStartedAction{TurnId: t.id, StartedAt: now(), Message: userMessage(g.user)}}); err != nil {
			m.b.log.Debug("ahp: turn not started", "session", m.id, "err", err)
		}
	}
	m.turns[g.key] = t
	m.order = append(m.order, g.key)
	// The keys of turns long gone are let go.
	for len(m.order) > 512 {
		delete(m.turns, m.order[0])
		m.order = m.order[1:]
	}
	m.active = t
	return t
}

func (m *mirror) endTurn(t *mTurn) {
	if m.approvalTurn == t.id {
		m.closeApproval()
	}
	for i := range t.parts {
		if p := &t.parts[i]; p.tool && !p.closed {
			m.completeTool(t, p, &protocol.ChatMessage{})
		}
	}
	_ = m.dispatch(ahptypes.StateAction{Value: &ahptypes.ChatTurnCompleteAction{TurnId: t.id}})
	if m.active == t {
		m.active = nil
	}
}

// syncParts sends what is new of the turn's parts.
func (m *mirror) syncParts(t *mTurn, msgs []*protocol.ChatMessage) {
	for i, msg := range msgs {
		if i >= len(t.parts) {
			t.parts = append(t.parts, m.newPart(t, i, msg))
			continue
		}
		p := &t.parts[i]
		switch {
		case p.tool:
			if !p.closed && !msg.GetPending() {
				m.completeTool(t, p, msg)
			}
		case p.id != "":
			if text := msg.GetText(); len(text) > len(p.text) && strings.HasPrefix(text, p.text) {
				_ = m.dispatch(ahptypes.StateAction{Value: &ahptypes.ChatDeltaAction{TurnId: t.id, PartId: p.id, Content: text[len(p.text):]}})
				p.text = text
			}
		case msg.GetRole() == "assistant" && msg.GetText() != "":
			*p = m.newPart(t, i, msg)
		}
	}
}

func (m *mirror) newPart(t *mTurn, i int, msg *protocol.ChatMessage) mPart {
	switch msg.GetRole() {
	case "tool":
		id := msg.GetId()
		if id == "" {
			id = fmt.Sprintf("%s.t%d", t.id, i)
		}
		p := mPart{id: id, tool: true}
		_ = m.dispatch(ahptypes.StateAction{Value: &ahptypes.ChatToolCallStartAction{TurnId: t.id, ToolCallId: id, ToolName: msg.GetTool(), DisplayName: msg.GetTool()}})
		confirmed := ahptypes.ToolCallConfirmationReasonNotNeeded
		ready := &ahptypes.ChatToolCallReadyAction{TurnId: t.id, ToolCallId: id, InvocationMessage: ahptypes.NewStringOrMarkdownPlain(callLabel(msg)), Confirmed: &confirmed}
		if d := msg.GetDetail(); d != "" {
			ready.ToolInput = &ahptypes.ToolInput{Inline: &d}
		}
		_ = m.dispatch(ahptypes.StateAction{Value: ready})
		if !msg.GetPending() {
			m.completeTool(t, &p, msg)
		}
		return p
	case "assistant":
		if msg.GetText() == "" {
			return mPart{}
		}
		id := fmt.Sprintf("%s.p%d", t.id, i)
		_ = m.dispatch(ahptypes.StateAction{Value: &ahptypes.ChatResponsePartAction{TurnId: t.id, Part: ahptypes.ResponsePart{Value: &ahptypes.MarkdownResponsePart{
			Kind: ahptypes.ResponsePartKindMarkdown, Id: id, Content: msg.GetText(),
		}}}})
		return mPart{id: id, text: msg.GetText()}
	}
	return mPart{}
}

func callLabel(msg *protocol.ChatMessage) string {
	if d := firstLine(msg.GetDetail()); d != "" {
		return msg.GetTool() + ": " + d
	}
	return msg.GetTool()
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

func (m *mirror) completeTool(t *mTurn, p *mPart, msg *protocol.ChatMessage) {
	p.closed = true
	result := ahptypes.ToolCallResult{Success: !msg.GetError(), PastTenseMessage: ahptypes.NewStringOrMarkdownPlain(callLabel(msg))}
	if r := msg.GetResult(); r != "" {
		if len(r) > resultMax {
			r = r[:resultMax] + "\n…"
		}
		result.Content = []ahptypes.ToolResultContent{{Value: &ahptypes.ToolResultTextContent{Type: ahptypes.ToolResultContentTypeText, Text: r}}}
	}
	_ = m.dispatch(ahptypes.StateAction{Value: &ahptypes.ChatToolCallCompleteAction{TurnId: t.id, ToolCallId: p.id, Result: result}})
}

// syncApproval shows the approval the agent waits on as a tool call that
// waits for a client's confirmation, and closes the one shown once it was
// answered, anywhere.
func (m *mirror) syncApproval(a *protocol.Approval) {
	if m.approval != "" && a.GetId() != m.approval {
		m.closeApproval()
	}
	if a == nil || a.GetId() == m.approval || m.active == nil {
		return
	}
	t := m.active
	id := approvalPrefix + a.GetId()
	m.approval, m.approvalTurn, m.approvalClosed = a.GetId(), t.id, false
	_ = m.dispatch(ahptypes.StateAction{Value: &ahptypes.ChatToolCallStartAction{TurnId: t.id, ToolCallId: id, ToolName: a.GetTool(), DisplayName: a.GetTool()}})
	ready := &ahptypes.ChatToolCallReadyAction{
		TurnId: t.id, ToolCallId: id,
		InvocationMessage: ahptypes.NewStringOrMarkdownPlain(strings.TrimSpace(a.GetTool() + ": " + firstLine(a.GetDetail()))),
	}
	if in := a.GetInput(); in != "" {
		ready.ToolInput = &ahptypes.ToolInput{Inline: &in}
	}
	title := ahptypes.NewStringOrMarkdownPlain("Allow " + a.GetTool() + "?")
	ready.ConfirmationTitle = &title
	_ = m.dispatch(ahptypes.StateAction{Value: ready})
}

// closeApproval ends the approval shown: confirmed, then complete, each
// left as it is where a client already moved it on.
func (m *mirror) closeApproval() {
	if m.approval == "" {
		return
	}
	id := approvalPrefix + m.approval
	if !m.approvalClosed {
		reason := ahptypes.ToolCallConfirmationReasonUserAction
		_ = m.dispatch(ahptypes.StateAction{Value: &ahptypes.ChatToolCallConfirmedAction{TurnId: m.approvalTurn, ToolCallId: id, Approved: true, Confirmed: &reason}})
	}
	_ = m.dispatch(ahptypes.StateAction{Value: &ahptypes.ChatToolCallCompleteAction{TurnId: m.approvalTurn, ToolCallId: id, Result: ahptypes.ToolCallResult{
		Success: true, PastTenseMessage: ahptypes.NewStringOrMarkdownPlain("Answered"),
	}}})
	m.approval, m.approvalTurn, m.approvalClosed = "", "", false
}

// clientTurn notes a turn a client started: the next user message in the
// log is its.
func (m *mirror) clientTurn(t *host.Turn) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active != nil && m.active.id != t.ID {
		// The host started the client's turn over the agent's own.
		m.active = nil
	}
	m.pending, m.pendingAt = t, time.Now()
}

// turnEnded notes a turn a client cancelled.
func (m *mirror) turnEnded(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending != nil && m.pending.ID == id {
		m.pending = nil
	}
	if m.active != nil && m.active.id == id {
		m.active = nil
	}
}

// approvalAnswered notes an approval a client answered: its tool call is
// confirmed already.
func (m *mirror) approvalAnswered(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.approval == id {
		m.approvalClosed = true
	}
}

// draftFromClient notes a client's draft, and reports whether it is new to
// the session's host.
func (m *mirror) draftFromClient(text string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if text == m.draft {
		return false
	}
	m.draft = text
	return true
}
