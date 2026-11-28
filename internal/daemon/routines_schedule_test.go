package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/config"
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

// fakeForge answers each poll with the items it holds then.
type fakeForge struct {
	mu    sync.Mutex
	items []*protocol.ForgeItem
	asked []*protocol.ForgeList
}

func (f *fakeForge) set(items ...*protocol.ForgeItem) {
	f.mu.Lock()
	f.items = items
	f.mu.Unlock()
}

func (f *fakeForge) list(_ context.Context, req *protocol.ForgeList) (*protocol.ForgeItems, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, req)
	return &protocol.ForgeItems{Forge: "github", Items: f.items}, nil
}

func pr(n int64) *protocol.ForgeItem {
	return &protocol.ForgeItem{Kind: protocol.ForgePR, Number: n, State: "OPEN", Title: fmt.Sprintf("PR %d", n), Association: "MEMBER"}
}

func ciRun(n int64, conclusion string) *protocol.ForgeItem {
	return &protocol.ForgeItem{Kind: protocol.ForgeRuns, Number: n, State: "completed", Checks: conclusion, Title: fmt.Sprintf("run %d", n)}
}

// forgeDaemon is a daemon of dir whose forge is f, its routines' projects
// missing, so each run they start is recorded as an error at once.
func forgeDaemon(dir string, f *fakeForge) *Daemon {
	d := New(&config.Config{NodeID: "node"}, slog.New(slog.DiscardHandler))
	d.dir = dir
	d.routines.forgeList = f.list
	return d
}

// firedTriggers are the triggers of the runs recorded, sorted, once the
// runs started are done: they run at once, and are recorded as they end.
func firedTriggers(t *testing.T, d *Daemon) []string {
	t.Helper()
	d.routines.runs.Wait()
	hist, err := routines.LoadHistory(d.dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, rec := range hist {
		out = append(out, rec.Trigger)
	}
	slices.Sort(out)
	return out
}

func forgeRoutine(id string, trigger routines.TriggerKind) routines.Routine {
	return routines.Routine{ID: id, Name: id, Enabled: true, Trigger: trigger, Project: "/nonexistent/mako-routine-test", Agent: "claude", Prompt: "p"}
}

// A pull request runs the routine when it is new, by its number, not when
// an older one is active again; and a restart neither runs it again nor
// misses one opened while the daemon was down.
func TestNewPullRequestsAreToldByNumberAcrossRestarts(t *testing.T) {
	dir := t.TempDir()
	f := &fakeForge{}
	d := forgeDaemon(dir, f)
	r := forgeRoutine("rtn-pr", routines.TriggerForgePR)
	saveRoutines(t, d, r)
	list := []routines.Routine{r}

	f.set(pr(12), pr(11), pr(5))
	d.tickForgeRoutines(t.Context(), list) // the first poll runs nothing
	if got := firedTriggers(t, d); len(got) != 0 {
		t.Fatalf("the first poll ran %v", got)
	}
	if a := f.asked[0]; a.GetOrder() != protocol.ForgeOrderCreated || a.GetState() != "all" {
		t.Fatalf("polled with %v; want all of them, the newest opened first", a)
	}
	f.set(pr(5), pr(13), pr(12), pr(11)) // 5 commented on: active, not new
	d.tickForgeRoutines(t.Context(), list)
	if got := firedTriggers(t, d); !slices.Equal(got, []string{"pr:#13"}) {
		t.Fatalf("ran %v; want pr:#13 alone", got)
	}

	// The daemon restarts; #14 is opened while it is down.
	d2 := forgeDaemon(dir, f)
	f.set(pr(14), pr(13), pr(12), pr(5))
	d2.tickForgeRoutines(t.Context(), list)
	if got := firedTriggers(t, d2); !slices.Equal(got, []string{"pr:#13", "pr:#14"}) {
		t.Fatalf("after a restart ran %v; want pr:#14 added", got)
	}

	// One opened and merged between two polls runs nothing, and moves the
	// watermark on.
	merged := pr(15)
	merged.State = "MERGED"
	f.set(merged, pr(14))
	d2.tickForgeRoutines(t.Context(), list)
	if st, _ := routines.LoadState(dir); st["rtn-pr"].PRWatermark != 15 {
		t.Fatalf("watermark = %d; want 15", st["rtn-pr"].PRWatermark)
	}
	if got := firedTriggers(t, d2); len(got) != 2 {
		t.Fatalf("a merged pull request ran the routine: %v", got)
	}
}

// Every failed conclusion runs a CI routine, once, and a restart does not
// run it again.
func TestFailedRunsOfEveryConclusionRunOnce(t *testing.T) {
	dir := t.TempDir()
	f := &fakeForge{}
	d := forgeDaemon(dir, f)
	r := forgeRoutine("rtn-ci", routines.TriggerForgeCIFail)
	saveRoutines(t, d, r)
	list := []routines.Routine{r}

	f.set(ciRun(100, "failure"), ciRun(99, "success"))
	d.tickForgeRoutines(t.Context(), list)
	f.set(ciRun(105, "timed_out"), ciRun(104, "startup_failure"), ciRun(103, "success"), ciRun(102, ""), ciRun(101, "cancelled"), ciRun(100, "failure"))
	d.tickForgeRoutines(t.Context(), list)
	want := []string{"ci:#101", "ci:#104", "ci:#105"}
	if got := firedTriggers(t, d); !slices.Equal(got, want) {
		t.Fatalf("ran %v; want %v", got, want)
	}
	d2 := forgeDaemon(dir, f)
	run102 := ciRun(102, "failure") // failed since, late
	f.set(ciRun(105, "timed_out"), run102, ciRun(101, "cancelled"))
	d2.tickForgeRoutines(t.Context(), list)
	if got := firedTriggers(t, d2); !slices.Equal(got, []string{"ci:#101", "ci:#102", "ci:#104", "ci:#105"}) {
		t.Fatalf("after a restart ran %v; want ci:#102 added alone", got)
	}
}

// A routine run by its schedule is not run again in the same minute by a
// daemon started anew.
func TestACronMinuteSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	r := routines.Routine{ID: "rtn-on", Name: "on", Enabled: true, Trigger: routines.TriggerCron, Schedule: "0 9 * * *", Project: "/nonexistent/x", Agent: "claude", Prompt: "p"}
	at := time.Date(2026, 11, 28, 9, 0, 5, 0, time.Local)
	d := forgeDaemon(dir, &fakeForge{})
	saveRoutines(t, d, r)
	d.tickCronRoutines(t.Context(), at)
	d.routines.runs.Wait()
	d2 := forgeDaemon(dir, &fakeForge{})
	d2.tickCronRoutines(t.Context(), at.Add(40*time.Second))
	if got := firedTriggers(t, d2); len(got) != 1 {
		t.Fatalf("ran %v; want once", got)
	}
}

// A routine disabled and enabled again runs for what comes after, not for
// what came while it was off.
func TestARoutineEnabledAgainStartsAfresh(t *testing.T) {
	dir := t.TempDir()
	f := &fakeForge{}
	d := forgeDaemon(dir, f)
	r := forgeRoutine("rtn-pr", routines.TriggerForgePR)
	f.set(pr(3))
	d.tickForgeRoutines(t.Context(), []routines.Routine{r})
	off := r
	off.Enabled = false
	d.tickForgeRoutines(t.Context(), []routines.Routine{off})
	f.set(pr(9), pr(8), pr(3))
	d.tickForgeRoutines(t.Context(), []routines.Routine{r})
	if got := firedTriggers(t, d); len(got) != 0 {
		t.Fatalf("ran %v for pull requests opened while it was off", got)
	}
}
