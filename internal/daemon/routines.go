package daemon

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/forge"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/routines"
)

const (
	routineTickInterval  = 30 * time.Second
	routineForgeInterval = 3 * time.Minute
	routineForgeTimeout  = 20 * time.Second // one routine's poll of its forge
)

// routineRunner is the daemon's side of its routines. Its lock serializes
// what the daemon reads and writes of them: routines.json and
// routines-history.json, which the front-ends change through it
// (routines_requests.go), and the scheduler and the runs it starts append
// to, and routines-state.json, where the scheduler keeps its place. The scheduler's cron ticks and its polls of the forges run apart,
// so a slow forge never costs a cron schedule its minute, and runs, the
// runs the scheduler starts and its poll, which the daemon's shutdown
// waits for.
type routineRunner struct {
	mu sync.Mutex

	runs    sync.WaitGroup
	polling atomic.Bool // a poll of the forges is under way

	// The intervals of the cron ticks and of the polls, the constants
	// above when zero, and how the forges are listed, forge.List when
	// nil: tests' stand-ins.
	cronEvery, forgeEvery time.Duration
	forgeList             func(context.Context, *protocol.ForgeList) (*protocol.ForgeItems, error)

	// state is the routines' trigger states, by routine ID, as
	// routines-state.json keeps them; nil until first read. Guarded by mu.
	state map[string]routines.TriggerState
	// starting counts, by routine ID, the runs whose sessions are being
	// started, which the history does not name yet. Guarded by mu.
	starting map[string]int
}

// routineWarmup is how long a routine's new session counts as at work
// whatever its status says, while its prompt is still on the way to it.
const routineWarmup = 2 * time.Minute

// Statuses of a RunRecord.
const (
	routineStarted = "started"
	routineError   = "error"
	routineSkipped = "skipped"
)

func (rr *routineRunner) list(ctx context.Context, req *protocol.ForgeList) (*protocol.ForgeItems, error) {
	if rr.forgeList != nil {
		return rr.forgeList(ctx, req)
	}
	return forge.List(ctx, req)
}

// start runs fn on a goroutine shutdown waits for.
func (rr *routineRunner) start(fn func()) { rr.runs.Go(fn) }

// loadRoutines reads the routines.
func (d *Daemon) loadRoutines() ([]routines.Routine, error) {
	d.routines.mu.Lock()
	defer d.routines.mu.Unlock()
	return routines.Load(d.dir)
}

// updateRoutines changes the routines as change says, and saves them, the
// whole read, change and write under the runner's lock. A routines.json
// that cannot be read is left as it is.
func (d *Daemon) updateRoutines(change func([]routines.Routine) ([]routines.Routine, error)) ([]routines.Routine, error) {
	d.routines.mu.Lock()
	defer d.routines.mu.Unlock()
	list, err := routines.Load(d.dir)
	if err != nil {
		return nil, err
	}
	next, err := change(list)
	if err != nil {
		return nil, err
	}
	if err := routines.Save(d.dir, next); err != nil {
		return nil, err
	}
	return routines.Load(d.dir)
}

// recordRoutineRun appends rec to the routines' history.
func (d *Daemon) recordRoutineRun(rec routines.RunRecord) {
	d.routines.mu.Lock()
	err := routines.AppendHistory(d.dir, rec)
	d.routines.mu.Unlock()
	if err != nil {
		d.log.Warn("routines: run not recorded", "routine", rec.RoutineName, "err", err)
	}
}

// recordRoutineSkip records a trigger a routine's limits did not run it
// for, unless the routine's latest record is the same skip of the same
// trigger: a schedule refused every minute while a long run goes on would
// otherwise crowd the runs out of the history.
func (d *Daemon) recordRoutineSkip(r routines.Routine, trigger, why string) {
	d.log.Info("routines: trigger skipped", "routine", r.Name, "trigger", trigger, "why", why)
	d.routines.mu.Lock()
	defer d.routines.mu.Unlock()
	hist, err := routines.LoadHistory(d.dir)
	if err != nil {
		d.log.Warn("routines: run not recorded", "routine", r.Name, "err", err)
		return
	}
	if i := slices.IndexFunc(hist, func(rec routines.RunRecord) bool { return rec.RoutineID == r.ID }); i >= 0 &&
		hist[i].Status == routineSkipped && hist[i].Trigger == trigger && hist[i].Error == why {
		return
	}
	rec := routines.RunRecord{RoutineID: r.ID, RoutineName: r.Name, Trigger: trigger, Status: routineSkipped, Error: why, StartedAt: time.Now().Unix()}
	if err := routines.AppendHistory(d.dir, rec); err != nil {
		d.log.Warn("routines: run not recorded", "routine", r.Name, "err", err)
	}
}

// routineRuns reads the routines' history, newest first.
func (d *Daemon) routineRuns() ([]routines.RunRecord, error) {
	d.routines.mu.Lock()
	defer d.routines.mu.Unlock()
	return routines.LoadHistory(d.dir)
}

// runRoutines runs the enabled routines of routines.json, in the daemon's
// configuration directory, as their schedules and Forge triggers say, until
// ctx is done and the runs it started are.
func (d *Daemon) runRoutines(ctx context.Context) {
	if d.dir == "" {
		return
	}
	rr := &d.routines
	defer rr.runs.Wait()
	cron := time.NewTicker(cmp.Or(rr.cronEvery, routineTickInterval))
	defer cron.Stop()
	// The first poll comes with the first tick, and then one an interval.
	poll := time.NewTimer(cmp.Or(rr.cronEvery, routineTickInterval))
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-cron.C:
			d.tickCronRoutines(ctx, now)
		case <-poll.C:
			d.startForgePoll(ctx)
			poll.Reset(cmp.Or(rr.forgeEvery, routineForgeInterval))
		}
	}
}

// startForgePoll polls the forges of the Forge routines on a goroutine of
// its own, and reports whether it did: not while the last poll is still
// under way, which a slow or unreachable forge can make longer than an
// interval.
func (d *Daemon) startForgePoll(ctx context.Context) bool {
	rr := &d.routines
	if !rr.polling.CompareAndSwap(false, true) {
		return false
	}
	rr.start(func() {
		defer rr.polling.Store(false)
		list, err := d.loadRoutines()
		if err != nil {
			d.log.Warn("routines: load failed", "err", err)
			return
		}
		d.tickForgeRoutines(ctx, list)
	})
	return true
}

func (d *Daemon) tickCronRoutines(ctx context.Context, now time.Time) {
	list, err := d.loadRoutines()
	if err != nil {
		d.log.Warn("routines: load failed", "err", err)
		return
	}
	d.forgetRemovedRoutines(list)
	minKey := now.Unix() / 60
	for _, r := range list {
		if !r.Enabled || r.Trigger != routines.TriggerCron {
			continue
		}
		sched, err := routines.ParseSchedule(r.Schedule)
		if err != nil || !sched.Matches(now) {
			continue
		}
		// Once a minute, however many ticks, or restarts, fall in it.
		due := false
		d.changeRoutineState(func(st map[string]routines.TriggerState) bool {
			s := st[r.ID]
			if s.LastCronMinute == minKey {
				return false
			}
			s.LastCronMinute, due = minKey, true
			st[r.ID] = s
			return true
		})
		if due {
			d.startRoutine(ctx, r, "cron:"+r.Schedule, r.Prompt, false, now)
		}
	}
}

// routinePollMax is how many of the latest pull requests or runs a poll
// reads.
const routinePollMax = 20

func (d *Daemon) tickForgeRoutines(ctx context.Context, list []routines.Routine) {
	for _, r := range list {
		if !r.Enabled {
			// Enabled again, it runs for what comes after, not for what
			// came while it was off.
			d.changeRoutineState(func(st map[string]routines.TriggerState) bool {
				s, ok := st[r.ID]
				if !ok || s.Source == "" {
					return false
				}
				st[r.ID] = s.For("")
				return true
			})
			continue
		}
		var req *protocol.ForgeList
		switch r.Trigger {
		case routines.TriggerForgePR:
			// All of them, the newest opened first, so a pull request opened
			// and merged between two polls still moves the watermark on.
			req = &protocol.ForgeList{Root: r.Project, Kind: protocol.ForgePR, State: "all", Order: protocol.ForgeOrderCreated, Max: routinePollMax}
		case routines.TriggerForgeCIFail:
			req = &protocol.ForgeList{Root: r.Project, Kind: protocol.ForgeRuns, Max: routinePollMax}
		default:
			continue
		}
		pollCtx, cancel := context.WithTimeout(ctx, routineForgeTimeout)
		items, err := d.routines.list(pollCtx, req)
		cancel()
		if err != nil {
			d.log.Debug("routines: forge poll failed", "routine", r.Name, "err", err)
			continue
		}
		var fresh []*protocol.ForgeItem
		d.changeRoutineState(func(st map[string]routines.TriggerState) bool {
			s := st[r.ID].For(routineSource(r))
			if r.Trigger == routines.TriggerForgePR {
				fresh = newPullRequests(&s, items.GetItems())
			} else {
				fresh = newFailedRuns(&s, items.GetItems())
			}
			st[r.ID] = s
			return true
		})
		started := 0
		for _, it := range fresh {
			trigger := fmt.Sprintf("ci:#%d", it.GetNumber())
			if r.Trigger == routines.TriggerForgePR {
				trigger = fmt.Sprintf("pr:#%d", it.GetNumber())
			}
			switch {
			case !r.AllowExternal && externalForgeItem(it, items.GetForge()):
				d.recordRoutineSkip(r, trigger, "from a fork or an author without write access to the repository; the routine allows its own branches and members only")
			case started >= r.PollCap():
				d.recordRoutineSkip(r, trigger, fmt.Sprintf("over its limit of %d runs a poll", r.PollCap()))
			default:
				if d.startRoutine(ctx, r, trigger, forgeEventPrompt(r, it), true, time.Now()) {
					started++
				}
			}
		}
	}
}

// trustedAssociations are the ties to a repository whose holders may
// write to it, as GitHub names them.
var trustedAssociations = []string{"OWNER", "MEMBER", "COLLABORATOR"}

// externalForgeItem reports whether a pull request or a run is from
// outside the repository: from a fork, or by an author without write
// access, as far as the forge tells. A Forgejo's or Gitea's runs do not
// say whose branch they are of, so one of a pull request counts as
// outside.
func externalForgeItem(it *protocol.ForgeItem, forgeName string) bool {
	if it.GetFork() {
		return true
	}
	if it.GetKind() == protocol.ForgeRuns {
		return forgeName != "github" && strings.HasPrefix(it.GetEvent(), "pull_request")
	}
	a := it.GetAssociation()
	return a != "" && !slices.Contains(trustedAssociations, strings.ToUpper(a))
}

// forgeEventPrompt is the prompt of r's run for the pull request or run it:
// r's own first, the event's facts after it, fenced as untrusted data.
func forgeEventPrompt(r routines.Routine, it *protocol.ForgeItem) string {
	if it.GetKind() == protocol.ForgeRuns {
		return routines.EventPrompt(r.Prompt, "a failed CI run", []routines.EventField{
			{Name: "run", Value: fmt.Sprint(it.GetNumber())},
			{Name: "workflow", Value: it.GetWorkflow()},
			{Name: "title", Value: it.GetTitle()},
			{Name: "branch", Value: it.GetHead()},
			{Name: "commit", Value: it.GetHeadOid()},
			{Name: "event", Value: it.GetEvent()},
			{Name: "conclusion", Value: it.GetChecks()},
			{Name: "url", Value: it.GetUrl()},
		})
	}
	return routines.EventPrompt(r.Prompt, "a new pull request", []routines.EventField{
		{Name: "number", Value: fmt.Sprint(it.GetNumber())},
		{Name: "title", Value: it.GetTitle()},
		{Name: "author", Value: it.GetAuthor()},
		{Name: "branch", Value: it.GetHead()},
		{Name: "base", Value: it.GetBase()},
		{Name: "url", Value: it.GetUrl()},
	})
}

// startRoutine runs r for trigger with prompt, on a goroutine the
// daemon's shutdown waits for, unless r's limits refuse it, which is
// recorded as a skipped run: its sessions at work at once and, for a
// Forge event, its cooldown. It reports whether the run started.
func (d *Daemon) startRoutine(ctx context.Context, r routines.Routine, trigger, prompt string, forgeEvent bool, now time.Time) bool {
	release, why := d.claimRoutineRun(r, forgeEvent, now)
	if release == nil {
		d.recordRoutineSkip(r, trigger, why)
		return false
	}
	d.routines.start(func() {
		defer release()
		_, _ = d.executeRoutine(ctx, r, trigger, prompt)
	})
	return true
}

// claimRoutineRun takes a place for a run of r, if its limits leave one,
// and returns how to give it back once its session has started and is in
// the history; else nil, and why not.
func (d *Daemon) claimRoutineRun(r routines.Routine, forgeEvent bool, now time.Time) (release func(), why string) {
	rr := &d.routines
	rr.mu.Lock()
	defer rr.mu.Unlock()
	st := d.routineStateLocked()
	s := st[r.ID]
	if cd := r.CooldownPeriod(); forgeEvent && cd > 0 && s.LastForgeRun > 0 && now.Sub(time.Unix(s.LastForgeRun, 0)) < cd {
		return nil, fmt.Sprintf("in its cooldown of %s after its last Forge-triggered run", cd)
	}
	if n, limit := d.routineAtWorkLocked(r.ID, now), r.ConcurrencyCap(); n >= limit {
		return nil, fmt.Sprintf("%d of its sessions are at work already, its limit", n)
	}
	if forgeEvent {
		s.LastForgeRun = now.Unix()
		st[r.ID] = s
		d.saveRoutineStateLocked()
	}
	if rr.starting == nil {
		rr.starting = map[string]int{}
	}
	rr.starting[r.ID]++
	return func() {
		rr.mu.Lock()
		defer rr.mu.Unlock()
		if rr.starting[r.ID]--; rr.starting[r.ID] <= 0 {
			delete(rr.starting, r.ID)
		}
	}, ""
}

// routineAtWorkLocked counts the sessions of routine id at work: those
// being started, and those its runs started that are still at work, ask
// the user something, or are too new for their status to say. The
// caller holds the runner's lock.
func (d *Daemon) routineAtWorkLocked(id string, now time.Time) int {
	n := d.routines.starting[id]
	hist, err := routines.LoadHistory(d.dir)
	if err != nil {
		return n
	}
	for _, rec := range hist {
		if rec.RoutineID != id || rec.Status != routineStarted || rec.SessionID == "" {
			continue
		}
		s, ok := d.sessions.get(rec.SessionID)
		if !ok {
			continue
		}
		info := s.Info()
		if info.GetState() != protocol.StateRunning {
			continue
		}
		if status := info.GetStatus(); protocol.AtWork(status) || status == protocol.AgentWaiting || now.Sub(time.Unix(rec.StartedAt, 0)) < routineWarmup {
			n++
		}
	}
	return n
}

// routineSource is what a routine's Forge cursors are of: its trigger and
// its project.
func routineSource(r routines.Routine) string { return string(r.Trigger) + " " + r.Project }

// newPullRequests are the open pull requests of items numbered above st's
// watermark, oldest first, and moves the watermark past every number in
// items. Numbers only grow, so a pull request that is only active again,
// commented on or pushed to, is not new. The first poll of a source sets
// the watermark alone.
func newPullRequests(st *routines.TriggerState, items []*protocol.ForgeItem) []*protocol.ForgeItem {
	var fresh []*protocol.ForgeItem
	top := st.PRWatermark
	for _, it := range items {
		n := it.GetNumber()
		if st.Baselined && n > st.PRWatermark && strings.EqualFold(it.GetState(), "OPEN") {
			fresh = append(fresh, it)
		}
		top = max(top, n)
	}
	st.PRWatermark, st.Baselined = top, true
	slices.SortFunc(fresh, func(a, b *protocol.ForgeItem) int { return cmp.Compare(a.GetNumber(), b.GetNumber()) })
	return fresh
}

// newFailedRuns are the failed runs of items st has not seen, oldest
// first, which it now has. A run failed by any conclusion protocol.Failed
// names counts: timed out, cancelled, a startup failure and the like. The
// first poll of a source sees the runs failed already, and runs nothing.
func newFailedRuns(st *routines.TriggerState, items []*protocol.ForgeItem) []*protocol.ForgeItem {
	var fresh []*protocol.ForgeItem
	for _, it := range items {
		n := it.GetNumber()
		if !protocol.Failed(it.GetChecks()) || st.FailedRuns.Has(n) {
			continue
		}
		st.FailedRuns.Add(n)
		if st.Baselined {
			fresh = append(fresh, it)
		}
	}
	st.Baselined = true
	slices.SortFunc(fresh, func(a, b *protocol.ForgeItem) int { return cmp.Compare(a.GetNumber(), b.GetNumber()) })
	return fresh
}

// changeRoutineState lets change read and change the routines' trigger
// states, under the runner's lock, and saves them when it reports a
// change. They are read from routines-state.json once; one that cannot be
// read is started afresh, which runs nothing until the polls after next.
func (d *Daemon) changeRoutineState(change func(map[string]routines.TriggerState) bool) {
	d.routines.mu.Lock()
	defer d.routines.mu.Unlock()
	if change(d.routineStateLocked()) {
		d.saveRoutineStateLocked()
	}
}

// routineStateLocked is the routines' trigger states; the caller holds
// the runner's lock.
func (d *Daemon) routineStateLocked() map[string]routines.TriggerState {
	rr := &d.routines
	if rr.state == nil {
		st, err := routines.LoadState(d.dir)
		if err != nil {
			d.log.Warn("routines: trigger state unreadable, starting it afresh", "err", err)
			st = map[string]routines.TriggerState{}
		}
		rr.state = st
	}
	return rr.state
}

// saveRoutineStateLocked writes the routines' trigger states; the caller
// holds the runner's lock.
func (d *Daemon) saveRoutineStateLocked() {
	if err := routines.SaveState(d.dir, d.routineStateLocked()); err != nil {
		d.log.Warn("routines: trigger state not saved", "err", err)
	}
}

// forgetRemovedRoutines drops the trigger states of routines list no
// longer has.
func (d *Daemon) forgetRemovedRoutines(list []routines.Routine) {
	d.changeRoutineState(func(st map[string]routines.TriggerState) bool {
		changed := false
		for id := range st {
			if !slices.ContainsFunc(list, func(r routines.Routine) bool { return r.ID == id }) {
				delete(st, id)
				changed = true
			}
		}
		return changed
	})
}

// executeRoutine starts an agent session for routine r, given prompt, and
// records the run: the one way a routine runs, whether its schedule, a
// Forge event or a front-end's run action started it.
func (d *Daemon) executeRoutine(ctx context.Context, r routines.Routine, triggerReason, prompt string) (*protocol.SessionInfo, error) {
	rec := routines.RunRecord{
		RoutineID:   r.ID,
		RoutineName: r.Name,
		Trigger:     triggerReason,
		StartedAt:   time.Now().Unix(),
	}
	if _, err := os.Stat(r.Project); err != nil {
		rec.Status = routineError
		rec.Error = fmt.Sprintf("project path unavailable: %v", err)
		d.recordRoutineRun(rec)
		return nil, err
	}
	agentName, profile := agentspec.SplitTarget(r.Agent)
	req := &protocol.OpenSession{
		Agent:   agentName,
		Profile: profile,
		Project: r.Project,
		Name:    "Routine: " + r.Name,
		Prompt:  prompt,
		Cols:    120,
		Rows:    36,
	}
	if r.Worktree {
		req.Worktree = &protocol.Worktree{Branch: r.BranchName(time.Now())}
	}
	runCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	s, err := d.openSession(runCtx, req)
	if err != nil {
		rec.Status = routineError
		rec.Error = err.Error()
		d.recordRoutineRun(rec)
		d.log.Warn("routines: execution failed", "routine", r.Name, "err", err)
		return nil, err
	}
	info := d.withHead(s.Info())
	rec.Status = routineStarted
	rec.SessionID = info.GetId()
	d.recordRoutineRun(rec)
	d.log.Info("routines: started session", "routine", r.Name, "session", info.GetId(), "trigger", triggerReason)
	return info, nil
}
