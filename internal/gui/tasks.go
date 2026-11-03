package gui

import (
	"cmp"
	"context"
	"fmt"
	"image/color"
	"slices"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/protocol"
)

// An agent's background commands, as Claude Code's Bash runs with
// run_in_background, show where the session does, as Claude Code's own
// screen shows its shells: how many run on its row in the sidebar, each
// in a menu in the header over its terminal, and over the composer in
// Chat a line saying how many run, which opens to the list of them. Each
// opens to its details: how it is getting on, its command whole, and the
// end of its output, read from the file on the session's node it is
// written to while it runs.

// taskTitle names a task: what its call said it does, else its command's
// first line.
func taskTitle(t *protocol.BackgroundTask) string {
	return cmp.Or(t.GetDescription(), firstLine(t.GetCommand()), t.GetId())
}

// firstLine is s up to its first line break, marked as cut short when
// there is more.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " …"
	}
	return s
}

// taskState says how a task is getting on: how long it has run, or how it
// ended and when.
func taskState(t *protocol.BackgroundTask, now time.Time) string {
	if t.GetRunning() {
		return untilText(now.Sub(time.Unix(t.GetStarted(), 0)))
	}
	return taskOutcome(t)
}

// taskOutcome is how a task stands, with the code it exited with when
// that was not 0.
func taskOutcome(t *protocol.BackgroundTask) string {
	state := taskStatus(t)
	if t.ExitCode != nil && t.GetExitCode() != 0 {
		state += " · " + Tf("exit %d", t.GetExitCode())
	}
	return state
}

// taskStatus is how a task stands, in a word.
func taskStatus(t *protocol.BackgroundTask) string {
	switch {
	case t.GetRunning():
		return T("Running")
	case t.GetStatus() == protocol.TaskFailed:
		return T("Failed")
	case t.GetStatus() == protocol.TaskKilled:
		return T("Killed")
	case t.GetStatus() == protocol.TaskStopped:
		return T("Stopped")
	}
	return T("Finished")
}

// taskEnd is how a task is getting on, as its dot and its mark show it.
type taskEnd int

const (
	taskRunning    taskEnd = iota
	taskDone               // ended well
	taskFailedEnd          // failed, or exited with a code not 0
	taskStoppedEnd         // killed, or stopped with its agent
)

func endOf(t *protocol.BackgroundTask) taskEnd {
	switch {
	case t.GetRunning():
		return taskRunning
	case t.GetStatus() == protocol.TaskFailed, t.GetExitCode() != 0:
		return taskFailedEnd
	case t.GetStatus() == protocol.TaskCompleted, t.GetStatus() == "":
		return taskDone
	}
	return taskStoppedEnd
}

// taskColor is the colour of a task's dot.
func taskColor(t *protocol.BackgroundTask) color.Color {
	return [...]color.Color{tokDotRunning, tokDotDone, tokDiffRemoved, tokDotExited}[endOf(t)]
}

// taskMark is a task's dot in text, for a menu.
func taskMark(t *protocol.BackgroundTask) string {
	return [...]string{"●", "✓", "✕", "■"}[endOf(t)]
}

// taskRuntime is how long a task ran, or has run, to the second.
func taskRuntime(t *protocol.BackgroundTask, now time.Time) time.Duration {
	end := now
	if !t.GetRunning() && t.GetEnded() > 0 {
		end = time.Unix(t.GetEnded(), 0)
	}
	return max(end.Sub(time.Unix(t.GetStarted(), 0)), 0)
}

// runtimeText is d to the second: 5m 23s.
func runtimeText(d time.Duration) string {
	d = d.Truncate(time.Second)
	h, mn, s := int(d/time.Hour), int(d%time.Hour/time.Minute), int(d%time.Minute/time.Second)
	switch {
	case h > 0:
		return Tf("%dh %dm %ds", h, mn, s)
	case mn > 0:
		return Tf("%dm %ds", mn, s)
	}
	return Tf("%ds", s)
}

// shellsText is how many shells run: 1 shell, 3 shells.
func shellsText(n int) string {
	if n == 1 {
		return T("1 shell")
	}
	return Tf("%d shells", n)
}

// latestFirst is a session's tasks, those running first, each lot the
// latest first.
func latestFirst(tasks []*protocol.BackgroundTask) []*protocol.BackgroundTask {
	out := slices.Clone(tasks)
	slices.Reverse(out)
	slices.SortStableFunc(out, func(a, b *protocol.BackgroundTask) int {
		switch {
		case a.GetRunning() == b.GetRunning():
			return 0
		case a.GetRunning():
			return -1
		}
		return 1
	})
	return out
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

// tasksMenu is the header's menu of session s's background commands, those
// running first, each to open its details; nothing while it has none.
func tasksMenu(m *model, s *protocol.SessionInfo) ggui.Widget {
	if len(s.GetTasks()) == 0 {
		return nil
	}
	now, id := time.Now(), s.GetId()
	var items []ggui.Widget
	for _, t := range latestFirst(s.GetTasks()) {
		items = append(items, ui.MenuItem(fmt.Sprintf("%s  %s · %s", taskMark(t), taskTitle(t), taskState(t, now)), func() { m.Shell.open(id, t.GetId()) }))
	}
	trigger := []ggui.Widget{icon("square-terminal").Size(15).Color(tokMuted)}
	if n := s.RunningTasks(); n > 0 {
		trigger = []ggui.Widget{
			icon("square-terminal").Size(15).Color(tokAccentBright),
			ggui.Text(fmt.Sprint(n)).Size(12).Color(tokAccentBright).NoWrap(),
		}
	}
	return ui.Tooltip(iconMenu(ui.MenuOf(ggui.Row(trigger...).Gap(4).Align(ggui.AlignCenter), items...).Name(T("Background commands")), 6), T("Background commands"))
}

// runningTasks stands over the composer of session id while its agent
// runs commands in the background: a line saying how many, as Claude
// Code's footer does, which opens to all its commands, those running
// first, each to open its details. Nothing shows while none runs.
func runningTasks(m *model, id string) ggui.Widget {
	tasks := ggui.Derived(func() []*protocol.BackgroundTask {
		s, ok := sessionIn(m.Sessions.Get(), id)
		if !ok || s.RunningTasks() == 0 {
			return nil
		}
		return latestFirst(s.GetTasks())
	}).WithEqual(func(a, b []*protocol.BackgroundTask) bool {
		return slices.EqualFunc(a, b, protocol.Same[*protocol.BackgroundTask])
	})
	open := ggui.State(false)
	return ggui.View(tasks, func(list []*protocol.BackgroundTask) ggui.Widget {
		if len(list) == 0 {
			return ggui.Box()
		}
		// Those running come first.
		running := slices.IndexFunc(list, func(t *protocol.BackgroundTask) bool { return !t.GetRunning() })
		if running < 0 {
			running = len(list)
		}
		var head ggui.Widget = ggui.Tap(ggui.Row(
			icon("square-terminal").Size(13).Color(tokAccentBright),
			ggui.Text(shellsText(running)).Size(12.5).Weight(ggui.WeightMedium).Color(tokAccentBright).NoWrap(),
			ggui.Expanded(muted(ggui.Text(taskTitle(list[0])).Size(12).NoWrap().Ellipsis())),
			ggui.View(open, func(o bool) ggui.Widget {
				return icon(pick(o, "chevron-down", "chevron-up")).Size(14).Color(tokMuted)
			}),
		).Gap(8).Align(ggui.AlignCenter), func() { open.Set(!ggui.Peek(open)) }).Cursor(ggui.CursorShapePointer)
		head = ui.Tooltip(head, T("Background commands"))
		rows := ggui.If(open, func() ggui.Widget {
			out := make([]ggui.Widget, len(list))
			for i, t := range list {
				out[i] = taskRow(m, id, t)
			}
			return ggui.Column(append([]ggui.Widget{hline()}, out...)...).Gap(4).Stretch()
		})
		return &lane{child: ggui.Box(ggui.Column(head, rows).Gap(6).Stretch()).
			Fill(tokSurface1).Border(1, tokBorder).Radius(10).Pad(8, 12)}
	})
}

// taskRow is one of a session's commands in the list over the composer:
// its dot, what it does, and how it is getting on, to open its details.
func taskRow(m *model, session string, t *protocol.BackgroundTask) ggui.Widget {
	row := ggui.Row(
		dot(taskColor(t)),
		ggui.Expanded(ggui.Text(taskTitle(t)).Size(12.5).NoWrap().Ellipsis()),
		ggui.View(m.Minute, func(now time.Time) ggui.Widget {
			return muted(ggui.Text(taskState(t, now)).Size(11.5).NoWrap())
		}),
	).Gap(8).Align(ggui.AlignCenter)
	return ui.Tooltip(ggui.Tap(row, func() { m.Shell.open(session, t.GetId()) }).Cursor(ggui.CursorShapePointer), T("Show its details"))
}

// shellView is the details of the background command shown: which, and
// the end of its output as last read.
type shellView struct {
	Open   *ggui.StateValue[bool]
	At     *ggui.StateValue[shellRef]
	Output *ggui.StateValue[shellOutput]
	// reading is the command whose output is being read, if busy; UI
	// goroutine only. Another's is read at once.
	reading shellRef
	busy    bool
}

// shellRef names a background command: its session, and its ID there.
type shellRef struct{ Session, Task string }

// shellOutput is the end of a command's output, as read.
type shellOutput struct {
	Text   string
	Cut    bool // there is more before it, in the file
	Err    string
	Loaded bool
}

func newShellView() *shellView {
	return &shellView{
		Open:   ggui.State(false),
		At:     ggui.State(shellRef{}),
		Output: ggui.State(shellOutput{}),
	}
}

// open shows task of session.
func (v *shellView) open(session, task string) {
	v.At.Set(shellRef{session, task})
	v.Output.Set(shellOutput{})
	v.Open.Set(true)
}

// outputTail is how much of a command's output its details show, from the
// end.
const outputTail = 16 << 10

// readOutput reads the end of the output of task t, of a session on node:
// as much as outputTail from its end.
func (m *model) readOutput(node string, t *protocol.BackgroundTask) {
	v := m.Shell
	path, at := t.GetOutput(), ggui.Peek(v.At)
	if path == "" || v.busy && v.reading == at {
		return
	}
	v.reading, v.busy = at, true
	req := &protocol.ReadFile{NodeId: m.remoteID(node), Path: path, Raw: true, Offset: -outputTail, Max: outputTail}
	fetch(m, 15*time.Second, func(ctx context.Context, c *client.Client) (*protocol.File, error) {
		f, err := c.ReadFile(ctx, req)
		if err == nil {
			return f, nil
		}
		// A daemon before reads from the end: the size, then the end.
		if f, err = c.ReadFile(ctx, &protocol.ReadFile{NodeId: req.NodeId, Path: path, Raw: true, Offset: 1 << 62}); err != nil {
			return nil, err
		}
		return c.ReadFile(ctx, &protocol.ReadFile{NodeId: req.NodeId, Path: path, Raw: true, Offset: max(0, f.GetSize()-outputTail), Max: outputTail})
	}, func(f *protocol.File, err error) {
		if v.reading == at {
			v.busy = false
		}
		if ggui.Peek(v.At) != at {
			return
		}
		if err != nil {
			v.Output.Set(shellOutput{Err: err.Error(), Loaded: true})
			return
		}
		text := agent.StripANSI(string(f.GetData()))
		cut := f.GetSize() > int64(len(f.GetData()))
		if cut {
			// The first line is likely a piece of one.
			if i := strings.IndexByte(text, '\n'); i >= 0 {
				text = text[i+1:]
			}
		}
		v.Output.Set(shellOutput{Text: strings.TrimRight(text, "\n"), Cut: cut, Loaded: true})
	})
}

// shellDialog shows a background command in full, as Claude Code's Shell
// details do: how it stands, how long it has run, its command whole and
// the end of its output, read again every couple of seconds while it
// runs.
func shellDialog(m *model) ggui.Widget {
	v := m.Shell
	// The command as its session last said, and its node.
	task := ggui.Derived(func() shellTask {
		at := v.At.Get()
		s, ok := sessionIn(m.Sessions.Get(), at.Session)
		if !ok {
			return shellTask{}
		}
		for _, t := range s.GetTasks() {
			if t.GetId() == at.Task {
				return shellTask{Node: s.GetNodeId(), Task: t}
			}
		}
		return shellTask{}
	}).WithEqual(func(a, b shellTask) bool { return a.Node == b.Node && protocol.Same(a.Task, b.Task) })
	// The runtime moves while the command runs.
	now := ggui.State(time.Now())
	ggui.Effect(func() ggui.Cleanup {
		cur := task.Get()
		if !v.Open.Get() || cur.Task == nil {
			return nil
		}
		now.Set(time.Now())
		m.readOutput(cur.Node, cur.Task)
		if !cur.Task.GetRunning() {
			return nil
		}
		return m.every(2*time.Second, func() {
			now.Set(time.Now())
			m.readOutput(cur.Node, cur.Task)
		})
	})
	body := ggui.View(task, func(cur shellTask) ggui.Widget {
		t := cur.Task
		if t == nil {
			return muted(ggui.Text(T("This command is no longer listed.")).Size(13))
		}
		label := func(s string) ggui.Widget {
			return ggui.Box(ggui.Text(s).Size(12.5).Weight(ggui.WeightMedium).NoWrap()).Width(84)
		}
		field := func(name string, value ggui.Widget) ggui.Widget {
			return ggui.Row(label(name), ggui.Expanded(value)).Gap(8).Align(ggui.AlignStart)
		}
		rows := []ggui.Widget{
			field(T("Status"), ggui.Row(
				dot(taskColor(t)),
				ggui.Text(taskOutcome(t)).Size(12.5).Color(taskColor(t)),
			).Gap(6).Align(ggui.AlignCenter)),
			field(T("Runtime"), ggui.View(now, func(at time.Time) ggui.Widget {
				return ggui.Text(runtimeText(taskRuntime(t, at))).Size(12.5)
			})),
		}
		if d := t.GetDescription(); d != "" {
			rows = append(rows, field(T("Description"), ggui.Text(d).Size(12.5).Selectable()))
		}
		rows = append(rows, field(T("Command"), ggui.Text(t.GetCommand()).Size(12).Font(ggui.DefaultMonoFont()).Selectable()))
		output := ggui.View(v.Output, func(o shellOutput) ggui.Widget {
			var text ggui.Widget
			switch {
			case !o.Loaded:
				text = muted(ggui.Text(T("Reading…")).Size(12))
			case o.Err != "":
				text = muted(ggui.Text(o.Err).Size(12).Selectable())
			case o.Text == "":
				text = muted(ggui.Text(T("No output yet.")).Size(12))
			default:
				lines := ggui.Text(o.Text).Size(11.5).Font(ggui.DefaultMonoFont()).Selectable()
				text = lines
				if o.Cut {
					text = ggui.Column(muted(ggui.Text(T("Earlier output is in the file.")).Size(11.5)), lines).Gap(6).Stretch()
				}
			}
			// The end is what matters: the scroll opens there, and stays
			// there as more is read while it is.
			return ggui.Box(ggui.Scroll(ggui.Box(text).Pad(8, 10)).Bar(tokSurface3).FollowEnd()).
				Fill(tokSurface0).Border(1, tokBorder).Radius(8).Height(260)
		})
		head := ggui.Row(
			ggui.Expanded(ggui.Text(T("Output")).Size(12.5).Weight(ggui.WeightMedium)),
			ui.Button(T("Open in a tab"), func() { v.Open.Set(false); m.openTaskOutput(cur.Node, t) }).Ghost(),
		).Align(ggui.AlignCenter)
		rows = append(rows, ggui.Column(head, output).Gap(6).Stretch())
		return ggui.Column(rows...).Gap(10).Stretch()
	})
	return ui.Dialog(v.Open, body).Title(T("Shell details")).Width(680)
}

// shellTask is a background command with the node of its session.
type shellTask struct {
	Node string
	Task *protocol.BackgroundTask
}
