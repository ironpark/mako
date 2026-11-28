package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/routines"
)

// routineClient is what the routines commands ask of the daemon: a
// client.Client, or a test's stand-in.
type routineClient interface {
	Routines(ctx context.Context, req *protocol.ListRoutines) (*protocol.Routines, error)
	ActRoutine(ctx context.Context, req *protocol.RoutineAction) (*protocol.Routines, error)
}

// routinesCmd reads and changes the routines of the daemon the CLI talks
// to, or of the node nodeID names, through that daemon, which alone writes
// its routines' files.
func routinesCmd(ctx context.Context, c routineClient, nodeID string, args []string, out io.Writer) error {
	sub := "list"
	var rest []string
	if len(args) > 0 {
		sub = args[0]
		rest = args[1:]
	}
	act := func(op, routine string, spec *protocol.Routine) (*protocol.Routines, error) {
		return c.ActRoutine(ctx, &protocol.RoutineAction{NodeId: nodeID, Op: op, Routine: routine, Spec: spec, Runs: 1})
	}
	needOne := func() (string, error) {
		if len(rest) < 1 {
			return "", fmt.Errorf("routines %s needs a routine ID or name", sub)
		}
		return rest[0], nil
	}
	switch sub {
	case "list", "ls":
		reply, err := c.Routines(ctx, &protocol.ListRoutines{NodeId: nodeID, Runs: 1})
		if err != nil {
			return err
		}
		return printRoutines(out, reply.GetRoutines())
	case "add":
		spec, err := routineSpec(rest)
		if err != nil {
			return err
		}
		reply, err := act("add", "", spec)
		if err != nil {
			return err
		}
		// The routine added comes last.
		if list := reply.GetRoutines(); len(list) > 0 {
			r := list[len(list)-1]
			fmt.Fprintf(out, "added routine %s (%s)\n", r.GetId(), r.GetName())
		}
		return nil
	case "remove", "rm":
		which, err := needOne()
		if err != nil {
			return err
		}
		if _, err := act("remove", which, nil); err != nil {
			return err
		}
		fmt.Fprintf(out, "removed routine %s\n", which)
		return nil
	case "enable", "disable":
		which, err := needOne()
		if err != nil {
			return err
		}
		if _, err := act(sub, which, nil); err != nil {
			return err
		}
		fmt.Fprintf(out, "%sd routine %s\n", sub, which)
		return nil
	case "history":
		fs := flag.NewFlagSet("routines history", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		limit := fs.Int("limit", 20, "max records")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		reply, err := c.Routines(ctx, &protocol.ListRoutines{NodeId: nodeID, Runs: uint32(max(*limit, 0))})
		if err != nil {
			return err
		}
		if e := reply.GetRunsError(); e != "" {
			return fmt.Errorf("routine history: %s", e)
		}
		return printRoutineRuns(out, reply.GetRuns())
	case "run":
		which, err := needOne()
		if err != nil {
			return err
		}
		reply, err := act("run", which, nil)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "started routine %s as session %s\n", which, reply.GetSession().GetId())
		return nil
	default:
		return fmt.Errorf("unknown routines subcommand %q (expected list, add, remove, enable, disable, run, history)", sub)
	}
}

// routineSpec is the routine routines add's flags describe; its project
// is made absolute against this folder, the CLI's.
func routineSpec(args []string) (*protocol.Routine, error) {
	fs := flag.NewFlagSet("routines add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	name := fs.String("name", "", "routine name")
	project := fs.String("project", "", "project folder path (default: cwd)")
	agent := fs.String("agent", "claude", "agent or agent:profile")
	trigger := fs.String("trigger", string(routines.TriggerCron), "trigger kind: cron, forge_pr_opened, forge_ci_failed")
	schedule := fs.String("schedule", "", "cron expression or @hourly/@daily/@every 30m")
	prompt := fs.String("prompt", "", "prompt text, typed into the agent as it is (//template calls are not expanded)")
	worktree := fs.Bool("worktree", false, "run each execution in a new git worktree")
	branchPrefix := fs.String("branch-prefix", "", "optional worktree branch prefix")
	disabled := fs.Bool("disabled", false, "create the routine disabled")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	proj := strings.TrimSpace(*project)
	if proj == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		proj = cwd
	}
	abs, err := filepath.Abs(proj)
	if err != nil {
		return nil, err
	}
	r := routines.Routine{
		Name:         *name,
		Enabled:      !*disabled,
		Trigger:      routines.TriggerKind(strings.TrimSpace(*trigger)),
		Schedule:     *schedule,
		Project:      abs,
		Agent:        *agent,
		Worktree:     *worktree,
		BranchPrefix: *branchPrefix,
		Prompt:       *prompt,
	}.Normalized()
	// Checked here too, so a mistake is told before the daemon is asked.
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return &protocol.Routine{
		Name: r.Name, Enabled: r.Enabled, Trigger: string(r.Trigger), Schedule: r.Schedule, Project: r.Project,
		Agent: r.Agent, Worktree: r.Worktree, BranchPrefix: r.BranchPrefix, Prompt: r.Prompt,
	}, nil
}

func printRoutines(out io.Writer, list []*protocol.Routine) error {
	if len(list) == 0 {
		fmt.Fprintln(out, "no routines configured")
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tENABLED\tTRIGGER\tAGENT\tPROJECT\tNEXT")
	now := time.Now()
	for _, r := range list {
		trig := r.GetTrigger()
		nextStr := "-"
		if routines.TriggerKind(trig) == routines.TriggerCron {
			trig = r.GetSchedule()
			if sched, err := routines.ParseSchedule(r.GetSchedule()); err == nil {
				if next := sched.Next(now); !next.IsZero() {
					nextStr = next.Local().Format("2006-01-02 15:04")
				}
			}
		}
		enabled := "no"
		if r.GetEnabled() {
			enabled = "yes"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.GetId(), r.GetName(), enabled, trig, r.GetAgent(), r.GetProject(), nextStr)
	}
	return w.Flush()
}

func printRoutineRuns(out io.Writer, runs []*protocol.RoutineRun) error {
	if len(runs) == 0 {
		fmt.Fprintln(out, "no routine runs yet")
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TIME\tROUTINE\tTRIGGER\tSTATUS\tSESSION\tERROR")
	for _, rec := range runs {
		ts := rec.GetStarted().AsTime().Local().Format("2006-01-02 15:04:05")
		sid := rec.GetSessionId()
		if sid == "" {
			sid = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", ts, rec.GetRoutineName(), rec.GetTrigger(), rec.GetStatus(), sid, rec.GetError())
	}
	return w.Flush()
}
