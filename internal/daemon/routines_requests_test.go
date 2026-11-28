package daemon

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"testing"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/routines"
)

func newRoutineDaemon(t *testing.T) *Daemon {
	t.Helper()
	d := New(&config.Config{NodeID: "node"}, slog.New(slog.DiscardHandler))
	d.dir = t.TempDir()
	return d
}

func cronSpec(name, project string) *protocol.Routine {
	return &protocol.Routine{Name: name, Enabled: true, Trigger: string(routines.TriggerCron), Schedule: "0 3 * * *",
		Project: project, Agent: "claude", Prompt: "Run the tests."}
}

// A front-end reads and changes the routines through the daemon's socket,
// as one given -daemon or an external daemon does.
func TestRoutinesAreServedOverTheSocket(t *testing.T) {
	_, c := startTestDaemonForGoal(t)
	spec := cronSpec("Nightly", t.TempDir())
	spec.Enabled = false // the daemon's scheduler runs, and must not start an agent
	reply, err := c.ActRoutine(t.Context(), &protocol.RoutineAction{Op: "add", Spec: spec})
	if err != nil {
		t.Fatal(err)
	}
	if len(reply.GetRoutines()) != 1 || reply.GetRoutines()[0].GetId() == "" {
		t.Fatalf("after add: %v", reply)
	}
	list, err := c.Routines(t.Context(), &protocol.ListRoutines{})
	if err != nil || len(list.GetRoutines()) != 1 || list.GetRoutines()[0].GetName() != "Nightly" {
		t.Fatalf("list = %v, %v", list, err)
	}
}

// Each action changes the one routine it names in the routines the daemon
// has then, so a change never brings back or undoes another.
func TestRoutineActionsChangeOneRoutine(t *testing.T) {
	d := newRoutineDaemon(t)
	ctx := t.Context()
	proj := t.TempDir()
	add := func(name string) string {
		t.Helper()
		reply, err := d.actOnRoutine(ctx, &protocol.RoutineAction{Op: "add", Spec: cronSpec(name, proj)})
		if err != nil {
			t.Fatalf("add %s: %v", name, err)
		}
		list := reply.GetRoutines()
		return list[len(list)-1].GetId()
	}
	a, b := add("A"), add("B")
	if a == b {
		t.Fatal("two routines got the same ID")
	}

	if _, err := d.actOnRoutine(ctx, &protocol.RoutineAction{Op: "disable", Routine: a}); err != nil {
		t.Fatal(err)
	}
	upd := cronSpec("B", proj)
	upd.Prompt = "Run the linters."
	if _, err := d.actOnRoutine(ctx, &protocol.RoutineAction{Op: "update", Routine: b, Spec: upd}); err != nil {
		t.Fatal(err)
	}
	list, err := routines.Load(d.dir)
	if err != nil || len(list) != 2 {
		t.Fatalf("routines = %+v, %v", list, err)
	}
	if list[0].ID != a || list[0].Enabled || list[0].Prompt != "Run the tests." {
		t.Fatalf("A = %+v, want it disabled and otherwise as it was", list[0])
	}
	if list[1].ID != b || !list[1].Enabled || list[1].Prompt != "Run the linters." {
		t.Fatalf("B = %+v, want its prompt changed", list[1])
	}

	if _, err := d.actOnRoutine(ctx, &protocol.RoutineAction{Op: "remove", Routine: "a"}); err != nil {
		t.Fatal(err)
	}
	if list, _ := routines.Load(d.dir); len(list) != 1 || list[0].ID != b {
		t.Fatalf("after removing A: %+v", list)
	}

	for _, bad := range []*protocol.RoutineAction{
		{Op: "add", Spec: cronSpec("relative", "some/dir")},
		{Op: "add", Spec: &protocol.Routine{Name: "x", Trigger: "cron", Schedule: "@every 90s", Project: proj, Agent: "claude", Prompt: "p"}},
		{Op: "update", Routine: "rtn-none", Spec: cronSpec("C", proj)},
		{Op: "remove", Routine: "nothing"},
		{Op: "frobnicate", Routine: b},
	} {
		if _, err := d.actOnRoutine(ctx, bad); err == nil {
			t.Errorf("%v was taken", bad)
		}
	}
	if list, _ := routines.Load(d.dir); len(list) != 1 {
		t.Fatalf("a refused action changed the routines: %+v", list)
	}
}

// Changes and runs recorded at once are all kept.
func TestConcurrentRoutineChangesAreAllKept(t *testing.T) {
	d := newRoutineDaemon(t)
	proj := t.TempDir()
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			if _, err := d.actOnRoutine(t.Context(), &protocol.RoutineAction{Op: "add", Spec: cronSpec(fmt.Sprintf("r%d", i), proj)}); err != nil {
				t.Error(err)
			}
		})
	}
	for i := range 50 {
		wg.Go(func() {
			d.recordRoutineRun(routines.RunRecord{RoutineID: "r", Trigger: fmt.Sprintf("t%d", i), Status: "started"})
		})
	}
	wg.Wait()
	if list, err := routines.Load(d.dir); err != nil || len(list) != 20 {
		t.Fatalf("%d routines kept of 20, %v", len(list), err)
	}
	if hist, err := routines.LoadHistory(d.dir); err != nil || len(hist) != 50 {
		t.Fatalf("%d runs kept of 50, %v", len(hist), err)
	}
}

// A routines.json the daemon cannot read is refused, never written over.
func TestUnreadableRoutinesAreLeftAlone(t *testing.T) {
	d := newRoutineDaemon(t)
	bad := []byte(`[{"id": "rtn-1", "name": `)
	if err := os.WriteFile(routines.FilePath(d.dir), bad, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := d.actOnRoutine(t.Context(), &protocol.RoutineAction{Op: "add", Spec: cronSpec("A", t.TempDir())}); err == nil {
		t.Fatal("add took an unreadable routines.json")
	}
	if got, _ := os.ReadFile(routines.FilePath(d.dir)); !bytes.Equal(got, bad) {
		t.Fatalf("routines.json = %q, want it untouched", got)
	}
	if err := os.WriteFile(routines.HistoryPath(d.dir), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(routines.FilePath(d.dir), []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	reply, err := d.actOnRoutine(t.Context(), &protocol.RoutineAction{Op: "add", Spec: cronSpec("A", t.TempDir())})
	if err != nil || reply.GetRunsError() == "" {
		t.Fatalf("an unreadable history should be said, not fail the reply: %v, %v", reply, err)
	}
}
