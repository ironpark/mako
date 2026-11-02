package gui

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/mako/internal/protocol"
)

// An agent's background commands, as Claude Code's Bash runs with
// run_in_background, show where the session does: how many run on its row
// in the sidebar, each in a menu in the header over its terminal, and
// those running over the composer in Chat. Each opens its output, the file
// on the session's node it is written to, in a tab.

// taskTitle names a task: what its call said it does, else its command.
func taskTitle(t *protocol.BackgroundTask) string {
	return cmp.Or(t.GetDescription(), t.GetCommand(), t.GetId())
}

// taskState says how a task is getting on: how long it has run, or how it
// ended and when.
func taskState(t *protocol.BackgroundTask, now time.Time) string {
	if t.GetRunning() {
		return untilText(now.Sub(time.Unix(t.GetStarted(), 0)))
	}
	state := T("Finished")
	switch t.GetStatus() {
	case protocol.TaskFailed:
		state = T("Failed")
	case protocol.TaskKilled:
		state = T("Killed")
	case protocol.TaskStopped:
		state = T("Stopped")
	}
	if t.ExitCode != nil && t.GetExitCode() != 0 {
		state += " · " + Tf("exit %d", t.GetExitCode())
	}
	return state
}

// openTaskOutput opens the output of task t, of a session on node, in a
// tab: read anew when it is open already, unless it was edited there.
func (m *model) openTaskOutput(node string, t *protocol.BackgroundTask) {
	path := t.GetOutput()
	if path == "" {
		return
	}
	if doc := m.docs[docTabID(node, path)]; doc != nil && ggui.Peek(doc.text) == doc.saved {
		m.readDoc(docTabID(node, path))
	}
	m.openFile(fileItem{Key: dirKey(node, path), Node: node, Path: path, Name: baseName(path)})
}

// tasksBadge marks a session's row with how many background commands its
// agent runs, nothing while none does.
func tasksBadge(s *protocol.SessionInfo) ggui.Widget {
	n := s.RunningTasks()
	if n == 0 {
		return nil
	}
	var titles []string
	for _, t := range s.GetTasks() {
		if t.GetRunning() {
			titles = append(titles, taskTitle(t))
		}
	}
	badge := ggui.Row(
		icon("square-terminal").Size(11).Color(tokAccentBright),
		ggui.Text(fmt.Sprint(n)).Size(11.5).Color(tokAccentBright).NoWrap(),
	).Gap(3).Align(ggui.AlignCenter)
	return ui.Tooltip(badge, Tf("Running in the background: %s", strings.Join(titles, ", ")))
}

// tasksMenu is the header's menu of session s's background commands, the
// latest first, each to open its output; nothing while it has none.
func tasksMenu(m *model, s *protocol.SessionInfo) ggui.Widget {
	tasks := slices.Clone(s.GetTasks())
	if len(tasks) == 0 {
		return nil
	}
	slices.Reverse(tasks)
	now := time.Now()
	items := make([]ggui.Widget, 0, len(tasks))
	for _, t := range tasks {
		mark := "✓"
		switch {
		case t.GetRunning():
			mark = "●"
		case t.GetStatus() != protocol.TaskCompleted || t.GetExitCode() != 0:
			mark = "✕"
		}
		item := ui.MenuItem(fmt.Sprintf("%s  %s · %s", mark, taskTitle(t), taskState(t, now)), func() { m.openTaskOutput(s.GetNodeId(), t) })
		items = append(items, item)
	}
	color := tokMuted
	trigger := []ggui.Widget{icon("square-terminal").Size(15).Color(color)}
	if n := s.RunningTasks(); n > 0 {
		trigger = []ggui.Widget{
			icon("square-terminal").Size(15).Color(tokAccentBright),
			ggui.Text(fmt.Sprint(n)).Size(12).Color(tokAccentBright).NoWrap(),
		}
	}
	return ui.Tooltip(iconMenu(ui.MenuOf(ggui.Row(trigger...).Gap(4).Align(ggui.AlignCenter), items...).Name(T("Background commands")), 6), T("Background commands"))
}

// runningTasks lists, over the composer, the background commands of
// session id still running: what each does and how long it has run, each
// to open its output. Nothing shows while none runs.
func runningTasks(m *model, id string) ggui.Widget {
	running := ggui.Derived(func() []*protocol.BackgroundTask {
		s, ok := sessionIn(m.Sessions.Get(), id)
		if !ok {
			return nil
		}
		var out []*protocol.BackgroundTask
		for _, t := range s.GetTasks() {
			if t.GetRunning() {
				out = append(out, t)
			}
		}
		return out
	}).WithEqual(func(a, b []*protocol.BackgroundTask) bool {
		return slices.EqualFunc(a, b, protocol.Same[*protocol.BackgroundTask])
	})
	return ggui.View(running, func(tasks []*protocol.BackgroundTask) ggui.Widget {
		if len(tasks) == 0 {
			return ggui.Box()
		}
		s, _ := sessionIn(ggui.Peek(m.Sessions), id)
		rows := make([]ggui.Widget, len(tasks))
		for i, t := range tasks {
			var row ggui.Widget = ggui.Row(
				icon("square-terminal").Size(13).Color(tokMuted),
				ggui.Expanded(ggui.Text(taskTitle(t)).Size(12.5).Weight(ggui.WeightMedium).NoWrap().Ellipsis()),
				ggui.View(m.Minute, func(now time.Time) ggui.Widget {
					return muted(ggui.Text(taskState(t, now)).Size(11.5).NoWrap())
				}),
			).Gap(8).Align(ggui.AlignCenter)
			if t.GetOutput() != "" {
				nodeID := s.GetNodeId()
				row = ui.Tooltip(ggui.Tap(row, func() { m.openTaskOutput(nodeID, t) }).Cursor(ggui.CursorShapePointer), T("Open its output"))
			}
			rows[i] = row
		}
		return &lane{child: ggui.Box(ggui.Column(rows...).Gap(6).Stretch()).
			Fill(tokSurface1).Border(1, tokBorder).Radius(10).Pad(8, 12)}
	})
}
