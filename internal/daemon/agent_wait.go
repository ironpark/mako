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

// An agent registers a non-blocking one-shot notification on another project
// agent with watch_agent (returning immediately; Mako pushes a note when the
// condition triggers), and an agent that spawned others also hears in a note
// if a child waits on the user or exits without reporting.

type watchAgentArgs struct {
	SessionID string `json:"session_id" jsonschema:"The agent to watch, of this project, on any node."`
	Until     string `json:"until,omitempty" jsonschema:"turn (default): notify once its current or next turn ends. output: notify once text appears in its terminal output on this node. Either way, you are also notified if it waits on the user or exits."`
	Text      string `json:"text,omitempty" jsonschema:"With until=output, the text to match in its terminal output (ANSI escapes removed)."`
}

type watchAgentResult struct {
	WatchID        string `json:"watch_id,omitempty"`
	SessionID      string `json:"session_id"`
	Until          string `json:"until"`
	AlreadyMatched bool   `json:"already_matched,omitempty"`
	Reason         string `json:"reason,omitempty"`
	Status         string `json:"status,omitempty"`
	State          string `json:"state"`
	Hint           string `json:"hint,omitempty"`
}

const (
	// waitRecheck is how often a watched agent is looked at besides when its
	// state changes: for status gone stale as it is read, and an agent of
	// another node, whose state comes as its daemon sends it.
	waitRecheck = time.Second
	// waitOutputKept is how much of what it printed is kept to match in.
	waitOutputKept = 64 << 10
)

// waited is an agent observed by watch_agent or mako-msg wait, local or remote.
type waited struct {
	info    func() *protocol.SessionInfo // nil once it is gone
	changed func() <-chan struct{}       // nil for one never announced
}

func (d *Daemon) watchProjectAgent(peer *agentPeer, in watchAgentArgs) (watchAgentResult, error) {
	caller, err := d.projectCaller(peer)
	if err != nil {
		return watchAgentResult{}, err
	}
	until := cmp.Or(strings.TrimSpace(in.Until), "turn")
	byTurn := until == "turn"
	switch {
	case !byTurn && until != "output":
		return watchAgentResult{}, errors.New("until must be turn or output")
	case !byTurn && strings.TrimSpace(in.Text) == "":
		return watchAgentResult{}, errors.New("until=output needs text to wait for")
	case in.SessionID == peer.id:
		return watchAgentResult{}, errors.New("an agent cannot watch itself")
	}

	callerInfo := caller.Info()
	scope := d.projectScope(callerInfo)
	var target waited
	var localTarget *session.Session
	if s, ok := d.sessions.get(in.SessionID); ok && d.inProject(scope, s.Info()) {
		localTarget = s
		target = waited{info: s.Info, changed: s.Changed}
	} else if d.remoteAgent(scope, in.SessionID) != nil {
		if !byTurn {
			return watchAgentResult{}, errors.New("until=output watches agents of this node; watch for another's turn instead")
		}
		target = waited{info: func() *protocol.SessionInfo { return d.remoteAgent(scope, in.SessionID) }}
	} else {
		return watchAgentResult{}, errNotInProject
	}

	info := target.info()
	turns := info.GetTurns()
	status := info.GetStatus()
	state := info.GetState()
	if byTurn {
		switch {
		case state == protocol.StateExited:
			return watchAgentResult{
				SessionID: in.SessionID, Until: until, AlreadyMatched: true,
				Reason: "exited", Status: status, State: state,
			}, nil
		case status == protocol.AgentWaiting:
			return watchAgentResult{
				SessionID: in.SessionID, Until: until, AlreadyMatched: true,
				Reason: "waiting", Status: status, State: state,
			}, nil
		case turns > 0 && !protocol.AtWork(status):
			if inbox, _ := d.projectAgents.messages.pending(in.SessionID); len(inbox) == 0 {
				return watchAgentResult{
					SessionID: in.SessionID, Until: until, AlreadyMatched: true,
					Reason: "idle", Status: status, State: state,
				}, nil
			}
		}
	}

	watchID, releaseWatch, ok := d.projectAgents.claimWatch(peer.id)
	if !ok {
		return watchAgentResult{}, fmt.Errorf("too many active watches (limit %d)", maxWatchesPerSession)
	}
	releaseWait := d.projectAgents.waitOn(in.SessionID)
	watchCtx, cancel := context.WithCancel(peer.ctx)
	var printed <-chan struct{}
	if !byTurn && localTarget != nil {
		printed = watchOutput(watchCtx, localTarget, in.Text)
	}

	go func() {
		defer cancel()
		defer releaseWait()
		defer releaseWatch()

		reason := d.runAgentWatch(watchCtx, target, byTurn, turns, printed)
		if reason == "" || peer.ctx.Err() != nil {
			return
		}
		var text string
		switch reason {
		case "turn_ended":
			text = fmt.Sprintf("Watch %s triggered: its turn ended.", watchID)
		case "output":
			text = fmt.Sprintf("Watch %s triggered: terminal output matched %q.", watchID, in.Text)
		case "waiting":
			text = fmt.Sprintf("Watch %s triggered: it waits on the user (approval or question in its terminal).", watchID)
		case "exited":
			text = fmt.Sprintf("Watch %s triggered: it exited.", watchID)
		case "gone":
			text = fmt.Sprintf("Watch %s triggered: it is no longer listed or reachable.", watchID)
		default:
			text = fmt.Sprintf("Watch %s triggered (%s).", watchID, reason)
		}
		if _, err := d.projectAgents.messages.note(in.SessionID, peer.id, text, callerInfo.GetProject()); err != nil {
			d.log.Warn("agent watch: notification not sent", "watch", watchID, "to", peer.id, "target", in.SessionID, "err", err)
		}
	}()

	return watchAgentResult{
		WatchID:   watchID,
		SessionID: in.SessionID,
		Until:     until,
		Status:    status,
		State:     state,
		Hint:      "Notification registered. Continue other work or end your turn—Mako will push a note into your session as soon as it triggers.",
	}, nil
}

func (d *Daemon) runAgentWatch(ctx context.Context, target waited, byTurn bool, turns uint64, printed <-chan struct{}) string {
	recheck := time.NewTicker(waitRecheck)
	defer recheck.Stop()
	worked := false
	for {
		var changed <-chan struct{}
		if target.changed != nil {
			changed = target.changed()
		}
		info := target.info()
		status := info.GetStatus()
		worked = worked || protocol.AtWork(status)
		switch {
		case info == nil:
			return "gone"
		case info.GetState() == protocol.StateExited:
			return "exited"
		case status == protocol.AgentWaiting:
			return "waiting"
		case byTurn && (info.GetTurns() > turns || turns == 0 && worked && !protocol.AtWork(status)):
			return "turn_ended"
		}
		select {
		case <-printed:
			return "output"
		case <-changed:
		case <-recheck.C:
		case <-ctx.Done():
			return ""
		}
	}
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
				// Waiting for text longer than that, all of it is kept, as
				// the text may begin anywhere in it.
				kept = append(kept[:0], kept[max(0, len(kept)-len(want)):]...)
			}
		}
	}()
	return found
}

// waitOn counts an agent watched until release, which keeps the duplicate note
// of its waiting from its parent, who hears of it from the watch or CLI wait.
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
// lasts, once for each, unless the parent is watching it.
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
// was stopped, the parent watches it, or it reported to the parent since
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

func (d *Daemon) noteRemoteExit(info *protocol.SessionInfo, parentID string, turnBegan time.Time) {
	if d.ctx.Err() != nil {
		return
	}
	if d.projectAgents.wasRemoteStopped(info.GetId()) || d.projectAgents.waitedOn(info.GetId()) || d.projectAgents.messages.sentSince(info.GetId(), parentID, turnBegan) {
		return
	}
	text := "It exited without reporting."
	if code := info.ExitCode; code != nil && *code != 0 {
		text = fmt.Sprintf("It exited with code %d without reporting.", *code)
	}
	d.noteParent(info, parentID, text)
}

func (d *Daemon) noteParent(info *protocol.SessionInfo, parentID, text string) {
	project := info.GetProject()
	if parent, ok := d.sessions.get(parentID); ok {
		project = d.projectScope(parent.Info())
	}
	if _, err := d.projectAgents.messages.note(info.GetId(), parentID, text, project); err != nil {
		d.log.Warn("agent note: not sent", "parent", parentID, "child", info.GetId(), "err", err)
	}
}
