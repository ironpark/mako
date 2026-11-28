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

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/routines"
)

func routinesCmd(args []string, daemonAddr string) error {
	dir, err := config.Dir()
	if err != nil {
		return err
	}
	sub := "list"
	var rest []string
	if len(args) > 0 {
		sub = args[0]
		rest = args[1:]
	}
	switch sub {
	case "list", "ls":
		return routinesList(dir, os.Stdout)
	case "add":
		return routinesAdd(dir, rest, os.Stdout)
	case "remove", "rm":
		if len(rest) < 1 {
			return fmt.Errorf("routines remove needs a routine ID or name")
		}
		return routinesRemove(dir, rest[0], os.Stdout)
	case "enable", "disable":
		if len(rest) < 1 {
			return fmt.Errorf("routines %s needs a routine ID or name", sub)
		}
		return routinesSetEnabled(dir, rest[0], sub == "enable", os.Stdout)
	case "history":
		return routinesHistory(dir, rest, os.Stdout)
	case "run":
		if len(rest) < 1 {
			return fmt.Errorf("routines run needs a routine ID or name")
		}
		return routinesRunNow(dir, daemonAddr, rest[0], os.Stdout)
	default:
		return fmt.Errorf("unknown routines subcommand %q (expected list, add, remove, enable, disable, run, history)", sub)
	}
}

func routinesList(dir string, out io.Writer) error {
	list, err := routines.Load(dir)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Fprintln(out, "no routines configured")
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tENABLED\tTRIGGER\tAGENT\tPROJECT\tNEXT")
	now := time.Now()
	for _, r := range list {
		trig := string(r.Trigger)
		nextStr := "-"
		if r.Trigger == routines.TriggerCron {
			trig = r.Schedule
			if sched, err := routines.ParseSchedule(r.Schedule); err == nil {
				if next := sched.Next(now); !next.IsZero() {
					nextStr = next.Local().Format("2006-01-02 15:04")
				}
			}
		}
		enabled := "no"
		if r.Enabled {
			enabled = "yes"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.Name, enabled, trig, r.Agent, r.Project, nextStr)
	}
	return w.Flush()
}

func routinesAdd(dir string, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("routines add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	name := fs.String("name", "", "routine name")
	project := fs.String("project", "", "project folder path (default: cwd)")
	agent := fs.String("agent", "claude", "agent or agent:profile")
	trigger := fs.String("trigger", string(routines.TriggerCron), "trigger kind: cron, forge_pr_opened, forge_ci_failed")
	schedule := fs.String("schedule", "", "cron expression or @hourly/@daily/@every 30m")
	prompt := fs.String("prompt", "", "prompt or //template call")
	worktree := fs.Bool("worktree", false, "run each execution in a new git worktree")
	branchPrefix := fs.String("branch-prefix", "", "optional worktree branch prefix")
	disabled := fs.Bool("disabled", false, "create the routine disabled")
	if err := fs.Parse(args); err != nil {
		return err
	}
	proj := strings.TrimSpace(*project)
	if proj == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		proj = cwd
	}
	abs, err := filepath.Abs(proj)
	if err != nil {
		return err
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
	if err := r.Validate(); err != nil {
		return err
	}
	list, err := routines.Load(dir)
	if err != nil {
		return err
	}
	list = append(list, r)
	if err := routines.Save(dir, list); err != nil {
		return err
	}
	fmt.Fprintf(out, "added routine %s (%s)\n", r.ID, r.Name)
	return nil
}

func routinesRemove(dir, idOrName string, out io.Writer) error {
	list, err := routines.Load(dir)
	if err != nil {
		return err
	}
	idx, ok := routines.Find(list, idOrName)
	if !ok {
		return fmt.Errorf("routine %q not found", idOrName)
	}
	removed := list[idx]
	list = append(list[:idx], list[idx+1:]...)
	if err := routines.Save(dir, list); err != nil {
		return err
	}
	fmt.Fprintf(out, "removed routine %s (%s)\n", removed.ID, removed.Name)
	return nil
}

func routinesSetEnabled(dir, idOrName string, enabled bool, out io.Writer) error {
	list, err := routines.Load(dir)
	if err != nil {
		return err
	}
	idx, ok := routines.Find(list, idOrName)
	if !ok {
		return fmt.Errorf("routine %q not found", idOrName)
	}
	list[idx].Enabled = enabled
	if err := routines.Save(dir, list); err != nil {
		return err
	}
	state := "disabled"
	if enabled {
		state = "enabled"
	}
	fmt.Fprintf(out, "%s routine %s (%s)\n", state, list[idx].ID, list[idx].Name)
	return nil
}

func routinesHistory(dir string, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("routines history", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	limit := fs.Int("limit", 20, "max records")
	if err := fs.Parse(args); err != nil {
		return err
	}
	hist, err := routines.LoadHistory(dir)
	if err != nil {
		return err
	}
	if len(hist) == 0 {
		fmt.Fprintln(out, "no routine runs yet")
		return nil
	}
	if *limit > 0 && len(hist) > *limit {
		hist = hist[:*limit]
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TIME\tROUTINE\tTRIGGER\tSTATUS\tSESSION\tERROR")
	for _, rec := range hist {
		ts := time.Unix(rec.StartedAt, 0).Local().Format("2006-01-02 15:04:05")
		sid := rec.SessionID
		if sid == "" {
			sid = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", ts, rec.RoutineName, rec.Trigger, rec.Status, sid, rec.Error)
	}
	return w.Flush()
}

func routinesRunNow(dir, daemonAddr, idOrName string, out io.Writer) error {
	list, err := routines.Load(dir)
	if err != nil {
		return err
	}
	idx, ok := routines.Find(list, idOrName)
	if !ok {
		return fmt.Errorf("routine %q not found", idOrName)
	}
	r := list[idx]
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := client.Dial(ctx, daemonAddr)
	if err != nil {
		return fmt.Errorf("cannot reach daemon (is makod running?): %w", err)
	}
	defer c.Close()

	agentName, profile := agentspec.SplitTarget(r.Agent)
	req := &protocol.OpenSession{
		Agent:   agentName,
		Profile: profile,
		Project: r.Project,
		Name:    "Routine: " + r.Name,
		Prompt:  r.Prompt,
		Cols:    120,
		Rows:    36,
	}
	if r.Worktree {
		prefix := r.BranchPrefix
		if prefix == "" {
			prefix = "routine/" + r.ID
		}
		req.Worktree = &protocol.Worktree{Branch: fmt.Sprintf("%s-%s", prefix, time.Now().Format("20060102-150405"))}
	}
	info, err := c.Open(ctx, req)
	rec := routines.RunRecord{
		RoutineID:   r.ID,
		RoutineName: r.Name,
		Trigger:     "manual",
		StartedAt:   time.Now().Unix(),
	}
	if err != nil {
		rec.Status = "error"
		rec.Error = err.Error()
		_ = routines.AppendHistory(dir, rec)
		return err
	}
	rec.Status = "started"
	rec.SessionID = info.GetId()
	_ = routines.AppendHistory(dir, rec)
	fmt.Fprintf(out, "started routine %s as session %s\n", r.Name, info.GetId())
	return nil
}
