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
	// started, which active does not name yet. Guarded by mu.
	starting map[string]int
	// active tracks sessions started by each routine (routineID -> sessionID
	// -> startedAt) so concurrency caps do not depend solely on the bounded
	// history file. Guarded by mu.
	active map[string]map[string]time.Time
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
// that cannot be read is left as it is. If afterSave is non-nil, it runs
// under the same lock once Save succeeds.
func (d *Daemon) updateRoutines(change func([]routines.Routine) ([]routines.Routine, error), afterSave ...func()) ([]routines.Routine, error) {
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
	for _, fn := range afterSave {
		if fn != nil {
			fn()
		}
	}
	return routines.Load(d.dir)
}

// loadRoutinesAndPruneState loads routines.json and removes trigger state
// for deleted routines in a single lock acquisition so a routine added
// concurrently never has its state dropped.
func (d *Daemon) loadRoutinesAndPruneState() ([]routines.Routine, error) {
	d.routines.mu.Lock()
	defer d.routines.mu.Unlock()
	list, err := routines.Load(d.dir)
	if err != nil {
		return nil, err
	}
	st := d.routineStateLocked()
	changed := false
	for id := range st {
		if !slices.ContainsFunc(list, func(r routines.Routine) bool { return r.ID == id }) {
			delete(st, id)
			changed = true
		}
	}
	if changed {
		d.saveRoutineStateLocked()
	}
	return list, nil
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
	list, err := d.loadRoutinesAndPruneState()
	if err != nil {
		d.log.Warn("routines: load failed", "err", err)
		return
	}
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
			d.startRoutine(ctx, r, "cron:"+r.Schedule, r.Prompt, false, false, now)
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
		// Re-verify the routine under the lock after the network call when
		// routines.json is present so a routine removed, disabled, or
		// repointed while polling is never resurrected or launched.
		var (
			fresh []*protocol.ForgeItem
			cur   = r
			valid = true
		)
		d.changeRoutineState(func(st map[string]routines.TriggerState) bool {
			if _, statErr := os.Stat(routines.FilePath(d.dir)); statErr == nil {
				latest, err := routines.Load(d.dir)
				if err != nil {
					valid = false
					return false
				}
				idx := slices.IndexFunc(latest, func(x routines.Routine) bool { return x.ID == r.ID })
				if idx < 0 || !latest[idx].Enabled || routineSource(latest[idx]) != routineSource(r) {
					valid = false
					return false
				}
				cur = latest[idx]
			}
			s := st[cur.ID].For(routineSource(cur))
			if cur.Trigger == routines.TriggerForgePR {
				fresh = newPullRequests(&s, items.GetItems())
			} else {
				fresh = newFailedRuns(&s, items.GetItems())
			}
			st[cur.ID] = s
			return true
		})
		if !valid {
			continue
		}
		started := 0
		for _, it := range fresh {
			trigger := fmt.Sprintf("ci:#%d", it.GetNumber())
			if cur.Trigger == routines.TriggerForgePR {
				trigger = fmt.Sprintf("pr:#%d", it.GetNumber())
			}
			switch {
			case !cur.AllowExternal && externalForgeItem(it, items.GetForge()):
				d.recordRoutineSkip(cur, trigger, "from a fork or an author without write access to the repository; the routine allows its own branches and members only")
			case started >= cur.PollCap():
				d.recordRoutineSkip(cur, trigger, fmt.Sprintf("over its limit of %d runs a poll", cur.PollCap()))
			default:
				// Check cooldown only for the first run of this poll; once
				// outside cooldown, this poll may start up to PollCap() runs.
				if d.startRoutine(ctx, cur, trigger, forgeEventPrompt(cur, it), true, started == 0, time.Now()) {
					started++
				}
			}
		}
	}
}

// trustedAssociations are the ties to a repository whose holders may
// write to it, as GitHub names them.
var trustedAssociations = []string{"OWNER", "MEMBER", "COLLABORATOR"}

// trustedGitHubRunEvents are GitHub Actions workflow events whose triggers
// and display titles originate from repository branches/maintainers rather
// than arbitrary external issues, comments, or pull_request_target payloads.
var trustedGitHubRunEvents = []string{
	"push", "schedule", "workflow_dispatch", "release",
	"pull_request", "merge_group", "deployment", "workflow_call", "repository_dispatch",
}

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
		if forgeName == "github" {
			ev := strings.ToLower(strings.TrimSpace(it.GetEvent()))
			return ev != "" && !slices.Contains(trustedGitHubRunEvents, ev)
		}
		return strings.HasPrefix(it.GetEvent(), "pull_request")
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
func (d *Daemon) startRoutine(ctx context.Context, r routines.Routine, trigger, prompt string, forgeEvent, checkCooldown bool, now time.Time) bool {
	onDone, why := d.claimRoutineRun(r, forgeEvent, checkCooldown, now)
	if onDone == nil {
		d.recordRoutineSkip(r, trigger, why)
		return false
	}
	d.routines.start(func() {
		defer onDone("", time.Time{})
		_, _ = d.executeRoutineWithHook(ctx, r, trigger, prompt, onDone)
	})
	return true
}

// claimRoutineRun takes a place for a run of r, if its limits leave one,
// and returns an idempotent completion callback that atomically moves the
// slot from starting to active (and commits LastForgeRun for Forge events)
// once the session starts, or releases the slot on failure.
func (d *Daemon) claimRoutineRun(r routines.Routine, forgeEvent, checkCooldown bool, now time.Time) (onDone func(sessionID string, startedAt time.Time), why string) {
	rr := &d.routines
	rr.mu.Lock()
	defer rr.mu.Unlock()
	st := d.routineStateLocked()
	s := st[r.ID]
	if cd := r.CooldownPeriod(); forgeEvent && checkCooldown && cd > 0 && s.LastForgeRun > 0 && now.Sub(time.Unix(s.LastForgeRun, 0)) < cd {
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
	done := false
	return func(sessionID string, startedAt time.Time) {
		rr.mu.Lock()
		defer rr.mu.Unlock()
		if done {
			return
		}
		done = true
		if rr.starting[r.ID]--; rr.starting[r.ID] <= 0 {
			delete(rr.starting, r.ID)
		}
		if sessionID != "" {
			if rr.active == nil {
				rr.active = map[string]map[string]time.Time{}
			}
			if rr.active[r.ID] == nil {
				rr.active[r.ID] = map[string]time.Time{}
			}
			rr.active[r.ID][sessionID] = startedAt
		}
	}, ""
}

// reserveManualRoutineStart tracks a manually triggered routine while its
// session is starting, transitioning it into rr.active once started so
// scheduled/Forge concurrency caps account for manual runs too.
func (d *Daemon) reserveManualRoutineStart(id string) func(sessionID string, startedAt time.Time) {
	rr := &d.routines
	rr.mu.Lock()
	if rr.starting == nil {
		rr.starting = map[string]int{}
	}
	rr.starting[id]++
	rr.mu.Unlock()
	done := false
	return func(sessionID string, startedAt time.Time) {
		rr.mu.Lock()
		defer rr.mu.Unlock()
		if done {
			return
		}
		done = true
		if rr.starting[id]--; rr.starting[id] <= 0 {
			delete(rr.starting, id)
		}
		if sessionID != "" {
			if rr.active == nil {
				rr.active = map[string]map[string]time.Time{}
			}
			if rr.active[id] == nil {
				rr.active[id] = map[string]time.Time{}
			}
			rr.active[id][sessionID] = startedAt
		}
	}
}

// routineAtWorkLocked counts the sessions of routine id at work: those
// being started, those tracked in memory, and those in history (for
// sessions started before a daemon restart) that are still at work, ask
// the user something, or are too new for their status to say. The caller
// holds the runner's lock.
func (d *Daemon) routineAtWorkLocked(id string, now time.Time) int {
	n := d.routines.starting[id]
	seen := map[string]bool{}
	isAtWork := func(sessionID string, startedAt time.Time) (running, atWork bool) {
		s, ok := d.sessions.get(sessionID)
		if !ok {
			return false, false
		}
		info := s.Info()
		if info.GetState() != protocol.StateRunning {
			return false, false
		}
		status := info.GetStatus()
		return true, protocol.AtWork(status) || status == protocol.AgentWaiting || now.Sub(startedAt) < routineWarmup
	}
	if active := d.routines.active[id]; len(active) > 0 {
		for sid, startedAt := range active {
			seen[sid] = true
			running, atWork := isAtWork(sid, startedAt)
			if !running {
				delete(active, sid)
				continue
			}
			if atWork {
				n++
			}
		}
		if len(active) == 0 {
			delete(d.routines.active, id)
		}
	}
	hist, err := routines.LoadHistory(d.dir)
	if err != nil {
		return n
	}
	for _, rec := range hist {
		if rec.RoutineID != id || rec.Status != routineStarted || rec.SessionID == "" || seen[rec.SessionID] {
			continue
		}
		seen[rec.SessionID] = true
		if _, atWork := isAtWork(rec.SessionID, time.Unix(rec.StartedAt, 0)); atWork {
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

// resetRoutineStateLocked clears routine id's Forge cursors immediately so
// disabling and re-enabling before the next poll starts fresh. Caller holds
// the runner's lock.
func (d *Daemon) resetRoutineStateLocked(id string) {
	st := d.routineStateLocked()
	s, ok := st[id]
	if !ok || s.Source == "" {
		return
	}
	st[id] = s.For("")
	d.saveRoutineStateLocked()
}

// deleteRoutineStateLocked removes routine id's persisted trigger state and
// in-memory active session tracking. Caller holds the runner's lock.
func (d *Daemon) deleteRoutineStateLocked(id string) {
	delete(d.routines.active, id)
	st := d.routineStateLocked()
	if _, ok := st[id]; !ok {
		return
	}
	delete(st, id)
	d.saveRoutineStateLocked()
}

// executeRoutine starts an agent session for routine r, given prompt, and
// records the run: the one way a routine runs, whether its schedule, a
// Forge event or a front-end's run action started it.
func (d *Daemon) executeRoutine(ctx context.Context, r routines.Routine, triggerReason, prompt string) (*protocol.SessionInfo, error) {
	return d.executeRoutineWithHook(ctx, r, triggerReason, prompt, nil)
}

func (d *Daemon) executeRoutineWithHook(ctx context.Context, r routines.Routine, triggerReason, prompt string, onStarted func(sessionID string, startedAt time.Time)) (*protocol.SessionInfo, error) {
	startedAt := time.Now()
	rec := routines.RunRecord{
		RoutineID:   r.ID,
		RoutineName: r.Name,
		Trigger:     triggerReason,
		StartedAt:   startedAt.Unix(),
	}
	if st, err := os.Stat(r.Project); err != nil {
		rec.Status = routineError
		rec.Error = fmt.Sprintf("project path unavailable: %v", err)
		d.recordRoutineRun(rec)
		return nil, err
	} else if !st.IsDir() {
		err := fmt.Errorf("project path %q is not a directory", r.Project)
		rec.Status = routineError
		rec.Error = err.Error()
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
	if onStarted != nil {
		onStarted(info.GetId(), startedAt)
	}
	rec.Status = routineStarted
	rec.SessionID = info.GetId()
	d.recordRoutineRun(rec)
	d.log.Info("routines: started session", "routine", r.Name, "session", info.GetId(), "trigger", triggerReason)
	return info, nil
}
