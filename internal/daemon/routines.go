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
// to. The scheduler's cron ticks and its polls of the forges run apart,
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
}

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
			d.routines.start(func() { _, _ = d.executeRoutine(ctx, r, "cron:"+r.Schedule, "") })
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
		for _, it := range fresh {
			num := it.GetNumber()
			if r.Trigger == routines.TriggerForgePR {
				extra := fmt.Sprintf("Triggered by newly opened PR #%d: %s (%s)", num, it.GetTitle(), it.GetUrl())
				d.routines.start(func() { _, _ = d.executeRoutine(ctx, r, fmt.Sprintf("pr:#%d", num), extra) })
			} else {
				extra := fmt.Sprintf("Triggered by failed CI run #%d: %s (%s)", num, it.GetTitle(), it.GetUrl())
				d.routines.start(func() { _, _ = d.executeRoutine(ctx, r, fmt.Sprintf("ci:#%d", num), extra) })
			}
		}
	}
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
	rr := &d.routines
	rr.mu.Lock()
	defer rr.mu.Unlock()
	if rr.state == nil {
		st, err := routines.LoadState(d.dir)
		if err != nil {
			d.log.Warn("routines: trigger state unreadable, starting it afresh", "err", err)
			st = map[string]routines.TriggerState{}
		}
		rr.state = st
	}
	if change(rr.state) {
		if err := routines.SaveState(d.dir, rr.state); err != nil {
			d.log.Warn("routines: trigger state not saved", "err", err)
		}
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

// executeRoutine starts an agent session for routine r and records the run:
// the one way a routine runs, whether its schedule, a Forge event or a
// front-end's run action started it.
func (d *Daemon) executeRoutine(ctx context.Context, r routines.Routine, triggerReason, extraContext string) (*protocol.SessionInfo, error) {
	rec := routines.RunRecord{
		RoutineID:   r.ID,
		RoutineName: r.Name,
		Trigger:     triggerReason,
		StartedAt:   time.Now().Unix(),
	}
	if _, err := os.Stat(r.Project); err != nil {
		rec.Status = "error"
		rec.Error = fmt.Sprintf("project path unavailable: %v", err)
		d.recordRoutineRun(rec)
		return nil, err
	}

	prompt := r.Prompt
	if strings.TrimSpace(extraContext) != "" {
		prompt = extraContext + "\n\n" + prompt
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
		prefix := strings.TrimSpace(r.BranchPrefix)
		if prefix == "" {
			prefix = "routine/" + r.ID
		}
		req.Worktree = &protocol.Worktree{Branch: fmt.Sprintf("%s-%s", prefix, time.Now().Format("20060102-150405"))}
	}
	runCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	s, err := d.openSession(runCtx, req)
	if err != nil {
		rec.Status = "error"
		rec.Error = err.Error()
		d.recordRoutineRun(rec)
		d.log.Warn("routines: execution failed", "routine", r.Name, "err", err)
		return nil, err
	}
	info := d.withHead(s.Info())
	rec.Status = "started"
	rec.SessionID = info.GetId()
	d.recordRoutineRun(rec)
	d.log.Info("routines: started session", "routine", r.Name, "session", info.GetId(), "trigger", triggerReason)
	return info, nil
}
