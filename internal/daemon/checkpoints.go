package daemon

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/gitstat"
	"github.com/ironpark/mako/internal/session"
)

// A turn's checkpoint is its session's work tree as the turn finds it, for
// the rewind dialog to restore. It is captured here alone, as the agent's
// prompt hook runs: the hook waits for it, and the agent for the hook, so
// the turn edits nothing before it is taken.

// checkpointTimeout bounds a capture, which the turn waits on: a work tree
// too big to capture in it goes without.
const checkpointTimeout = 4 * time.Second

// turnWait is how long a turn's hook waits on its checkpoint, within the
// agents' own wait on a hook (agent.HookTimeout).
const turnWait = checkpointTimeout + time.Second

// turnHooks are the hook events a turn starts with, by the names the
// agents give them: their hook waits for the daemon to hang up.
var turnHooks = []string{"UserPromptSubmit", "PreInvocation"}

// startsTurn reports whether a hook's event starts a turn.
func startsTurn(event string) bool { return slices.Contains(turnHooks, event) }

// turnCheckpoints says of each session whether its turn under way has its
// checkpoint. A prompt the agent hands its hook starts a turn of its own;
// agy's PreInvocation, which hands none, comes before each call of the
// model, and only the first since the last turn ended starts one.
type turnCheckpoints struct {
	mu   sync.Mutex
	open map[string]int64 // the hook seq each session's turn under way started at
}

// start reports whether a turn of session id starts with a prompt event
// of hook seq seq, one the agent said the prompt of, or not, marking it
// under way.
func (t *turnCheckpoints) start(id string, seq int64, prompted bool) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.open[id]; ok && !prompted {
		return false
	}
	if t.open == nil {
		t.open = map[string]int64{}
	}
	t.open[id] = seq
	return true
}

// end marks session id's turn over, on an event of hook seq seq: one the
// agent ran before the turn started, and the daemon reads late, ends an
// earlier turn.
func (t *turnCheckpoints) end(id string, seq int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if started, ok := t.open[id]; ok && started <= seq {
		delete(t.open, id)
	}
}

// drop forgets session id, closed for good.
func (t *turnCheckpoints) drop(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.open, id)
}

// checkpointTurn captures the checkpoint of the turn ev, of hook seq seq,
// starts, if it starts one, within checkpointTimeout of ctx, the daemon's.
func (d *Daemon) checkpointTurn(ctx context.Context, s *session.Session, seq int64, ev agent.Event) {
	root := s.Info().GetProject()
	if root == "" || !d.turns.start(s.ID(), seq, ev.Prompt != "") {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, checkpointTimeout)
	defer cancel()
	cp := gitstat.Checkpoint{Kind: gitstat.CheckpointTurn, Session: s.ID(), Prompt: gitstat.PromptKey(ev.Prompt)}
	if _, err := gitstat.CaptureSnapshot(ctx, root, cp); err != nil && !errors.Is(err, gitstat.ErrNoWorkTree) {
		d.log.Warn("checkpoint not captured", "session", s.ID(), "err", err)
	}
}

// dropCheckpoints drops the turn checkpoints of session id, closed for
// good, in its project at root (falling back to repo if root was a
// worktree that has already been removed).
func (d *Daemon) dropCheckpoints(id, root, repo string) {
	d.turns.drop(id)
	target := root
	if target == "" {
		target = repo
	}
	if target == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(d.ctx, time.Minute)
		defer cancel()
		err := gitstat.DeleteCheckpoints(ctx, target, id)
		if err != nil && repo != "" && repo != target {
			err = gitstat.DeleteCheckpoints(ctx, repo, id)
		}
		if err != nil {
			d.log.Debug("checkpoints not dropped", "session", id, "err", err)
		}
	}()
}
