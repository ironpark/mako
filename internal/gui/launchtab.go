package gui

import (
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/runtime"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/gui/skin"
)

// isShellTarget reports whether target launches a plain shell rather than an
// AI agent.
func isShellTarget(target string) bool {
	agent, _ := agentspec.SplitTarget(target)
	return agent == "shell"
}

// sessionAgentTargets removes plain shell targets from the choices offered
// when creating a new agent session.
func sessionAgentTargets(targets []string) []string {
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		if !isShellTarget(t) {
			out = append(out, t)
		}
	}
	return out
}

// A new session starts in a tab of its own, as a conversation does in a
// chat app: the tab asks what to work on, over a box like a session's
// composer, whose bar picks the folder, the node, the agent, a team and a
// worktree. What is typed in it is the agent's first prompt, given as it
// starts; the session then takes the tab's place. Each New session opens
// another such tab, with what it picks of its own; none is written down
// with the workspace.

// launchPrefix begins a new session's tab's ID, a number after it.
const launchPrefix = "new\x00"

// isLaunchTab reports whether a tab is a new session's.
func isLaunchTab(id string) bool { return strings.HasPrefix(id, launchPrefix) }

// launchState is what a new session's tab picks, and why the session it
// started did not start. UI goroutine.
type launchState struct {
	Form     *ggui.StateValue[newSession]
	Err      *ggui.StateValue[string]
	Starting *ggui.StateValue[bool]
}

// launchOf is the state of new session's tab id, made for one it has none
// of yet.
func (m *model) launchOf(id string) *launchState {
	l := m.launches[id]
	if l == nil {
		l = &launchState{Form: ggui.State(newSession{}), Err: ggui.State(""), Starting: ggui.State(false)}
		m.launches[id] = l
	}
	return l
}

// newLaunch opens a new session's tab for f, in the current pane, and
// shows it.
func (m *model) newLaunch(f newSession) (string, *launchState) {
	m.launchSeq++
	id := launchPrefix + strconv.Itoa(m.launchSeq)
	l := m.launchOf(id)
	l.Form.Set(f)
	m.showTab(id)
	return id, l
}

// launchShown is the state of the new session's tab shown, nil when the
// tab shown is not one.
func (m *model) launchShown() *launchState {
	if id := ggui.Peek(m.Selected); isLaunchTab(id) {
		return m.launchOf(id)
	}
	return nil
}

// launchTabFace is a new session's tab in a pane's bar.
func launchTabFace(m *model, id string, selected, focused bool) ggui.Widget {
	fg := pick(selected, skin.Fg, skin.Muted)
	return ui.EditorTab(T("New session"), func() { m.showTab(id) }).
		Icon(icon("plus").Size(14).Color(fg)).
		MaxLabelWidth(168).Selected(selected).Current(focused).
		OnClose(func() { m.closeLaunch(id) }).CloseName(T("Close New session"))
}

// closeLaunch closes new session's tab id, forgetting what it held.
func (m *model) closeLaunch(id string) {
	m.closeTab(id)
	delete(m.launches, id)
}

// launchView is what new session's tab id shows: a greeting, and under it
// the box that starts the session.
func launchView(m *model, id string) ggui.Widget {
	l := m.launchOf(id)
	greeting := widget.Center(widget.Column(
		ggui.Reactive(func() ggui.Widget {
			return launchAvatar(m, l.Form.Get().Agent)
		}),
		widget.Text(T("What should we work on?")).Size(22).Weight(ggui.WeightMedium),
		ggui.Reactive(func() ggui.Widget {
			f := l.Form.Get()
			where := prettyPath(f.Project)
			if where == "" {
				where = T("The node's home folder")
			}
			return muted(widget.Text(nodeName(m.Nodes.Get(), f.NodeID) + " · " + where).Size(12.5).NoWrap().Ellipsis())
		}),
	).Gap(12).Align(widget.AlignCenter))
	return widget.Column(widget.Expanded(greeting), launchBox(m, id, l)).Stretch()
}

// launchBox is the box a new session starts from: where it runs over the
// prompt, and the agent and the button that starts it under it.
func launchBox(m *model, id string, l *launchState) ggui.Widget {
	start := func() { m.submitLaunch(id) }
	prompt := l.Form.Field(func(f *newSession) *string { return &f.Prompt })
	agent := l.Form.Field(func(f *newSession) *string { return &f.Agent })
	worktree := l.Form.Field(func(f *newSession) *bool { return &f.Worktree })
	branch := l.Form.Field(func(f *newSession) *string { return &f.Branch })
	where := ggui.Derived(func() launchWhere {
		f := l.Form.Get()
		return launchWhere{f.NodeID, f.Project, f.Agent, f.Team}
	}).WithEqual(func(a, b launchWhere) bool { return a == b })
	shell := ggui.Map(where, func(w launchWhere) bool { return w.Agent == "shell" })
	field := ggui.View(shell, func(sh bool) ggui.Widget { return composerField(launchField(prompt, sh, start)) })
	targets := ggui.Derived(func() []string {
		return sessionAgentTargets(nodeTargets(m.Nodes.Get(), where.Get().NodeID, m.Agents.Get()))
	}).WithEqual(slices.Equal)
	agents := borderless(ui.Select(agent).BindOptions(targets).Format(targetLabel).Name(T("Agent")))
	gitToggle := ui.Tooltip(ui.ButtonOf(ggui.View(worktree, func(on bool) ggui.Widget {
		return widget.Row(icon("git-branch").Size(14).Color(pick(on, skin.AccentBright, skin.Muted)),
			widget.Text(T("Worktree")).Size(12.5).Color(pick(on, skin.Fg, skin.Muted)).NoWrap()).Gap(5).Align(widget.AlignCenter)
	}), func() { worktree.Set(!ggui.Peek(worktree)) }).Ghost().Pad(4, 8).Radius(8).Name(T("Work in a new worktree")),
		T("A worktree of the folder's repository, beside it, on a new branch: agents in one repository then keep out of each other's files."))
	top := widget.Row(folderMenu(m, l, where), nodeMenu(m, l, where), widget.Spacer(), gitToggle).Gap(4).Align(widget.AlignCenter)
	branchRow := ggui.If(worktree, func() ggui.Widget {
		return widget.Box(ui.TextField(branch).Placeholder(T("Branch, such as agent/fix-login. Empty names one after the repository")).Name(T("Branch")).
			OnSubmit(func(string) { start() })).Pad(0, 4)
	})
	hasErr := ggui.Map(l.Err, func(s string) bool { return s != "" })
	errLine := reveal(hasErr, func() ggui.Widget {
		return widget.Box(widget.TextOf(l.Err).Size(12).Color(skin.DotDown)).Pad(0, 6)
	})
	content := widget.Column(
		top,
		field,
		branchRow,
		widget.Row(agents, launchTeamMenu(m, l, where), widget.Spacer(),
			roundButton("arrow-up", T("Start session"), start, l.Starting)).Gap(6).Align(widget.AlignCenter),
		errLine,
	).Gap(6).Stretch()
	box := widget.Box(content).Fill(skin.Surface1).Border(1, skin.Border).Radius(composerRadius).Pad(8, 12, 8, 12)
	return &lane{child: widget.Box(box).Pad(10, 0, 18, 0)}
}

// launchWhere is what a new session's tab picks besides its prompt: the
// menus are built again only when it changes, not as the prompt is typed.
type launchWhere struct{ NodeID, Project, Agent, Team string }

// folderMenu picks where the new session works: a folder its node ran
// sessions in lately, or one browsed to.
func folderMenu(m *model, l *launchState, where ggui.Readable[launchWhere]) ggui.Widget {
	return ggui.View(where, func(f launchWhere) ggui.Widget {
		name := path.Base(strings.ReplaceAll(f.Project, `\`, "/"))
		if f.Project == "" || name == "." || name == "/" {
			name = T("Home folder")
		}
		var items []ggui.Widget
		for _, dir := range m.recentFolders(f.NodeID, 8) {
			items = append(items, ui.MenuItem(checked(prettyPath(dir), dir == f.Project), func() { l.setProject(dir) }))
		}
		if len(items) > 0 {
			items = append(items, ui.MenuDivider())
		}
		items = append(items, ui.MenuItem(T("Browse…"), func() { m.browseLaunchFolder(l) }))
		return dropMenu("folder", name, false, 320, T("Working directory"), items...)
	})
}

// nodeMenu picks the node the new session runs on, where there are more
// than this one to pick.
func nodeMenu(m *model, l *launchState, where ggui.Readable[launchWhere]) ggui.Widget {
	ids := reachableNodeIDs(m)
	return ggui.Reactive(func() ggui.Widget {
		ids, f, nodes := ids.Get(), where.Get(), m.Nodes.Get()
		if len(ids) < 2 {
			return widget.Box()
		}
		items := make([]ggui.Widget, 0, len(ids))
		for _, id := range ids {
			items = append(items, ui.MenuItem(checked(nodeName(nodes, id), id == f.NodeID), func() {
				l.Form.Update(func(f newSession) newSession {
					if f.NodeID != id {
						f.NodeID, f.Project = id, m.launchProject(id)
						if isShellTarget(f.Agent) {
							f.Agent = m.defaultSessionAgent(id)
						}
					}
					return f
				})
			}))
		}
		return dropMenu("monitor", nodeName(nodes, f.NodeID), false, 240, T("Node"), items...)
	})
}

// setProject sets the new session's folder; "" leaves it.
func (l *launchState) setProject(dir string) {
	if dir != "" {
		l.Form.Update(func(f newSession) newSession { f.Project = dir; return f })
	}
}

// browseLaunchFolder picks the folder of l's session: in this machine's
// own dialog for its folders, and through the daemons for another node's.
func (m *model) browseLaunchFolder(l *launchState) {
	f := ggui.Peek(l.Form)
	if f.NodeID != ggui.Peek(m.Conn).Self.GetId() {
		m.pickFolder(l)
		return
	}
	browseInto(l.setProject, func(d runtime.Dialogs) (string, error) {
		return d.PickFolder(runtime.FileDialog{Title: T("Working directory"), Directory: f.Project})
	})
}

// launchField is the new session's first prompt, which Enter starts it
// with: an agent's, or a command a shell gets typed at its prompt.
func launchField(prompt ggui.Binding[string], shell bool, start func()) *ui.TextFieldWidget {
	hint := T("Ask the agent anything. Enter starts the session, Shift+Enter adds a line")
	if shell {
		hint = T("A command for the shell, typed at its prompt. Enter starts it")
	}
	return ui.TextField(prompt).Lines(2).Placeholder(hint).Name(T("First prompt")).
		OnKey(func(ev ggui.KeyEvent) bool {
			if (ev.Key == ggui.KeyEnter || ev.Key == ggui.KeyNumpadEnter) && !ev.Mods.Shift && !ev.Mods.Alt {
				start()
				return true
			}
			return false
		})
}

// launchTeamMenu picks a team for the new session's agent to lead, started
// with it; nothing without a team to pick.
func launchTeamMenu(m *model, l *launchState, where ggui.Readable[launchWhere]) ggui.Widget {
	return ggui.Reactive(func() ggui.Widget {
		teams, f := startable(m.Prefs.Get().Teams), where.Get()
		if len(teams) == 0 || f.Agent == "shell" {
			return widget.Box()
		}
		set := func(team string) func() {
			return func() { l.Form.Update(func(f newSession) newSession { f.Team = team; return f }) }
		}
		_, on := m.teamNamed(f.Team)
		items := []ggui.Widget{ui.MenuItem(checked(T("No team"), !on), set(""))}
		for _, t := range teams {
			items = append(items, ui.MenuItem(checked(Tf("%s · %d agents", t.Name, len(t.Members)), t.Name == f.Team), set(t.Name)))
		}
		return dropMenu("users", pick(on, f.Team, T("No team")), on, 260, T("Team"), append(items, editTeamsItems(m)...)...)
	})
}
