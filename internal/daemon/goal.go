package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agents"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
	"github.com/ironpark/mako/internal/transport"
)

var (
	claudeGoalConfirmWait = 2 * time.Second
	claudeGoalConfirmPoll = 100 * time.Millisecond
)

// goalActionGuard serializes goal actions per session so concurrent requests
// cannot interleave commands or RPCs.
type goalActionGuard struct {
	mu       sync.Mutex
	inFlight map[string]bool
}

func (g *goalActionGuard) acquire(sessionID string) (func(), bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.inFlight == nil {
		g.inFlight = map[string]bool{}
	}
	if g.inFlight[sessionID] {
		return nil, false
	}
	g.inFlight[sessionID] = true
	return func() {
		g.mu.Lock()
		delete(g.inFlight, sessionID)
		g.mu.Unlock()
	}, true
}

// handleGoalAction sets, edits, or clears a session's tracked long-running goal.
func (d *Daemon) handleGoalAction(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypeSessionInfo, func(r *protocol.GoalAction) (*protocol.SessionInfo, error) {
		return d.goalAction(ctx, r)
	})
}

func (d *Daemon) goalAction(ctx context.Context, req *protocol.GoalAction) (*protocol.SessionInfo, error) {
	s, ok := d.sessions.get(req.GetSessionId())
	if !ok {
		return nil, fmt.Errorf("unknown session %q", req.GetSessionId())
	}

	action := strings.ToLower(strings.TrimSpace(req.GetAction()))
	objective := strings.TrimSpace(req.GetObjective())
	switch action {
	case protocol.GoalActionSet, protocol.GoalActionEdit:
		if objective == "" {
			return nil, errors.New("goal objective cannot be empty")
		}
	case protocol.GoalActionClear:
	default:
		return nil, fmt.Errorf("unsupported goal action %q", req.GetAction())
	}

	release, acquired := d.goalActions.acquire(s.ID())
	if !acquired {
		return nil, errors.New("goal action already in progress")
	}
	defer release()

	d.refreshActivity(s)
	info := s.Info()
	if info.GetState() != protocol.StateRunning {
		return nil, errors.New("session is not running")
	}

	if info.GetAgent() == "codex" {
		return d.codexGoalAction(ctx, s, action, objective)
	}
	return d.commandGoalAction(ctx, s, info, action, objective)
}

func (d *Daemon) codexGoalAction(ctx context.Context, s *session.Session, action, objective string) (*protocol.SessionInfo, error) {
	b := d.codexBridge(s.ID())
	if b == nil {
		return nil, &protocol.CodedError{
			Code:    protocol.CodeUnsupported,
			Message: "Codex goal actions require a managed codex app-server session",
		}
	}
	var err error
	if action == protocol.GoalActionClear {
		err = b.clearGoal(ctx, s)
	} else {
		err = b.setGoal(ctx, s, objective)
	}
	if err != nil {
		return nil, err
	}
	return s.Info(), nil
}

func (d *Daemon) commandGoalAction(ctx context.Context, s *session.Session, info *protocol.SessionInfo, action, objective string) (*protocol.SessionInfo, error) {
	gc, ok := agents.As[agent.GoalCommands](info.GetAgent())
	if !ok {
		return nil, &protocol.CodedError{
			Code:    protocol.CodeUnsupported,
			Message: fmt.Sprintf("agent %q does not support goal actions", info.GetAgent()),
		}
	}

	cur := info.GetGoal()
	switch action {
	case protocol.GoalActionClear:
		if !cur.Active() {
			return nil, errors.New("no active goal to clear")
		}
		if !cur.GetCanClear() {
			return nil, &protocol.CodedError{
				Code:    protocol.CodeUnsupported,
				Message: "clearing the goal is not supported on this session",
			}
		}
	case protocol.GoalActionEdit:
		if !cur.Active() {
			return nil, errors.New("no active goal to edit")
		}
		if !cur.GetCanEdit() {
			return nil, &protocol.CodedError{
				Code:    protocol.CodeUnsupported,
				Message: "editing the goal is not supported on this session",
			}
		}
	}

	// Readiness checks so `/goal ...` never corrupts an active turn, dialog, or approval.
	if info.GetApproval() != nil || info.GetDialog() != nil {
		return nil, errors.New("goal action is not ready while waiting on a dialog or approval")
	}
	if info.GetBusy() {
		return nil, errors.New("goal action is not ready while the agent is busy")
	}
	atPrompt, _ := s.AtPrompt()
	if !atPrompt {
		return nil, errors.New("goal action is only available while the agent is idle at its prompt")
	}

	var cmd string
	var wantText string
	if action == protocol.GoalActionClear {
		cmd = gc.GoalClearCommand()
	} else {
		cmd = gc.GoalSetCommand(objective)
		wantText = strings.TrimSpace(strings.TrimPrefix(cmd, "/"+gc.GoalCommandName()))
	}
	if cmd == "" {
		return nil, errors.New("goal objective cannot be empty")
	}

	prevRev := cur.GetSourceRevision()
	if err := s.Send(cmd, []byte{'\r'}); err != nil {
		return nil, err
	}

	if s.TranscriptPath() != "" && claudeGoalConfirmWait > 0 {
		if err := d.waitForGoalTranscript(ctx, s, action, prevRev, wantText); err != nil {
			return nil, err
		}
	}
	return s.Info(), nil
}

// waitForGoalTranscript polls refreshActivity briefly after submitting a slash
// command so GoalAction returns the updated SessionInfo as soon as the agent
// writes its goal_status attachment.
func (d *Daemon) waitForGoalTranscript(ctx context.Context, s *session.Session, action, prevRev, wantText string) error {
	deadline := time.NewTimer(claudeGoalConfirmWait)
	defer deadline.Stop()
	tick := time.NewTicker(claudeGoalConfirmPoll)
	defer tick.Stop()
	for {
		d.refreshActivity(s)
		g := s.Info().GetGoal()
		if action == protocol.GoalActionClear {
			if !g.Active() {
				return nil
			}
		} else if g.Active() && g.GetSourceRevision() != prevRev && (wantText == "" || strings.TrimSpace(g.GetText()) == wantText) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.Done():
			return nil
		case <-deadline.C:
			return nil
		case <-tick.C:
		}
	}
}
