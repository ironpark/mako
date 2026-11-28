package daemon

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agents"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// An agent about to ask for an approval runs its PermissionRequest hook
// first, and waits on it. While a front-end that shows approvals is
// watching, the hook waits for the user's answer there, so the approval
// is answered in the window; with none watching, or once it is told to,
// it leaves the question to the agent's own prompt in the terminal.

// approvalWatch is how long a front-end's listing counts as watching: a few
// of its polls.
const approvalWatch = 15 * time.Second

// maxApprovalInput caps the arguments an approval shows.
const maxApprovalInput = 16 << 10

type approvals struct {
	mu      sync.Mutex
	watched time.Time                   // the last listing by a front-end showing approvals, here or on a peer
	here    time.Time                   // the last such listing by a front-end of this daemon
	held    int                         // the front-ends of this daemon's watching for approvals on a watch stream
	pending map[string]*pendingApproval // by approval ID
	// sessions are the sessions a subscriber of their host shows the
	// approvals of, and until when, after the last left, they still count
	// as shown: a front-end that hands a session to another is not one
	// that stopped watching.
	sessions map[string]*sessionWatch
}

type sessionWatch struct {
	held  int
	until time.Time
}

type pendingApproval struct {
	session *session.Session
	reply   chan *protocol.AnswerApproval
}

// watch notes a listing by a front-end that shows approvals: one of this
// daemon's own when here is set, which the daemon passes on to its peers,
// else a peer's.
func (a *approvals) watch(here bool) {
	a.mu.Lock()
	a.watched = time.Now()
	if here {
		a.here = a.watched
	}
	a.mu.Unlock()
}

// hold counts a front-end of this daemon's as watching until it calls
// release, as its watch stream asks.
func (a *approvals) hold() (release func()) {
	a.mu.Lock()
	a.held++
	a.mu.Unlock()
	return func() {
		a.mu.Lock()
		a.held--
		// Its going counts as a last listing, as a poll's would.
		a.watched, a.here = time.Now(), time.Now()
		a.mu.Unlock()
	}
}

// watchedHere reports whether a front-end of this daemon's is watching.
func (a *approvals) watchedHere() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.held > 0 || time.Since(a.here) < approvalWatch
}

func (a *approvals) watching() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.held > 0 || time.Since(a.watched) < approvalWatch
}

// holdSession counts session id's approvals as shown until release is
// called, and for grace after.
func (a *approvals) holdSession(id string, grace time.Duration) (release func()) {
	a.mu.Lock()
	a.pruneLocked(time.Now())
	if a.sessions == nil {
		a.sessions = map[string]*sessionWatch{}
	}
	w := a.sessions[id]
	if w == nil {
		w = &sessionWatch{}
		a.sessions[id] = w
	}
	w.held++
	a.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			a.mu.Lock()
			defer a.mu.Unlock()
			w.held--
			w.until = time.Now().Add(grace)
		})
	}
}

// watchingSession reports whether session id's approvals are shown: by a
// front-end watching them all, or by one subscribed to its host.
func (a *approvals) watchingSession(id string) bool {
	a.mu.Lock()
	a.pruneLocked(time.Now())
	_, held := a.sessions[id]
	a.mu.Unlock()
	return held || a.watching()
}

// pruneLocked forgets the sessions no subscriber shows the approvals of
// any longer, their grace over.
func (a *approvals) pruneLocked(now time.Time) {
	for id, w := range a.sessions {
		if w.held <= 0 && !now.Before(w.until) {
			delete(a.sessions, id)
		}
	}
}

// forgetSession forgets session id, closed: a subscriber still holding it
// releases a hold no longer counted.
func (a *approvals) forgetSession(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, id)
}

// sessionOf is the session the agent approval id waits for, and whether
// one does.
func (a *approvals) sessionOf(id string) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	p, ok := a.pending[id]
	if !ok || p.session == nil {
		return "", false
	}
	return p.session.ID(), true
}

// ask holds an approval s's agent, the one called name, asks for until it
// is answered, the front-ends stop watching, the session ends or ctx does,
// and returns the hook's reply: "" leaves it to the agent's prompt. A
// question is held so only for an agent whose hook can answer it, and is
// answered with the user's answers.
func (a *approvals) ask(ctx context.Context, s *session.Session, name string, ev agent.Event, seq int64) hookReply {
	// The call reads as its kind does.
	kind, canon, shown := "", agent.Fields(ev.Input), ev.Input
	if tools, ok := agents.As[agent.Tools](name); ok {
		var read map[string]any
		if kind, read = tools.Tool(ev.Tool, ev.Input); read != nil {
			canon = read
			shown, _ = json.Marshal(read)
		}
	}
	var questions []*protocol.Question
	answerer, _ := agents.As[agent.HookAnswers](name)
	if kind == protocol.ToolQuestion {
		if questions = agent.Questions(canon); answerer == nil || len(questions) == 0 {
			return hookReply{}
		}
	}
	if !a.watchingSession(s.ID()) {
		return hookReply{}
	}
	var options []*protocol.ApprovalOption
	optioner, _ := agents.As[agent.ApprovalOptions](name)
	if optioner != nil && questions == nil {
		options = optioner.Options(kind, ev)
	}
	id := approvalID()
	p := &pendingApproval{session: s, reply: make(chan *protocol.AnswerApproval, 1)}
	a.mu.Lock()
	if a.pending == nil {
		a.pending = map[string]*pendingApproval{}
	}
	a.pending[id] = p
	a.mu.Unlock()
	changed, current := s.BeginApproval(seq, &protocol.Approval{
		Id: id, Tool: ev.Tool, Detail: agent.Detail(kind, canon),
		Input: indentInput(shown), Since: timestamppb.Now(), Questions: questions,
		Kind: kind, Options: options,
	})
	status := protocol.AgentIdle // a disconnected hook abandoned its request
	defer func() {
		a.mu.Lock()
		delete(a.pending, id)
		a.mu.Unlock()
		s.FinishApproval(seq, id, status)
	}()
	if !current {
		return hookReply{}
	}

	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case r := <-p.reply:
			decision, reply := r.GetDecision(), hookReply{Decision: r.GetDecision(), Message: r.GetMessage()}
			if decision == protocol.DecisionDeny && reply.Message == "" {
				reply.Message = "The user denied this in mako."
				if optioner != nil && optioner.DenyMessage(kind) != "" {
					reply.Message = optioner.DenyMessage(kind)
				}
			}
			if questions != nil && decision == protocol.DecisionAllow {
				// Allowed, a question is answered; allowed unanswered, as
				// a front-end that knows no questions would, it is left to
				// the terminal.
				if !agent.Answered(questions, r.GetAnswers()) {
					decision = protocol.DecisionTerminal
				} else {
					reply.Output = answerer.AnswerReply(ev.Input, questions, r.GetAnswers())
				}
			} else if optioner != nil && decision == protocol.DecisionAllow {
				reply.Output = optioner.AllowReply(kind, ev, r.GetOption())
			}
			if decision == protocol.DecisionTerminal {
				status = protocol.AgentWaiting
				return hookReply{}
			}
			status = protocol.AgentWorking
			return reply
		case <-tick.C:
			if !a.watchingSession(s.ID()) {
				status = protocol.AgentWaiting
				return hookReply{}
			}
		case <-s.Done():
			return hookReply{}
		case <-changed:
			return hookReply{}
		case <-ctx.Done():
			return hookReply{}
		}
	}
}

// answer passes the user's decision to the approval sessionID's agent waits
// for.
func (a *approvals) answer(sessionID string, req *protocol.AnswerApproval) error {
	switch req.GetDecision() {
	case protocol.DecisionAllow, protocol.DecisionDeny, protocol.DecisionTerminal:
	default:
		return fmt.Errorf("unknown decision %q", req.GetDecision())
	}
	a.mu.Lock()
	p, ok := a.pending[req.GetApprovalId()]
	a.mu.Unlock()
	if !ok || p.session.ID() != sessionID {
		return fmt.Errorf("the approval was answered or withdrawn")
	}
	select {
	case p.reply <- req:
	default: // answered twice at once; the first stands
	}
	return nil
}

// abortSession denies all pending approvals belonging to sessionID so any
// blocked PermissionRequest hook unblocks immediately when a turn is aborted.
func (a *approvals) abortSession(sessionID string) {
	a.mu.Lock()
	var targets []*pendingApproval
	var ids []string
	for id, p := range a.pending {
		if p.session != nil && p.session.ID() == sessionID {
			targets = append(targets, p)
			ids = append(ids, id)
		}
	}
	a.mu.Unlock()
	for i, p := range targets {
		select {
		case p.reply <- &protocol.AnswerApproval{
			SessionId:  sessionID,
			ApprovalId: ids[i],
			Decision:   protocol.DecisionDeny,
			Message:    "The user interrupted the turn.",
		}:
		default:
		}
	}
}

func approvalID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// indentInput is a tool call's arguments as the user reads them.
func indentInput(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if json.Indent(&buf, input, "", "  ") != nil {
		return ""
	}
	out := buf.Bytes()
	if len(out) > maxApprovalInput {
		out = out[:maxApprovalInput]
		for len(out) > 0 && !utf8.Valid(out) {
			out = out[:len(out)-1] // not inside a rune
		}
		return string(out) + "\n…"
	}
	return string(out)
}
