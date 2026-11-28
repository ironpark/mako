package gui

import (
	"cmp"
	"context"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"
	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/routines"
)

var routineTriggerLabels = map[string]string{
	string(routines.TriggerCron):        "Cron schedule",
	string(routines.TriggerForgePR):     "New pull request",
	string(routines.TriggerForgeCIFail): "Failed CI run",
}

// routineRunsShown is how many of the latest runs the page lists.
const routineRunsShown = 10

// routineSettings lists and changes the routines of the daemon the window
// talks to. The daemon keeps them: the page reads them from it, and each
// change is one request that the daemon makes to the routines it has then,
// so the page never writes a copy it read earlier over another's change.
func routineSettings(m *model) ggui.Widget {
	sameRoutines := func(a, b []*protocol.Routine) bool {
		return slices.EqualFunc(a, b, func(x, y *protocol.Routine) bool { return proto.Equal(x, y) })
	}
	sameRuns := func(a, b []*protocol.RoutineRun) bool {
		return slices.EqualFunc(a, b, func(x, y *protocol.RoutineRun) bool { return proto.Equal(x, y) })
	}
	items := ggui.State([]*protocol.Routine(nil)).WithEqual(sameRoutines)
	runs := ggui.State([]*protocol.RoutineRun(nil)).WithEqual(sameRuns)
	loaded := ggui.State(false)
	errText := ggui.State("")
	runsErr := ggui.State("")

	name := ggui.State("")
	project := ggui.State(m.launchProject(ggui.Peek(m.Conn).Self.GetId()))
	agentTarget := ggui.State(cmp.Or(ggui.Peek(m.Prefs).DefaultAgent, "claude"))
	trigger := ggui.State(string(routines.TriggerCron))
	schedule := ggui.State("0 9 * * 1-5")
	worktree := ggui.State(true)
	prompt := ggui.State("")
	allowExternal := ggui.State(false)
	maxConcurrent := ggui.State(strconv.Itoa(routines.DefaultMaxConcurrent))
	maxPerPoll := ggui.State(strconv.Itoa(routines.DefaultMaxPerPoll))
	cooldown := ggui.State("10m")

	targets := ggui.Derived(func() []string {
		return sessionAgentTargets(nodeTargets(m.Nodes.Get(), m.Conn.Get().Self.GetId(), m.Agents.Get()))
	}).WithEqual(slices.Equal)

	show := func(res *protocol.Routines) {
		items.Set(res.GetRoutines())
		runs.Set(res.GetRuns())
		runsErr.Set(res.GetRunsError())
		loaded.Set(true)
	}
	load := func() {
		fetch(m, 15*time.Second, func(ctx context.Context, c *client.Client) (*protocol.Routines, error) {
			return c.Routines(ctx, &protocol.ListRoutines{Runs: routineRunsShown})
		}, func(res *protocol.Routines, err error) {
			if err != nil {
				errText.Set(nodeErr(err))
				return
			}
			errText.Set("")
			show(res)
		})
	}
	// act asks the daemon for one change; its reply, or a fresh read after
	// a refusal, is what the page shows next.
	act := func(req *protocol.RoutineAction, done func(*protocol.Routines, error)) {
		req.Runs = routineRunsShown
		fetch(m, 90*time.Second, func(ctx context.Context, c *client.Client) (*protocol.Routines, error) {
			return c.ActRoutine(ctx, req)
		}, func(res *protocol.Routines, err error) {
			if err != nil {
				errText.Set(nodeErr(err))
				load()
			} else {
				errText.Set("")
				show(res)
			}
			if done != nil {
				done(res, err)
			}
		})
	}
	load()

	runNow := func(r *protocol.Routine) {
		act(&protocol.RoutineAction{Op: "run", Routine: r.GetId()}, func(res *protocol.Routines, err error) {
			if err != nil {
				m.toaster.Push(ui.Toast(T("Could not start routine"), nodeErr(err)).Destructive())
				return
			}
			m.toaster.Push(ui.Toast(Tf("Started routine %s", r.GetName()), res.GetSession().GetId()))
			m.link.pollNow()
		})
	}

	addRoutine := func() {
		proj := strings.TrimSpace(ggui.Peek(project))
		if proj != "" {
			if abs, err := filepath.Abs(proj); err == nil {
				proj = abs
			}
		}
		concurrent, _ := strconv.Atoi(ggui.Peek(maxConcurrent))
		perPoll, _ := strconv.Atoi(ggui.Peek(maxPerPoll))
		r := routines.Routine{
			Name:          ggui.Peek(name),
			Enabled:       true,
			Trigger:       routines.TriggerKind(ggui.Peek(trigger)),
			Schedule:      ggui.Peek(schedule),
			Project:       proj,
			Agent:         ggui.Peek(agentTarget),
			Worktree:      ggui.Peek(worktree),
			Prompt:        ggui.Peek(prompt),
			MaxConcurrent: concurrent,
		}.Normalized()
		if r.Trigger == routines.TriggerCron {
			r.MaxPerPoll, r.Cooldown, r.AllowExternal = 0, "", false
		} else {
			r.Schedule = ""
			r.MaxPerPoll, r.Cooldown, r.AllowExternal = perPoll, ggui.Peek(cooldown), ggui.Peek(allowExternal)
		}
		if err := r.Validate(); err != nil {
			errText.Set(err.Error())
			return
		}
		spec := &protocol.Routine{
			Name: r.Name, Enabled: r.Enabled, Trigger: string(r.Trigger), Schedule: r.Schedule,
			Project: r.Project, Agent: r.Agent, Worktree: r.Worktree, Prompt: r.Prompt,
			AllowExternal: r.AllowExternal, MaxConcurrent: uint32(r.MaxConcurrent), MaxPerPoll: uint32(r.MaxPerPoll), Cooldown: r.Cooldown,
		}
		act(&protocol.RoutineAction{Op: "add", Spec: spec}, func(_ *protocol.Routines, err error) {
			if err == nil {
				name.Set("")
				prompt.Set("")
			}
		})
	}

	listView := ggui.Reactive(func() ggui.Widget {
		list := items.Get()
		if !loaded.Get() {
			return card(widget.Box(widget.Row(
				widget.Expanded(muted(widget.Text(T("Reading this node's routines…")).Size(12.5))),
				ui.Button(T("Retry"), load).Outline(),
			).Gap(12).Align(widget.AlignCenter)).Pad(14, 16))
		}
		if len(list) == 0 {
			return card(widget.Box(muted(widget.Text(T("No routines configured yet. Create one below to automate recurring or Forge-triggered agent runs.")).Size(13))).Pad(14, 16))
		}
		now := time.Now()
		rows := make([]ggui.Widget, 0, len(list))
		for _, r := range list {
			subtitle := T(routineTriggerLabels[r.GetTrigger()])
			if routines.TriggerKind(r.GetTrigger()) == routines.TriggerCron {
				subtitle = r.GetSchedule()
				if sched, err := routines.ParseSchedule(r.GetSchedule()); err == nil {
					if next := sched.Next(now); !next.IsZero() {
						subtitle += " · " + Tf("next %s", next.Local().Format("01-02 15:04"))
					}
				}
			}
			subtitle += " · " + r.GetAgent() + " · " + prettyPath(r.GetProject())
			toggle := pick(r.GetEnabled(), "disable", "enable")
			rows = append(rows, widget.Box(widget.Column(
				widget.Row(
					dot(pick(r.GetEnabled(), skin.DotRunning, skin.ExtraMuted)),
					widget.Expanded(widget.Column(
						widget.Text(r.GetName()).Size(13.5).Weight(ggui.WeightSemibold),
						muted(widget.Text(subtitle).Size(12).NoWrap().Ellipsis()),
					).Gap(2).Stretch()),
					ui.Button(T("Run now"), func() { runNow(r) }).Outline().Pad(4, 10).Radius(6),
					ui.Button(pick(r.GetEnabled(), T("Disable"), T("Enable")), func() {
						act(&protocol.RoutineAction{Op: toggle, Routine: r.GetId()}, nil)
					}).Ghost().Pad(4, 8).Radius(6),
					iconButton("x", T("Delete routine"), func() {
						act(&protocol.RoutineAction{Op: "remove", Routine: r.GetId()}, nil)
					}),
				).Gap(8).Align(widget.AlignCenter),
				muted(widget.Text(firstLine(r.GetPrompt())).Size(12).NoWrap().Ellipsis()),
			).Gap(6).Stretch()).Pad(12, 16))
		}
		return card(rows...)
	})

	// The trigger's fields are built again only when the trigger changes:
	// the schedule's field is not rebuilt as it is typed in, which would
	// lose its focus, caret and composition; only its hint reads it.
	limits := []string{"1", "2", "3", "5", "10"}
	triggerFields := ggui.Reactive(func() ggui.Widget {
		if trigger.Get() != string(routines.TriggerCron) {
			return widget.Column(
				widget.Row(
					widget.Box(ui.Select(maxPerPoll).Options(limits).Format(func(v string) string { return Tf("Runs per poll: %s", v) }).Name(T("Runs per poll"))).Width(200),
					widget.Box(ui.Select(cooldown).Options([]string{"0", "5m", "10m", "30m", "1h"}).Format(func(v string) string {
						if v == "0" {
							return T("No cooldown")
						}
						return Tf("Cooldown %s", v)
					}).Name(T("Cooldown"))).Width(200),
				).Gap(10).Align(widget.AlignCenter),
				ui.Switch(allowExternal, T("Also run for pull requests and CI runs from forks or outside contributors")),
				muted(widget.Text(T("The event's title, branch and author are given to the agent after the prompt, marked as untrusted.")).Size(12)),
			).Gap(8).Stretch()
		}
		hint := ggui.Reactive(func() ggui.Widget {
			sched, err := routines.ParseSchedule(schedule.Get())
			if err != nil {
				return widget.Text(err.Error()).Size(12).Color(skin.DotDown)
			}
			if next := sched.Next(time.Now()); !next.IsZero() {
				return muted(widget.Text(Tf("Next run: %s", next.Local().Format("2006-01-02 15:04"))).Size(12))
			}
			return widget.Box()
		})
		return widget.Column(
			ui.TextField(schedule).Placeholder("0 9 * * 1-5 / @hourly / @every 30m").Name(T("Schedule")),
			hint,
		).Gap(4).Stretch()
	})

	createCard := card(
		widget.Box(widget.Column(
			widget.Text(T("New routine")).Size(13.5).Weight(ggui.WeightSemibold),
			widget.Row(
				widget.Expanded(ui.TextField(name).Placeholder(T("Routine name")).Name(T("Routine name"))),
				widget.Box(ui.Select(agentTarget).BindOptions(targets).Format(targetLabel).Name(T("Agent"))).Width(200),
			).Gap(10).Align(widget.AlignCenter),
			ui.TextField(project).Placeholder(T("Project folder path")).Name(T("Project")),
			widget.Row(
				widget.Box(ui.Select(trigger).Options([]string{
					string(routines.TriggerCron),
					string(routines.TriggerForgePR),
					string(routines.TriggerForgeCIFail),
				}).Format(func(v string) string {
					if l, ok := routineTriggerLabels[v]; ok {
						return T(l)
					}
					return v
				}).Name(T("Trigger"))).Width(200),
				widget.Expanded(triggerFields),
			).Gap(10).Align(widget.AlignStart),
			widget.Row(
				widget.Expanded(ui.Switch(worktree, T("Run in a new isolated worktree"))),
				widget.Box(ui.Select(maxConcurrent).Options(limits).Format(func(v string) string { return Tf("Sessions at work at once: %s", v) }).
					Name(T("Sessions at work at once"))).Width(240),
			).Gap(10).Align(widget.AlignCenter),
			ui.TextField(prompt).Lines(3).Placeholder(T("Prompt to run when triggered")).Name(T("Prompt")),
			ggui.Reactive(func() ggui.Widget {
				if e := errText.Get(); e != "" {
					return widget.Text(e).Size(12).Color(skin.DotDown)
				}
				return widget.Box()
			}),
			widget.Row(widget.Spacer(), ui.Button(T("Add routine"), addRoutine)).Align(widget.AlignCenter),
		).Gap(10).Stretch()).Pad(14, 16),
	)

	historyView := ggui.Reactive(func() ggui.Widget {
		list := runs.Get()
		if e := runsErr.Get(); e != "" {
			return card(widget.Box(warnLine("circle-slash", skin.DiffRemoved, Tf("Could not read the routines' runs: %s", e))).Pad(12, 16))
		}
		if len(list) == 0 {
			return widget.Box()
		}
		rows := make([]ggui.Widget, 0, len(list)+1)
		rows = append(rows, widget.Box(widget.Text(T("Recent runs")).Size(13).Weight(ggui.WeightSemibold)).Pad(10, 16))
		for _, rec := range list {
			ts := rec.GetStarted().AsTime().Local().Format("01-02 15:04")
			statusCol := skin.DotDown
			switch rec.GetStatus() {
			case "started":
				statusCol = skin.DotRunning
			case "skipped":
				statusCol = skin.DotWaiting
			}
			detail := rec.GetTrigger()
			if rec.GetError() != "" {
				detail += " · " + rec.GetError()
			} else if rec.GetSessionId() != "" {
				detail += " · " + rec.GetSessionId()
			}
			rows = append(rows, widget.Box(widget.Row(
				dot(statusCol),
				widget.Text(rec.GetRoutineName()).Size(12.5).Weight(ggui.WeightMedium).NoWrap(),
				widget.Expanded(muted(widget.Text(detail).Size(12).NoWrap().Ellipsis())),
				muted(widget.Text(ts).Size(11.5).NoWrap()),
			).Gap(8).Align(widget.AlignCenter)).Pad(8, 16))
		}
		return card(rows...)
	})

	return page(T("Routines"), T("Self-hosted scheduled and Forge-triggered agent runs executed by this node's daemon."),
		widget.Column(listView, createCard, historyView).Gap(16).Stretch())
}
