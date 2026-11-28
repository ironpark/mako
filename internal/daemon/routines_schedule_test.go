package daemon

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/routines"
)

func saveRoutines(t *testing.T, d *Daemon, list ...routines.Routine) {
	t.Helper()
	if err := routines.Save(d.dir, list); err != nil {
		t.Fatal(err)
	}
}

// A forge that does not answer holds up neither the cron ticks nor another
// poll, and the daemon's shutdown waits for the poll it started.
func TestAStalledForgePollHoldsUpNothing(t *testing.T) {
	d := newRoutineDaemon(t)
	saveRoutines(t, d,
		routines.Routine{ID: "rtn-pr", Name: "PRs", Enabled: true, Trigger: routines.TriggerForgePR, Project: t.TempDir(), Agent: "claude", Prompt: "Review it."},
		routines.Routine{ID: "rtn-cron", Name: "Every minute", Enabled: true, Trigger: routines.TriggerCron, Schedule: "* * * * *",
			Project: "/nonexistent/mako-routine-test", Agent: "claude", Prompt: "p"},
	)
	var calls, returned atomic.Int32
	polled := make(chan struct{}, 8)
	d.routines.cronEvery, d.routines.forgeEvery = 10*time.Millisecond, 5*time.Millisecond
	d.routines.forgeList = func(ctx context.Context, _ *protocol.ForgeList) (*protocol.ForgeItems, error) {
		calls.Add(1)
		polled <- struct{}{}
		<-ctx.Done() // a forge that never answers
		returned.Add(1)
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.runRoutines(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	select {
	case <-polled:
	case <-time.After(5 * time.Second):
		t.Fatal("the forge was not polled")
	}
	// The cron routine still runs, though the poll is stuck: its run,
	// failing for its missing project, is recorded.
	deadline := time.Now().Add(5 * time.Second)
	for {
		hist, _ := routines.LoadHistory(d.dir)
		if len(hist) > 0 && hist[0].RoutineID == "rtn-cron" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the cron routine did not run while a forge poll was stuck")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("the forge was polled %d times while its first poll was under way", n)
	}
	cancel()
	<-done
	if returned.Load() != 1 {
		t.Fatal("runRoutines returned before the poll it started")
	}
}

// A schedule runs a routine once in its minute, however many ticks fall in it.
func TestACronRoutineRunsOnceAMinute(t *testing.T) {
	d := newRoutineDaemon(t)
	saveRoutines(t, d,
		routines.Routine{ID: "rtn-on", Name: "on", Enabled: true, Trigger: routines.TriggerCron, Schedule: "0 9 * * *", Project: "/nonexistent/x", Agent: "claude", Prompt: "p"},
		routines.Routine{ID: "rtn-off", Name: "off", Enabled: false, Trigger: routines.TriggerCron, Schedule: "0 9 * * *", Project: "/nonexistent/x", Agent: "claude", Prompt: "p"},
	)
	at := time.Date(2026, 11, 28, 9, 0, 10, 0, time.Local)
	d.tickCronRoutines(t.Context(), at)
	d.tickCronRoutines(t.Context(), at.Add(30*time.Second))
	d.tickCronRoutines(t.Context(), at.Add(time.Minute)) // 9:01 does not match
	d.routines.runs.Wait()
	hist, err := routines.LoadHistory(d.dir)
	if err != nil || len(hist) != 1 || hist[0].RoutineID != "rtn-on" {
		t.Fatalf("history = %+v, %v; want one run of rtn-on", hist, err)
	}
}
