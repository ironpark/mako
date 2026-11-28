package gui

import (
	"cmp"
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/routines"
)

var routineTriggerLabels = map[string]string{
	string(routines.TriggerCron):        "Cron schedule",
	string(routines.TriggerForgePR):     "New pull request",
	string(routines.TriggerForgeCIFail): "Failed CI run",
}

func routineSettings(m *model) ggui.Widget {
	dir, _ := config.Dir()
	loadList := func() []routines.Routine {
		list, _ := routines.Load(dir)
		return list
	}
	loadHist := func() []routines.RunRecord {
		hist, _ := routines.LoadHistory(dir)
		return hist
	}

	items := ggui.State(loadList())
	hist := ggui.State(loadHist())
	errText := ggui.State("")

	name := ggui.State("")
	project := ggui.State(m.launchProject(ggui.Peek(m.Conn).Self.GetId()))
	agentTarget := ggui.State(cmp.Or(ggui.Peek(m.Prefs).DefaultAgent, "claude"))
	trigger := ggui.State(string(routines.TriggerCron))
	schedule := ggui.State("0 9 * * 1-5")
	worktree := ggui.State(true)
	prompt := ggui.State("")

	targets := ggui.Derived(func() []string {
		return sessionAgentTargets(nodeTargets(m.Nodes.Get(), m.Conn.Get().Self.GetId(), m.Agents.Get()))
	}).WithEqual(slices.Equal)

	saveList := func(next []routines.Routine) bool {
		if err := routines.Save(dir, next); err != nil {
			errText.Set(err.Error())
			return false
		}
		errText.Set("")
		items.Set(loadList())
		return true
	}

	runNow := func(r routines.Routine) {
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
			prefix := cmp.Or(r.BranchPrefix, "routine/"+r.ID)
			req.Worktree = &protocol.Worktree{Branch: fmt.Sprintf("%s-%s", prefix, time.Now().Format("20060102-150405"))}
		}
		fetch(m, 30*time.Second, func(ctx context.Context, c *client.Client) (*protocol.SessionInfo, error) {
			return c.Open(ctx, req)
		}, func(info *protocol.SessionInfo, err error) {
			rec := routines.RunRecord{
				RoutineID:   r.ID,
				RoutineName: r.Name,
				Trigger:     "manual",
				StartedAt:   time.Now().Unix(),
			}
			if err != nil {
				rec.Status = "error"
				rec.Error = nodeErr(err)
				_ = routines.AppendHistory(dir, rec)
				hist.Set(loadHist())
				m.toaster.Push(ui.Toast(T("Could not start routine"), nodeErr(err)).Destructive())
				return
			}
			rec.Status = "started"
			rec.SessionID = info.GetId()
			_ = routines.AppendHistory(dir, rec)
			hist.Set(loadHist())
			m.toaster.Push(ui.Toast(Tf("Started routine %s", r.Name), info.GetId()))
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
		r := routines.Routine{
			Name:     ggui.Peek(name),
			Enabled:  true,
			Trigger:  routines.TriggerKind(ggui.Peek(trigger)),
			Schedule: ggui.Peek(schedule),
			Project:  proj,
			Agent:    ggui.Peek(agentTarget),
			Worktree: ggui.Peek(worktree),
			Prompt:   ggui.Peek(prompt),
		}.Normalized()
		if err := r.Validate(); err != nil {
			errText.Set(err.Error())
			return
		}
		cur := slices.Clone(ggui.Peek(items))
		cur = append(cur, r)
		if saveList(cur) {
			name.Set("")
			prompt.Set("")
		}
	}

	listView := ggui.Reactive(func() ggui.Widget {
		list := items.Get()
		if len(list) == 0 {
			return card(widget.Box(muted(widget.Text(T("No routines configured yet. Create one below to automate recurring or Forge-triggered agent runs.")).Size(13))).Pad(14, 16))
		}
		now := time.Now()
		rows := make([]ggui.Widget, 0, len(list))
		for i, item := range list {
			idx := i
			r := item
			subtitle := T(routineTriggerLabels[string(r.Trigger)])
			if r.Trigger == routines.TriggerCron {
				subtitle = r.Schedule
				if sched, err := routines.ParseSchedule(r.Schedule); err == nil {
					if next := sched.Next(now); !next.IsZero() {
						subtitle += " · " + Tf("next %s", next.Local().Format("01-02 15:04"))
					}
				}
			}
			subtitle += " · " + r.Agent + " · " + prettyPath(r.Project)
			toggleVal := ggui.State(r.Enabled)
			toggleBtn := ui.Button(pick(r.Enabled, T("Disable"), T("Enable")), func() {
				cur := slices.Clone(ggui.Peek(items))
				if idx < len(cur) {
					cur[idx].Enabled = !cur[idx].Enabled
					toggleVal.Set(cur[idx].Enabled)
					saveList(cur)
				}
			}).Ghost().Pad(4, 8).Radius(6)
			rows = append(rows, widget.Box(widget.Column(
				widget.Row(
					dot(pick(r.Enabled, skin.DotRunning, skin.ExtraMuted)),
					widget.Expanded(widget.Column(
						widget.Text(r.Name).Size(13.5).Weight(ggui.WeightSemibold),
						muted(widget.Text(subtitle).Size(12).NoWrap().Ellipsis()),
					).Gap(2).Stretch()),
					ui.Button(T("Run now"), func() { runNow(r) }).Outline().Pad(4, 10).Radius(6),
					toggleBtn,
					iconButton("x", T("Delete routine"), func() {
						cur := slices.Clone(ggui.Peek(items))
						if idx < len(cur) {
							cur = slices.Delete(cur, idx, idx+1)
							saveList(cur)
						}
					}),
				).Gap(8).Align(widget.AlignCenter),
				muted(widget.Text(firstLine(r.Prompt)).Size(12).NoWrap().Ellipsis()),
			).Gap(6).Stretch()).Pad(12, 16))
		}
		return card(rows...)
	})

	schedulePreview := ggui.Reactive(func() ggui.Widget {
		if trigger.Get() != string(routines.TriggerCron) {
			return widget.Box()
		}
		expr := schedule.Get()
		var hint ggui.Widget
		if sched, err := routines.ParseSchedule(expr); err != nil {
			hint = widget.Text(err.Error()).Size(12).Color(skin.DotDown)
		} else if next := sched.Next(time.Now()); !next.IsZero() {
			hint = muted(widget.Text(Tf("Next run: %s", next.Local().Format("2006-01-02 15:04"))).Size(12))
		} else {
			hint = widget.Box()
		}
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
				widget.Expanded(schedulePreview),
			).Gap(10).Align(widget.AlignStart),
			ui.Switch(worktree, T("Run in a new isolated worktree")),
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
		list := hist.Get()
		if len(list) == 0 {
			return widget.Box()
		}
		shown := list[:min(len(list), 10)]
		rows := make([]ggui.Widget, 0, len(shown)+1)
		rows = append(rows, widget.Box(widget.Text(T("Recent runs")).Size(13).Weight(ggui.WeightSemibold)).Pad(10, 16))
		for _, rec := range shown {
			ts := time.Unix(rec.StartedAt, 0).Local().Format("01-02 15:04")
			statusCol := pick(rec.Status == "started", skin.DotRunning, skin.DotDown)
			detail := rec.Trigger
			if rec.Error != "" {
				detail += " · " + rec.Error
			} else if rec.SessionID != "" {
				detail += " · " + rec.SessionID
			}
			rows = append(rows, widget.Box(widget.Row(
				dot(statusCol),
				widget.Text(rec.RoutineName).Size(12.5).Weight(ggui.WeightMedium).NoWrap(),
				widget.Expanded(muted(widget.Text(detail).Size(12).NoWrap().Ellipsis())),
				muted(widget.Text(ts).Size(11.5).NoWrap()),
			).Gap(8).Align(widget.AlignCenter)).Pad(8, 16))
		}
		return card(rows...)
	})

	return page(T("Routines"), T("Self-hosted scheduled and Forge-triggered agent runs executed by this node's daemon."),
		widget.Column(listView, createCard, historyView).Gap(16).Stretch())
}
