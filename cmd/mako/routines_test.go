package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/protocol"
)

// fakeRoutines stands in for the daemon: it keeps the routines, and what
// it was asked.
type fakeRoutines struct {
	list    []*protocol.Routine
	runs    []*protocol.RoutineRun
	actions []*protocol.RoutineAction
	lists   []*protocol.ListRoutines
}

func (f *fakeRoutines) Routines(_ context.Context, req *protocol.ListRoutines) (*protocol.Routines, error) {
	f.lists = append(f.lists, req)
	return &protocol.Routines{Routines: f.list, Runs: f.runs}, nil
}

func (f *fakeRoutines) ActRoutine(_ context.Context, req *protocol.RoutineAction) (*protocol.Routines, error) {
	f.actions = append(f.actions, req)
	reply := &protocol.Routines{}
	switch req.GetOp() {
	case "add":
		r := proto.CloneOf(req.GetSpec())
		r.Id = "rtn-new"
		f.list = append(f.list, r)
	case "run":
		reply.Session = &protocol.SessionInfo{Id: "sess-1"}
	}
	reply.Routines = f.list
	return reply, nil
}

func TestRoutinesCLIAsksTheDaemon(t *testing.T) {
	dir := t.TempDir()
	f := &fakeRoutines{}
	var buf bytes.Buffer
	ctx := t.Context()

	if err := routinesCmd(ctx, f, "node-b", []string{"add",
		"-name", "Nightly lint",
		"-project", dir,
		"-agent", "claude",
		"-schedule", "0 3 * * *",
		"-worktree",
		"-prompt", "Run golangci-lint and summarize warnings.",
	}, &buf); err != nil {
		t.Fatalf("routines add: %v", err)
	}
	if !strings.Contains(buf.String(), "added routine rtn-new (Nightly lint)") {
		t.Fatalf("add said %q", buf.String())
	}
	add := f.actions[0]
	if add.GetOp() != "add" || add.GetNodeId() != "node-b" || add.GetSpec().GetProject() != dir ||
		add.GetSpec().GetSchedule() != "0 3 * * *" || !add.GetSpec().GetWorktree() || !add.GetSpec().GetEnabled() {
		t.Fatalf("add asked %v", add)
	}

	buf.Reset()
	if err := routinesCmd(ctx, f, "", []string{"list"}, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "Nightly lint") || !strings.Contains(buf.String(), "0 3 * * *") {
		t.Fatalf("routines list = %s", buf.String())
	}

	for _, c := range []struct {
		args []string
		op   string
	}{{[]string{"disable", "Nightly lint"}, "disable"}, {[]string{"enable", "rtn-new"}, "enable"}, {[]string{"rm", "rtn-new"}, "remove"}} {
		if err := routinesCmd(ctx, f, "", c.args, &buf); err != nil {
			t.Fatalf("routines %v: %v", c.args, err)
		}
		last := f.actions[len(f.actions)-1]
		if last.GetOp() != c.op || last.GetRoutine() != c.args[1] {
			t.Fatalf("routines %v asked %v", c.args, last)
		}
	}

	buf.Reset()
	if err := routinesCmd(ctx, f, "", []string{"run", "rtn-new"}, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "session sess-1") {
		t.Fatalf("run said %q", buf.String())
	}

	f.runs = []*protocol.RoutineRun{{RoutineName: "Nightly lint", Trigger: "manual", Status: "started", SessionId: "sess-1", Started: protocol.Time(time.Unix(100, 0))}}
	buf.Reset()
	if err := routinesCmd(ctx, f, "", []string{"history", "-limit", "5"}, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "sess-1") || f.lists[len(f.lists)-1].GetRuns() != 5 {
		t.Fatalf("history = %q, asked %v", buf.String(), f.lists[len(f.lists)-1])
	}
}

// A routine that is not valid is refused before the daemon is asked.
func TestRoutinesAddChecksBeforeAsking(t *testing.T) {
	f := &fakeRoutines{}
	var buf bytes.Buffer
	err := routinesCmd(t.Context(), f, "", []string{"add", "-name", "x", "-project", t.TempDir(), "-schedule", "@every 90s", "-prompt", "p"}, &buf)
	if err == nil || len(f.actions) != 0 {
		t.Fatalf("add of @every 90s = %v, asked %d times", err, len(f.actions))
	}
	if err := routinesCmd(t.Context(), f, "", []string{"frobnicate"}, &buf); err == nil {
		t.Fatal("an unknown subcommand was taken")
	}
}
