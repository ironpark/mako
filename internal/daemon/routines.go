package daemon

import (
	"cmp"
	"context"
	"fmt"
	"os"
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

	lastFired map[string]int64 // routine ID: the unix minute it last ran by its schedule; the cron ticks'
	// What each Forge routine's polls have seen, by routine ID; the poll's
	// alone, which never runs twice at once.
	seenPRs, seenFailed map[string]map[int64]bool
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
	rr := &d.routines
	if rr.lastFired == nil {
		rr.lastFired = map[string]int64{}
	}
	minKey := now.Unix() / 60
	for _, r := range list {
		if !r.Enabled || r.Trigger != routines.TriggerCron {
			continue
		}
		if rr.lastFired[r.ID] == minKey {
			continue
		}
		sched, err := routines.ParseSchedule(r.Schedule)
		if err != nil {
			continue
		}
		if !sched.Matches(now) {
			continue
		}
		rr.lastFired[r.ID] = minKey
		rr.start(func() { _, _ = d.executeRoutine(ctx, r, "cron:"+r.Schedule, "") })
	}
}

func (d *Daemon) tickForgeRoutines(ctx context.Context, list []routines.Routine) {
	rr := &d.routines
	if rr.seenPRs == nil {
		rr.seenPRs, rr.seenFailed = map[string]map[int64]bool{}, map[string]map[int64]bool{}
	}
	seenPRs, seenFailed := rr.seenPRs, rr.seenFailed
	for _, r := range list {
		if !r.Enabled || r.Project == "" {
			continue
		}
		switch r.Trigger {
		case routines.TriggerForgePR:
			pollCtx, cancel := context.WithTimeout(ctx, routineForgeTimeout)
			items, err := rr.list(pollCtx, &protocol.ForgeList{Root: r.Project, Kind: protocol.ForgePR, State: "open", Max: 20})
			cancel()
			if err != nil {
				continue
			}
			known, initialized := seenPRs[r.ID]
			if !initialized {
				known = map[int64]bool{}
				for _, it := range items.GetItems() {
					known[it.GetNumber()] = true
				}
				seenPRs[r.ID] = known
				continue
			}
			for _, it := range items.GetItems() {
				num := it.GetNumber()
				if known[num] {
					continue
				}
				known[num] = true
				extra := fmt.Sprintf("Triggered by newly opened PR #%d: %s (%s)", num, it.GetTitle(), it.GetUrl())
				rr.start(func() { _, _ = d.executeRoutine(ctx, r, fmt.Sprintf("pr:#%d", num), extra) })
			}
		case routines.TriggerForgeCIFail:
			pollCtx, cancel := context.WithTimeout(ctx, routineForgeTimeout)
			items, err := rr.list(pollCtx, &protocol.ForgeList{Root: r.Project, Kind: protocol.ForgeRuns, Max: 20})
			cancel()
			if err != nil {
				continue
			}
			known, initialized := seenFailed[r.ID]
			if !initialized {
				known = map[int64]bool{}
				for _, it := range items.GetItems() {
					if strings.EqualFold(it.GetChecks(), "failure") {
						known[it.GetNumber()] = true
					}
				}
				seenFailed[r.ID] = known
				continue
			}
			for _, it := range items.GetItems() {
				if !strings.EqualFold(it.GetChecks(), "failure") {
					continue
				}
				num := it.GetNumber()
				if known[num] {
					continue
				}
				known[num] = true
				extra := fmt.Sprintf("Triggered by failed CI run #%d: %s (%s)", num, it.GetTitle(), it.GetUrl())
				rr.start(func() { _, _ = d.executeRoutine(ctx, r, fmt.Sprintf("ci:#%d", num), extra) })
			}
		}
	}
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
