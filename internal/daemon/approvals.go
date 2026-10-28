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
}

type pendingApproval struct {
	session *session.Session
	reply   chan hookReply
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

// ask holds an approval s's agent, the one called name, asks for until it
// is answered, the front-ends stop watching, the session ends or ctx does,
// and returns the hook's reply: "" leaves it to the agent's prompt, as it
// does a question, which only the agent's own prompt can take.
func (a *approvals) ask(ctx context.Context, s *session.Session, name string, ev agent.Event) hookReply {
	// The call reads as its kind does.
	kind, canon := "", agent.Fields(ev.Input)
	if tools, ok := agents.As[agent.Tools](name); ok {
		var read map[string]any
		if kind, read = tools.Tool(ev.Tool, ev.Input); read != nil {
			canon = read
			ev.Input, _ = json.Marshal(read)
		}
	}
	if kind == protocol.ToolQuestion || !a.watching() {
		s.SetStatus(protocol.AgentWaiting)
		return hookReply{}
	}
	id := approvalID()
	p := &pendingApproval{session: s, reply: make(chan hookReply, 1)}
	a.mu.Lock()
	if a.pending == nil {
		a.pending = map[string]*pendingApproval{}
	}
	a.pending[id] = p
	a.mu.Unlock()
	s.SetApproval(&protocol.Approval{
		Id: id, Tool: ev.Tool, Detail: agent.Detail(kind, canon),
		Input: indentInput(ev.Input), Since: timestamppb.Now(),
	})
	defer func() {
		a.mu.Lock()
		delete(a.pending, id)
		a.mu.Unlock()
		s.SetApproval(nil)
	}()

	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case r := <-p.reply:
			if r.Decision == protocol.DecisionTerminal {
				s.SetStatus(protocol.AgentWaiting)
				return hookReply{}
			}
			s.SetStatus(protocol.AgentWorking)
			return r
		case <-tick.C:
			if !a.watching() {
				s.SetStatus(protocol.AgentWaiting)
				return hookReply{}
			}
		case <-s.Done():
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
	message := req.GetMessage()
	if req.GetDecision() == protocol.DecisionDeny && message == "" {
		message = "The user denied this in mako."
	}
	select {
	case p.reply <- hookReply{Decision: req.GetDecision(), Message: message}:
	default: // answered twice at once; the first stands
	}
	return nil
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
