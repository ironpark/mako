package daemon

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
)

// An agent waits on another of the project with wait_agent rather than
// asking list_agents again and again; and an agent that spawned others
// hears of one that stops where it cannot report, waiting on the user or
// gone, in a note typed in as its messages are.

type waitAgentArgs struct {
	SessionID string `json:"session_id" jsonschema:"The agent to wait on, of this project on this node."`
	Until     string `json:"until,omitempty" jsonschema:"turn (the default): until the turn it is on, or the next, ends. output: until text shows in what it prints from now on. Either way, the wait ends when the agent waits on the user or exits."`
	Text      string `json:"text,omitempty" jsonschema:"With until=output, the text to wait for, in its output with terminal escapes removed."`
	Timeout   int    `json:"timeout_seconds,omitempty" jsonschema:"1 to 300; defaults to 120. A timeout is a check-in, not a failure: wait again while it works."`
}

type waitAgentResult struct {
	Reason string `json:"reason" jsonschema:"turn_ended: it ended a turn. idle: it was at its prompt, its turns done and nothing sent to it pending. waiting: it waits on the user, an approval or a question in its terminal. exited. output: the text showed. message: a message reached you, which read_messages reads. timeout."`
	Status string `json:"status,omitempty"`
	State  string `json:"state"`
}

const (
	waitDefault = 120 * time.Second
	waitMax     = 300 * time.Second
	// waitPoll is how often the agent waited on is looked at.
	waitPoll = 250 * time.Millisecond
	// waitOutputKept is how much of what it printed is kept to match in.
	waitOutputKept = 64 << 10
)

func (d *Daemon) waitProjectAgent(ctx context.Context, peer *agentPeer, in waitAgentArgs) (waitAgentResult, error) {
	caller, err := d.projectCaller(peer)
	if err != nil {
		return waitAgentResult{}, err
	}
	timeout := waitDefault
	if in.Timeout != 0 {
		timeout = time.Duration(in.Timeout) * time.Second
	}
	byTurn := cmp.Or(in.Until, "turn") == "turn"
	switch {
	case timeout < time.Second || timeout > waitMax:
		return waitAgentResult{}, errors.New("timeout_seconds must be 1..300")
	case !byTurn && in.Until != "output":
		return waitAgentResult{}, errors.New("until must be turn or output")
	case !byTurn && strings.TrimSpace(in.Text) == "":
		return waitAgentResult{}, errors.New("until=output needs text to wait for")
	case in.SessionID == peer.id:
		return waitAgentResult{}, errors.New("an agent cannot wait on itself")
	}
	scope := d.projectScope(caller.Info())
	target, ok := d.sessions.get(in.SessionID)
	if !ok || !d.inProject(scope, target.Info()) {
		if d.remoteAgent(scope, in.SessionID) != nil {
			return waitAgentResult{}, errors.New("wait_agent waits on agents of this node; read_messages waits for one of another's to report")
		}
		return waitAgentResult{}, errNotInProject
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	release := d.projectAgents.waitOn(in.SessionID)
	defer release()

	var printed <-chan struct{}
	if !byTurn {
		printed = watchOutput(ctx, target, in.Text)
	}
	mine, _ := d.projectAgents.messages.pending(peer.id)
	seen := lastID(mine)
	turns := target.Turns()
	result := func(info *protocol.SessionInfo, reason string) waitAgentResult {
		return waitAgentResult{Reason: reason, Status: info.GetStatus(), State: info.GetState()}
	}
	if info := target.Info(); byTurn && turns > 0 && !protocol.AtWork(info.GetStatus()) && info.GetStatus() != protocol.AgentWaiting {
		// Its turn ended before the wait, unless a message waits to start
		// it on another.
		if inbox, _ := d.projectAgents.messages.pending(in.SessionID); len(inbox) == 0 && info.GetState() != protocol.StateExited {
			return result(info, "idle"), nil
		}
	}
	for {
		info := target.Info()
		switch {
		case info.GetState() == protocol.StateExited:
			return result(info, "exited"), nil
		case info.GetStatus() == protocol.AgentWaiting:
			return result(info, "waiting"), nil
		case byTurn && target.Turns() > turns:
			return result(info, "turn_ended"), nil
		}
		inbox, changed := d.projectAgents.messages.pending(peer.id)
		if lastID(inbox) > seen {
			return result(info, "message"), nil
		}
		select {
		case <-printed:
			return result(target.Info(), "output"), nil
		case <-changed:
		case <-time.After(waitPoll):
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return result(target.Info(), "timeout"), nil
			}
			return waitAgentResult{}, ctx.Err()
		}
	}
}

// lastID is the id of inbox's last message, 0 for none.
func lastID(inbox []agentMessage) uint64 {
	if len(inbox) == 0 {
		return 0
	}
	return inbox[len(inbox)-1].ID
}

// watchOutput is a channel closed once text shows in what s prints, its
// terminal escapes removed, from now until ctx ends.
func watchOutput(ctx context.Context, s *session.Session, text string) <-chan struct{} {
	found := make(chan struct{})
	want := []byte(text)
	sub := s.Subscribe()
	context.AfterFunc(ctx, sub.Close)
	go func() {
		defer sub.Close()
		var kept []byte
		for {
			chunk, ok := sub.Next(ctx)
			if !ok {
				return
			}
			// Only what the chunk adds, and the end of what came before it
			// the text may begin in, is looked in.
			from := max(0, len(kept)-len(want)+1)
			kept = append(kept, agent.StripANSI(string(chunk))...)
			if bytes.Contains(kept[from:], want) {
				close(found)
				return
			}
			if len(kept) > waitOutputKept {
				kept = append(kept[:0], kept[len(kept)-len(want):]...)
			}
		}
	}()
	return found
}

// waitOn counts an agent waited on until release, which keeps the note of
// its waiting from its parent, who hears of it from the wait.
func (p *projectAgents) waitOn(id string) (release func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.waits == nil {
		p.waits = map[string]int{}
	}
	p.waits[id]++
	return func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.waits[id]--; p.waits[id] <= 0 {
			delete(p.waits, id)
		}
	}
}

func (p *projectAgents) waitedOn(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.waits[id] > 0
}

// noteWaitingAfter is how long a spawned agent waits on the user before
// its parent is told: an approval answered at once is not news.
const noteWaitingAfter = 10 * time.Second

// childWatch is what a parent was told of an agent it spawned.
type childWatch struct {
	working      bool
	workSince    time.Time // when its turn began
	waitingSince time.Time // when it began to wait on the user
	noted        bool      // its parent heard of that wait
}

// noteChild tells the parent of the agent info of a wait on the user that
// lasts, once for each, unless the parent is waiting on it.
func (d *Daemon) noteChild(info *protocol.SessionInfo, parentID string, w *childWatch, now time.Time) {
	status := info.GetStatus()
	work := protocol.AtWork(status)
	if work && !w.working {
		w.workSince = now
	}
	w.working = work
	if status != protocol.AgentWaiting {
		w.waitingSince, w.noted = time.Time{}, false
		return
	}
	if w.waitingSince.IsZero() {
		w.waitingSince = now
	}
	if w.noted || now.Sub(w.waitingSince) < noteWaitingAfter {
		return
	}
	w.noted = true
	if !d.projectAgents.waitedOn(info.GetId()) {
		d.noteParent(info, parentID, "It waits on the user: an approval or a question in its terminal, which a message cannot answer.")
	}
}

// noteExit tells the parent of the agent info that it exited, unless it
// was stopped, the parent waits on it, or it reported to the parent since
// its last turn began, at turnBegan.
func (d *Daemon) noteExit(info *protocol.SessionInfo, parentID string, turnBegan time.Time) {
	if d.ctx.Err() != nil {
		return // the daemon ends its sessions as it stops
	}
	d.sessionMu.Lock()
	_, kept := d.saved[info.GetId()]
	d.sessionMu.Unlock()
	if !kept || d.projectAgents.waitedOn(info.GetId()) || d.projectAgents.messages.sentSince(info.GetId(), parentID, turnBegan) {
		return
	}
	text := "It exited without reporting."
	if code := info.ExitCode; code != nil && *code != 0 {
		text = fmt.Sprintf("It exited with code %d without reporting.", *code)
	}
	d.noteParent(info, parentID, text)
}

func (d *Daemon) noteParent(info *protocol.SessionInfo, parentID, text string) {
	if d.projectAgents.peer(parentID) == nil {
		return // the parent is gone, or takes no messages
	}
	if _, err := d.projectAgents.messages.note(info.GetId(), parentID, text, d.projectScope(info)); err != nil {
		d.log.Warn("agent note dropped", "session", info.GetId(), "parent", parentID, "err", err)
	}
}
