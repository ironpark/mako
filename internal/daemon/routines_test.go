//go:build unix

package daemon

import (
	"log/slog"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/routines"
)

func TestDaemonExecuteRoutineRecordsHistory(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	command, root := fakeAgent(t, "claude")
	cfg := &config.Config{
		NodeID: "node",
		Agents: map[string]agentspec.Agent{
			"claude": {
				Command: []string{command},
				Env: map[string]string{
					"MAKO_FAKE_AGENT":   "claude",
					"MAKO_FAKE_ROOT":    root,
					"CLAUDE_CONFIG_DIR": root,
				},
			},
		},
	}
	d := New(cfg, slog.New(slog.DiscardHandler))
	d.dir, d.ctx, d.mcp, d.hookBinary = t.TempDir(), t.Context(), nil, "/bin/makod"
	if err := d.loadSessions(d.dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.sessions.closeAll)

	proj := t.TempDir()
	r := routines.Routine{
		ID:       "rtn-test",
		Name:     "Daily checks",
		Enabled:  true,
		Trigger:  routines.TriggerCron,
		Schedule: "0 9 * * *",
		Project:  proj,
		Agent:    "claude",
		Prompt:   "Run tests and report",
	}
	info, err := d.executeRoutine(t.Context(), r, "cron:0 9 * * *", "")
	if err != nil {
		t.Fatalf("executeRoutine: %v", err)
	}
	if info.GetName() != "Routine: Daily checks" {
		t.Fatalf("session name = %q", info.GetName())
	}

	hist, err := routines.LoadHistory(d.dir)
	if err != nil || len(hist) != 1 {
		t.Fatalf("history = %+v, err=%v", hist, err)
	}
	if hist[0].Status != "started" || hist[0].SessionID != info.GetId() || hist[0].RoutineID != r.ID {
		t.Fatalf("unexpected history record: %+v", hist[0])
	}

	// A front-end's run action takes the same path, and says the session.
	added, err := d.actOnRoutine(t.Context(), &protocol.RoutineAction{Op: "add", Spec: routineProto(r)})
	if err != nil {
		t.Fatal(err)
	}
	id := added.GetRoutines()[0].GetId()
	ran, err := d.actOnRoutine(t.Context(), &protocol.RoutineAction{Op: "run", Routine: id})
	if err != nil {
		t.Fatalf("run action: %v", err)
	}
	if ran.GetSession().GetName() != "Routine: Daily checks" || len(ran.GetRuns()) != 2 ||
		ran.GetRuns()[0].GetTrigger() != "manual" || ran.GetRuns()[0].GetSessionId() != ran.GetSession().GetId() {
		t.Fatalf("run action replied %v", ran)
	}

	// Verify tickCronRoutines deduplicates within the same minute.
	fired := map[string]int64{}
	now := time.Date(2026, 11, 28, 9, 0, 10, 0, time.UTC)
	d.tickCronRoutines(t.Context(), []routines.Routine{{ID: "r1", Name: "x", Enabled: false, Trigger: routines.TriggerCron, Schedule: "0 9 * * *"}}, now, fired)
	if len(fired) != 0 {
		t.Fatalf("disabled routine should not fire")
	}
}
