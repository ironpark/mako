package daemon

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/forge"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/routines"
)

const (
	routineTickInterval  = 30 * time.Second
	routineForgeInterval = 3 * time.Minute
)

// runRoutines executes enabled cron schedules and Forge event triggers defined
// in routines.json inside the daemon's configuration directory.
func (d *Daemon) runRoutines(ctx context.Context) {
	if d.dir == "" {
		return
	}
	ticker := time.NewTicker(routineTickInterval)
	defer ticker.Stop()

	lastFiredMinute := map[string]int64{} // routineID -> unix minute
	seenPRs := map[string]map[int64]bool{}
	seenFailedRuns := map[string]map[int64]bool{}
	var lastForgePoll time.Time

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			list, err := routines.Load(d.dir)
			if err != nil {
				d.log.Warn("routines: load failed", "err", err)
				continue
			}
			if len(list) == 0 {
				continue
			}
			d.tickCronRoutines(ctx, list, now, lastFiredMinute)
			if time.Since(lastForgePoll) >= routineForgeInterval {
				lastForgePoll = now
				d.tickForgeRoutines(ctx, list, seenPRs, seenFailedRuns)
			}
		}
	}
}

func (d *Daemon) tickCronRoutines(ctx context.Context, list []routines.Routine, now time.Time, lastFired map[string]int64) {
	minKey := now.Unix() / 60
	for _, r := range list {
		if !r.Enabled || r.Trigger != routines.TriggerCron {
			continue
		}
		if lastFired[r.ID] == minKey {
			continue
		}
		sched, err := routines.ParseSchedule(r.Schedule)
		if err != nil {
			continue
		}
		if !sched.Matches(now) {
			continue
		}
		lastFired[r.ID] = minKey
		go d.executeRoutine(ctx, r, "cron:"+r.Schedule, "")
	}
}

func (d *Daemon) tickForgeRoutines(ctx context.Context, list []routines.Routine, seenPRs, seenFailed map[string]map[int64]bool) {
	for _, r := range list {
		if !r.Enabled || r.Project == "" {
			continue
		}
		switch r.Trigger {
		case routines.TriggerForgePR:
			pollCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			items, err := forge.List(pollCtx, &protocol.ForgeList{Root: r.Project, Kind: protocol.ForgePR, State: "open", Max: 20})
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
				go d.executeRoutine(ctx, r, fmt.Sprintf("pr:#%d", num), extra)
			}
		case routines.TriggerForgeCIFail:
			pollCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			items, err := forge.List(pollCtx, &protocol.ForgeList{Root: r.Project, Kind: protocol.ForgeRuns, Max: 20})
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
				go d.executeRoutine(ctx, r, fmt.Sprintf("ci:#%d", num), extra)
			}
		}
	}
}

// executeRoutine starts an agent session for routine r and persists a RunRecord.
func (d *Daemon) executeRoutine(ctx context.Context, r routines.Routine, triggerReason, extraContext string) (*protocol.SessionInfo, error) {
	if _, err := os.Stat(r.Project); err != nil {
		rec := routines.RunRecord{
			RoutineID:   r.ID,
			RoutineName: r.Name,
			Trigger:     triggerReason,
			Status:      "error",
			Error:       fmt.Sprintf("project path unavailable: %v", err),
			StartedAt:   time.Now().Unix(),
		}
		_ = routines.AppendHistory(d.dir, rec)
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
	rec := routines.RunRecord{
		RoutineID:   r.ID,
		RoutineName: r.Name,
		Trigger:     triggerReason,
		StartedAt:   time.Now().Unix(),
	}
	if err != nil {
		rec.Status = "error"
		rec.Error = err.Error()
		_ = routines.AppendHistory(d.dir, rec)
		d.log.Warn("routines: execution failed", "routine", r.Name, "err", err)
		return nil, err
	}
	info := s.Info()
	rec.Status = "started"
	rec.SessionID = info.GetId()
	_ = routines.AppendHistory(d.dir, rec)
	d.log.Info("routines: started session", "routine", r.Name, "session", info.GetId(), "trigger", triggerReason)
	return info, nil
}
