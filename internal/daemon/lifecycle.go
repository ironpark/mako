package daemon

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ironpark/mako/internal/gitstat"
	"github.com/ironpark/mako/internal/lifecycle"
	"github.com/ironpark/mako/internal/session"
)

// A session's lifecycle hooks (internal/lifecycle) run in the background,
// so that one that fails, hangs or times out never holds the session, the
// hook connection its agent waits on, or the daemon. They run under the
// daemon's context, which ends them on shutdown, and Run waits for them.

// maxQueuedHookEvents is how many events of one session wait for its
// hooks to run before more are dropped: a hook that hangs until its
// timeout does not pile them up without end.
const maxQueuedHookEvents = 16

// sessionStartHooks runs the hooks of s's start; turnDoneHooks of its
// agent's turn ending; sessionEndHooks of its agent's session ending.
func (d *Daemon) sessionStartHooks(s *session.Session) { d.fireLifecycle(lifecycle.SessionStart, s) }
func (d *Daemon) turnDoneHooks(s *session.Session)     { d.fireLifecycle(lifecycle.TurnDone, s) }
func (d *Daemon) sessionEndHooks(s *session.Session)   { d.fireLifecycle(lifecycle.SessionEnd, s) }

// fireLifecycle queues the hooks of event for s, after those of its
// events before.
func (d *Daemon) fireLifecycle(event string, s *session.Session) {
	ctx := d.ctx
	if ctx.Err() != nil || os.Getenv(lifecycle.DisableEnv) == "1" {
		return
	}
	info := s.Info()
	ev := lifecycle.Event{Name: event, SessionID: info.GetId(), Agent: info.GetAgent(), Project: info.GetProject()}
	if !d.hookRuns.start(ev.SessionID, func() { d.runLifecycle(ctx, ev) }) {
		d.log.Warn("lifecycle hooks: dropped, too many waiting or the daemon stopping", "event", event, "session", ev.SessionID)
	}
}

// runLifecycle runs the hooks of ev, the user's then the repository's,
// one after another, and logs what failed. A repository's run only while
// the user trusts its file as it reads.
func (d *Daemon) runLifecycle(ctx context.Context, ev lifecycle.Event) {
	if ctx.Err() != nil {
		return
	}
	// As the session's folder is checked out now, worktree or not: the
	// session's own record has no branch.
	ev.Branch = gitstat.Branch(ev.Project)
	hooks, err := lifecycle.LoadGlobal(d.dir)
	if err != nil {
		d.log.Warn("lifecycle hooks: not read", "file", filepath.Join(d.dir, lifecycle.GlobalFile), "err", err)
	}
	// The repository's file is read from its main work tree, for a session
	// in a folder of it or in another worktree of it too, and trusted there.
	if repo := gitstat.Repo(ev.Project); repo != "" {
		p, err := lifecycle.LoadProject(repo, d.repoTrust.Trusted)
		switch {
		case err != nil:
			d.log.Warn("lifecycle hooks: not read", "file", p.File, "err", err)
		case p.File != "" && !p.Trusted:
			d.log.Warn("lifecycle hooks: the repository's did not run, as the user has not trusted the file as it reads",
				"file", p.File, "event", ev.Name, "session", ev.SessionID, "trust", "mako repo-trust allow "+p.File)
		default:
			hooks = append(hooks, p.Hooks...)
		}
	}
	for _, h := range lifecycle.For(hooks, ev) {
		if ctx.Err() != nil {
			return
		}
		start := time.Now()
		out, err := lifecycle.Run(ctx, h, ev)
		if ctx.Err() != nil {
			return // the daemon stopped it
		}
		if err != nil {
			d.log.Warn("lifecycle hook failed", "hook", h.Label(), "event", ev.Name, "session", ev.SessionID, "err", err, "output", out)
			continue
		}
		d.log.Debug("lifecycle hook ran", "hook", h.Label(), "event", ev.Name, "session", ev.SessionID, "took", time.Since(start))
	}
}

// hookRuns are the lifecycle hooks waiting and running: a lane for each
// session, run in the order its events came, one at a time, so that two of
// its hooks never work on its folder at once.
type hookRuns struct {
	mu     sync.Mutex
	closed bool
	lanes  map[string]*hookLane
	wg     sync.WaitGroup
}

// hookLane is what of one session's hooks waits to run.
type hookLane struct {
	queue []func()
}

// start runs fn in the lane of session id after what is in it, false when
// the lane is full or the runs are closed.
func (r *hookRuns) start(id string, fn func()) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return false
	}
	if lane, ok := r.lanes[id]; ok {
		if len(lane.queue) >= maxQueuedHookEvents {
			return false
		}
		lane.queue = append(lane.queue, fn)
		return true
	}
	if r.lanes == nil {
		r.lanes = map[string]*hookLane{}
	}
	lane := &hookLane{queue: []func(){fn}}
	r.lanes[id] = lane
	r.wg.Go(func() { r.drain(id, lane) })
	return true
}

// drain runs lane's functions until it is empty, then lets it go.
func (r *hookRuns) drain(id string, lane *hookLane) {
	for {
		r.mu.Lock()
		if len(lane.queue) == 0 {
			delete(r.lanes, id)
			r.mu.Unlock()
			return
		}
		fn := lane.queue[0]
		lane.queue = lane.queue[1:]
		r.mu.Unlock()
		fn()
	}
}

// closeAndWait takes no more and waits for what was taken: the daemon's
// context, ended first, ends the hooks running and skips those waiting.
func (r *hookRuns) closeAndWait() {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	r.wg.Wait()
}
