package daemon

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transcript"
)

// An agent reads another agent of its project, on this node or another,
// with read_agent (and mako-msg inspect): what the session is and what it
// does now, from its SessionInfo, and its latest turns of conversation,
// from its log, cut to what an agent's context takes. The view is shaped
// as the Agent Host Protocol's session and chat state are, a turn being
// the user's message and the parts of the reply to it, so that it moves to
// that protocol's channels without a change of meaning (see
// docs/agent-host-protocol-plan.md).

// The bounds of what read_agent returns. The text of a turn's parts, in
// all, stays under readAgentBudget: older turns go first, then a turn's
// older parts.
const (
	readAgentTurns    = 3
	readAgentMaxTurns = 20
	readAgentText     = 2000 // runes of a prompt or of a reply's text
	readAgentDetail   = 300  // runes of a call's detail, as its command
	readAgentOutput   = 500  // runes of a call's result
	readAgentParts    = 40   // parts kept of a turn, its latest
	readAgentBudget   = 48 << 10
	readAgentTasks    = 10
	readAgentSubs     = 10
	readAgentTimeout  = relayTimeout
)

type readAgentArgs struct {
	SessionID string `json:"session_id" jsonschema:"The agent to read, of this project, on any node (an id from list_agents)."`
	Turns     *int   `json:"turns,omitempty" jsonschema:"How many of its latest conversation turns to return, 0 to 20; defaults to 3. 0 returns the session alone, without reading its conversation."`
}

// readAgentResult is another agent of the project, as read_agent shows it.
type readAgentResult struct {
	Session agentSessionView `json:"session"`
	// Turns are its latest turns of conversation, oldest first.
	Turns []agentTurn `json:"turns"`
	// EarlierTurns counts the turns read but left out, before Turns.
	EarlierTurns int `json:"earlier_turns,omitempty"`
	// Truncated says its log holds more, before what was read.
	Truncated bool `json:"truncated,omitempty"`
	// ChatUnavailable says why no conversation was read, when none was.
	ChatUnavailable string `json:"chat_unavailable,omitempty"`
}

type agentSessionView struct {
	ID          string          `json:"id"`
	Agent       string          `json:"agent"`
	Profile     string          `json:"profile,omitempty"`
	Name        string          `json:"name,omitempty"`
	Title       string          `json:"title,omitempty"`
	ParentID    string          `json:"parent_session_id,omitempty"`
	Node        string          `json:"node,omitempty"`
	NodeID      string          `json:"node_id,omitempty"`
	Folder      string          `json:"folder,omitempty"`
	Branch      string          `json:"branch,omitempty"`
	Worktree    string          `json:"worktree,omitempty"`
	State       string          `json:"state"`
	Status      string          `json:"status,omitempty"`
	StatusAt    string          `json:"status_at,omitempty"`
	Messaging   bool            `json:"messaging"`
	TurnsEnded  uint64          `json:"turns_ended,omitempty"`
	StartedAt   string          `json:"started_at,omitempty"`
	LastMessage string          `json:"last_message,omitempty"`
	WaitingOn   *agentWaiting   `json:"waiting_on,omitempty"`
	AuthError   string          `json:"auth_error,omitempty"`
	Goal        *agentGoal      `json:"goal,omitempty"`
	Tasks       []agentTask     `json:"tasks,omitempty"`
	Subagents   []agentSubagent `json:"subagents,omitempty"`
	Usage       *agentUsage     `json:"usage,omitempty"`
}

// agentWaiting is what the agent waits on the user for.
type agentWaiting struct {
	Kind      string   `json:"kind"` // approval, question, dialog
	Tool      string   `json:"tool,omitempty"`
	Detail    string   `json:"detail,omitempty"`
	Questions []string `json:"questions,omitempty"`
	Options   []string `json:"options,omitempty"`
}

type agentGoal struct {
	Text   string `json:"text,omitempty"`
	Status string `json:"status,omitempty"`
}

type agentTask struct {
	Command     string `json:"command"`
	Description string `json:"description,omitempty"`
	Running     bool   `json:"running"`
	Status      string `json:"status,omitempty"`
}

type agentSubagent struct {
	ID          string `json:"id"`
	Kind        string `json:"kind,omitempty"`
	Description string `json:"description,omitempty"`
	Running     bool   `json:"running"`
	Tool        string `json:"tool,omitempty"`
	Detail      string `json:"detail,omitempty"`
}

type agentUsage struct {
	Model   string `json:"model,omitempty"`
	Effort  string `json:"effort,omitempty"`
	Mode    string `json:"permission_mode,omitempty"`
	Context int64  `json:"context,omitempty"`
	Window  int64  `json:"window,omitempty"`
}

// agentTurn is one turn of conversation: the user's message, and the
// parts of the reply to it in order.
type agentTurn struct {
	// Message is what the turn answers; empty for a turn whose start came
	// before what was read.
	Message string      `json:"message,omitempty"`
	Parts   []agentPart `json:"parts"`
	// State is complete, or in_progress for the turn the agent is on.
	State string `json:"state"`
	// OmittedParts counts the turn's older parts left out.
	OmittedParts int `json:"omitted_parts,omitempty"`
}

// agentPart is a part of a reply: the agent's text, or a call it made.
type agentPart struct {
	Kind    string `json:"kind"` // text, tool_call
	Text    string `json:"text,omitempty"`
	Tool    string `json:"tool,omitempty"`
	Call    string `json:"call_kind,omitempty"`
	Detail  string `json:"detail,omitempty"`
	Result  string `json:"result,omitempty"`
	Error   bool   `json:"error,omitempty"`
	Pending bool   `json:"pending,omitempty"`
}

const (
	turnComplete   = "complete"
	turnInProgress = "in_progress"
	partText       = "text"
	partToolCall   = "tool_call"
)

// readProjectAgent reads the agent of session in.SessionID for the agent
// of peer, when the two are of one project.
func (d *Daemon) readProjectAgent(ctx context.Context, peer *agentPeer, in readAgentArgs) (readAgentResult, error) {
	caller, err := d.projectCaller(peer)
	if err != nil {
		return readAgentResult{}, err
	}
	turns := readAgentTurns
	if in.Turns != nil {
		turns = *in.Turns
	}
	if turns < 0 || turns > readAgentMaxTurns {
		return readAgentResult{}, fmt.Errorf("turns must be 0..%d", readAgentMaxTurns)
	}
	id := strings.TrimSpace(in.SessionID)
	if id == "" {
		return readAgentResult{}, errors.New("session_id is required")
	}
	scope := d.projectScope(caller.Info())

	var info *protocol.SessionInfo
	var read func(context.Context) (*protocol.Chat, error)
	if s, ok := d.sessions.get(id); ok && d.inProject(scope, s.Info()) {
		info = s.Info()
		d.sessionMu.Lock()
		if saved, ok := d.saved[id]; ok && info.GetParentSessionId() == "" {
			info.ParentSessionId = saved.ParentID
		}
		d.sessionMu.Unlock()
		info.AgentMessaging = d.takesMessages(info)
		read = func(ctx context.Context) (*protocol.Chat, error) {
			chat, err := d.chatForSession(ctx, s, transcript.Span{})
			if err != nil {
				return nil, err
			}
			reply := shallow(chat)
			reply.Usage = withReported(chat.GetUsage(), s.Reported())
			return reply, nil
		}
	} else if remote := d.remoteAgent(scope, id); remote != nil {
		info = remote
		read = func(ctx context.Context) (*protocol.Chat, error) {
			ctx, cancel := context.WithTimeout(ctx, readAgentTimeout)
			defer cancel()
			chat, err := forward[*protocol.Chat](ctx, d, protocol.TypeReadChat, &protocol.ReadChat{NodeId: remote.GetNodeId(), SessionId: id})
			if err != nil {
				return nil, fmt.Errorf("%s did not answer: %w", d.nodeName(remote.GetNodeId()), err)
			}
			return chat, nil
		}
	} else {
		return readAgentResult{}, errors.New("session is not an agent of this project")
	}

	out := readAgentResult{Session: d.agentSessionViewOf(info), Turns: []agentTurn{}}
	if turns == 0 {
		return out, nil
	}
	chat, err := read(ctx)
	switch {
	case err != nil:
		out.ChatUnavailable = err.Error()
	case !chat.GetSupported():
		out.ChatUnavailable = "mako cannot read this agent's conversation"
	case chat.GetConversation() == "":
		out.ChatUnavailable = "the agent has saved no conversation yet"
	}
	if out.ChatUnavailable != "" {
		return out, nil
	}
	out.Session.Usage = agentUsageOf(chat.GetUsage())
	out.Session.Subagents = agentSubagentsOf(chat.GetAgents())
	all := agentTurns(chat.GetMessages(), atWorkOrWaiting(info.GetStatus()))
	out.Truncated = chat.GetTruncated() || chat.GetStart() > 0
	if len(all) > turns {
		out.EarlierTurns = len(all) - turns
		all = all[len(all)-turns:]
	}
	kept, dropped := fitTurns(all, readAgentBudget)
	out.EarlierTurns += dropped
	out.Turns = kept
	return out, nil
}

// agentSessionViewOf is info as read_agent shows it.
func (d *Daemon) agentSessionViewOf(info *protocol.SessionInfo) agentSessionView {
	v := agentSessionView{
		ID:          info.GetId(),
		Agent:       info.GetAgent(),
		Profile:     info.GetProfile(),
		Name:        info.GetName(),
		Title:       info.GetTitle(),
		ParentID:    info.GetParentSessionId(),
		Folder:      info.GetProject(),
		Branch:      info.GetBranch(),
		Worktree:    info.GetWorktree().GetBranch(),
		State:       info.GetState(),
		Status:      info.GetStatus(),
		StatusAt:    stamp(info.GetStatusAt()),
		Messaging:   info.GetAgentMessaging(),
		TurnsEnded:  info.GetTurns(),
		StartedAt:   stamp(info.GetStartedAt()),
		LastMessage: stamp(info.GetLastMessage()),
		AuthError:   info.GetAuthError().GetSummary(),
	}
	if node := info.GetNodeId(); node != "" && node != d.config().NodeID {
		v.Node, v.NodeID = d.nodeName(node), node
	}
	v.WaitingOn = agentWaitingOf(info)
	if g := info.GetGoal(); g != nil && (g.GetText() != "" || g.GetStatus() != "") {
		v.Goal = &agentGoal{Text: clipRunes(g.GetText(), readAgentText), Status: g.GetStatus()}
	}
	for _, t := range info.GetTasks() {
		v.Tasks = append(v.Tasks, agentTask{
			Command:     clipRunes(firstLine(t.GetCommand()), readAgentDetail),
			Description: clipRunes(t.GetDescription(), readAgentDetail),
			Running:     t.GetRunning(),
			Status:      t.GetStatus(),
		})
	}
	if len(v.Tasks) > readAgentTasks {
		v.Tasks = v.Tasks[len(v.Tasks)-readAgentTasks:]
	}
	return v
}

// agentWaitingOf is what the session's agent waits on the user for: an
// approval (or the questions it asks), else a dialog in its terminal.
func agentWaitingOf(info *protocol.SessionInfo) *agentWaiting {
	if a := info.GetApproval(); a != nil {
		w := &agentWaiting{Kind: "approval", Tool: a.GetTool(), Detail: clipRunes(cmp.Or(a.GetDetail(), a.GetInput()), readAgentDetail)}
		if qs := a.GetQuestions(); len(qs) > 0 {
			w.Kind = "question"
			for _, q := range qs {
				w.Questions = append(w.Questions, clipRunes(q.GetQuestion(), readAgentDetail))
			}
		}
		return w
	}
	if dlg := info.GetDialog(); dlg != nil {
		w := &agentWaiting{Kind: "dialog", Detail: clipRunes(dlg.GetText(), readAgentDetail)}
		for _, o := range dlg.GetOptions() {
			w.Options = append(w.Options, clipRunes(o, readAgentDetail))
		}
		return w
	}
	return nil
}

func agentUsageOf(u *protocol.Usage) *agentUsage {
	if u == nil || (u.GetModel() == "" && u.GetContext() == 0 && u.GetWindow() == 0) {
		return nil
	}
	return &agentUsage{Model: u.GetModel(), Effort: u.GetEffort(), Mode: u.GetPermissionMode(), Context: u.GetContext(), Window: u.GetWindow()}
}

// agentSubagentsOf are the subagents the conversation started, those at
// work first, then the latest of the rest.
func agentSubagentsOf(list []*protocol.SubAgent) []agentSubagent {
	var running, ended []agentSubagent
	for _, a := range list {
		v := agentSubagent{
			ID:          a.GetId(),
			Kind:        a.GetKind(),
			Description: clipRunes(a.GetDescription(), readAgentDetail),
			Running:     a.GetRunning(),
			Tool:        a.GetTool(),
			Detail:      clipRunes(a.GetDetail(), readAgentDetail),
		}
		if v.Running {
			running = append(running, v)
		} else {
			ended = append(ended, v)
		}
	}
	out := running
	if room := readAgentSubs - len(out); room > 0 && len(ended) > 0 {
		out = append(out, ended[max(0, len(ended)-room):]...)
	}
	if len(out) > readAgentSubs {
		out = out[:readAgentSubs]
	}
	return out
}

// agentTurns groups a conversation's messages into turns: each user
// message starts one, and what the agent wrote and called after it, up to
// the next, is its reply. Messages before the first user message make a
// turn of their own, begun before what was read. The last turn is in
// progress while the agent is at work on it or waits on the user, or while
// one of its calls is unanswered.
func agentTurns(msgs []*protocol.ChatMessage, active bool) []agentTurn {
	var out []agentTurn
	for _, m := range msgs {
		switch m.GetRole() {
		case "user":
			out = append(out, agentTurn{Message: clipRunes(m.GetText(), readAgentText), Parts: []agentPart{}, State: turnComplete})
			continue
		case "assistant":
			if strings.TrimSpace(m.GetText()) == "" {
				continue
			}
		case "tool":
		default:
			continue
		}
		if len(out) == 0 {
			out = append(out, agentTurn{Parts: []agentPart{}, State: turnComplete})
		}
		t := &out[len(out)-1]
		t.Parts = append(t.Parts, agentPartOf(m))
	}
	for i := range out {
		t := &out[i]
		if n := len(t.Parts); n > readAgentParts {
			t.OmittedParts = n - readAgentParts
			t.Parts = t.Parts[n-readAgentParts:]
		}
	}
	if n := len(out); n > 0 {
		last := &out[n-1]
		if active || hasPending(last.Parts) {
			last.State = turnInProgress
		}
	}
	return out
}

func agentPartOf(m *protocol.ChatMessage) agentPart {
	if m.GetRole() == "assistant" {
		return agentPart{Kind: partText, Text: clipRunes(m.GetText(), readAgentText)}
	}
	return agentPart{
		Kind:    partToolCall,
		Tool:    m.GetTool(),
		Call:    m.GetKind(),
		Detail:  clipRunes(cmp.Or(m.GetDetail(), m.GetText()), readAgentDetail),
		Result:  clipRunes(m.GetResult(), readAgentOutput),
		Error:   m.GetError(),
		Pending: m.GetPending(),
	}
}

func hasPending(parts []agentPart) bool {
	for _, p := range parts {
		if p.Pending {
			return true
		}
	}
	return false
}

// fitTurns keeps the latest of turns whose text fits budget bytes, and
// how many older turns it left out. The latest turn is always kept: its
// older parts are left out until it fits.
func fitTurns(turns []agentTurn, budget int) ([]agentTurn, int) {
	used, from := 0, len(turns)
	for from > 0 {
		size := turnSize(turns[from-1])
		if used+size > budget && from < len(turns) {
			break
		}
		used += size
		from--
	}
	kept := turns[from:]
	if len(kept) == 1 {
		t := &kept[0]
		for turnSize(*t) > budget && len(t.Parts) > 0 {
			t.Parts = t.Parts[1:]
			t.OmittedParts++
		}
	}
	return kept, from
}

func turnSize(t agentTurn) int {
	n := len(t.Message)
	for _, p := range t.Parts {
		n += len(p.Text) + len(p.Tool) + len(p.Detail) + len(p.Result) + 32
	}
	return n
}

func atWorkOrWaiting(status string) bool {
	return protocol.AtWork(status) || status == protocol.AgentWaiting
}

func stamp(ts *timestamppb.Timestamp) string {
	if ts == nil || !ts.IsValid() || ts.AsTime().IsZero() {
		return ""
	}
	return ts.AsTime().UTC().Format(time.RFC3339)
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
