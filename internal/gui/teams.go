package gui

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// An agent team is agents the user put together once, in Settings ▸
// Teams, to start together: each by a name, an agent and its role, the
// prompt it starts with. A team joins a session from the session's tab,
// its agents started as ones the session spawned, which shows them in its
// tab and has them report to it; or starts whole from the new session's
// tab, under the agent started there, which leads it.

// agentTeam is a team as gui.json keeps it.
type agentTeam struct {
	Name    string       `json:"name"`
	Members []teamMember `json:"members,omitempty"`
}

// teamMember is one agent of a team: its name, the target it runs (an
// agent, or agent:profile), and its role, which it starts with.
type teamMember struct {
	Name  string `json:"name"`
	Agent string `json:"agent"`
	Role  string `json:"role,omitempty"`
}

// teamBrief tells the agent that leads team who its agents are, ahead of
// what it is asked.
func teamBrief(team agentTeam) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Mako team %s: you lead these agents, started as ones you spawned. They report to you with send_message; message them the same way.]\n", team.Name)
	for _, mb := range team.Members {
		fmt.Fprintf(&b, "- %s (%s)", mb.Name, targetLabel(mb.Agent))
		if role := strings.TrimSpace(mb.Role); role != "" {
			fmt.Fprintf(&b, ": %s", strings.ReplaceAll(role, "\n", " "))
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// memberPrompt is what a team's agent starts with: its role, and the
// team's task when it has one.
func memberPrompt(mb teamMember, task string) string {
	role, task := strings.TrimSpace(mb.Role), strings.TrimSpace(task)
	switch {
	case task == "":
		return role
	case role == "":
		return "The team's task:\n" + task
	}
	return role + "\n\nThe team's task:\n" + task
}

// teamNamed is the team of the name, and whether there is one.
func (m *model) teamNamed(name string) (agentTeam, bool) {
	teams := ggui.Peek(m.Prefs).Teams
	if i := slices.IndexFunc(teams, func(t agentTeam) bool { return t.Name == name }); i >= 0 {
		return teams[i], true
	}
	return agentTeam{}, false
}

// startTeam starts team's agents as ones session parent spawned, in its
// folder on its node, each with its role and task; and says which did
// not start.
func (m *model) startTeam(parent string, team agentTeam, task string) {
	s, ok := m.session(parent)
	if !ok {
		return
	}
	node := m.remoteID(s.GetNodeId())
	for _, mb := range team.Members {
		agent, profile := agentspec.SplitTarget(mb.Agent)
		req := &protocol.OpenSession{NodeId: node, Agent: agent, Profile: profile, Project: s.GetProject(), Worktree: s.GetWorktree(),
			Cols: 120, Rows: 36, ParentSessionId: parent, Name: mb.Name, Prompt: memberPrompt(mb, task)}
		fetch(m, 20*time.Second, func(ctx context.Context, c *client.Client) (*protocol.SessionInfo, error) {
			return c.Open(ctx, req)
		}, func(info *protocol.SessionInfo, err error) {
			if err != nil {
				m.toaster.Push(ui.Toast(Tf("%s did not start", mb.Name), err.Error()).Destructive())
				return
			}
			if _, known := m.session(info.GetId()); !known {
				m.Sessions.Set(append(slices.Clone(ggui.Peek(m.Sessions)), info))
			}
		})
	}
}

// startable are the teams with agents to start.
func startable(teams []agentTeam) []agentTeam {
	return slices.DeleteFunc(slices.Clone(teams), func(t agentTeam) bool { return len(t.Members) == 0 })
}

// teamItems are the items of a session's menu that start a team under it.
func teamItems(m *model, id string) []ggui.Widget {
	s, ok := m.session(id)
	if !ok || s.GetAgent() == "shell" || s.GetState() != protocol.StateRunning {
		return nil
	}
	var items []ggui.Widget
	for _, team := range startable(ggui.Peek(m.Prefs).Teams) {
		items = append(items, ui.MenuItem(Tf("Add team %s", team.Name), func() { m.startTeam(id, team, "") }))
	}
	return items
}

// teamMenu is the button of a session's tab that starts a team under it,
// of items; nothing without a team to start.
func teamMenu(m *model, items []ggui.Widget) ggui.Widget {
	if len(items) == 0 {
		return widget.Box()
	}
	return dropMenu("users", T("Team"), false, 240, T("Add a team"), append(items, editTeamsItems(m)...)...)
}

// editTeamsItems end a menu of teams with the way to the teams' settings.
func editTeamsItems(m *model) []ggui.Widget {
	return []ggui.Widget{ui.MenuDivider(), ui.MenuItem(T("Edit teams…"), func() { m.openSettings("teams") })}
}

// dropMenu is a menu whose button is a glyph and a label, in the accent
// when on, and a chevron: what a bar like a composer's picks with.
func dropMenu(glyph, label string, on bool, width float64, name string, items ...ggui.Widget) ggui.Widget {
	chip := widget.Row(icon(glyph).Size(14).Color(pick(on, skin.AccentBright, skin.Muted)),
		widget.Text(label).Size(13).Color(pick(on, skin.Fg, skin.Muted)).NoWrap(),
		icon("chevron-down").Size(12).Color(skin.ExtraMuted)).Gap(6).Align(widget.AlignCenter)
	return iconMenu(ui.MenuOf(widget.Box(chip).Pad(2, 4), items...).Width(width).Name(name), 0)
}

// checked is a menu item's label, ticked when on.
func checked(label string, on bool) string { return pick(on, "✓ ", "") + label }

// Settings ▸ Teams -------------------------------------------------------------

// teamShape is how many teams there are and of how many agents each: the
// page is built again only when it changes, so a field typed in keeps the
// keys.
func teamShape(teams []agentTeam) []int {
	shape := make([]int, len(teams))
	for i, t := range teams {
		shape[i] = len(t.Members)
	}
	return shape
}

func teamSettings(m *model) ggui.Widget {
	shape := ggui.Map(m.Prefs, func(p prefs) []int { return teamShape(p.Teams) }).WithEqual(slices.Equal)
	targets := ggui.Derived(func() []string {
		return nodeTargets(m.Nodes.Get(), m.Conn.Get().Self.GetId(), m.Agents.Get())
	}).WithEqual(slices.Equal)
	list := ggui.View(shape, func(shape []int) ggui.Widget {
		sections := make([]ggui.Widget, 0, len(shape)+1)
		for i, n := range shape {
			sections = append(sections, teamCard(m, i, n, targets))
		}
		add := ui.Button(T("New team"), func() {
			m.Prefs.Update(func(p prefs) prefs {
				p.Teams = append(slices.Clone(p.Teams), agentTeam{Name: Tf("Team %d", len(p.Teams)+1),
					Members: []teamMember{{Name: T("Reviewer"), Agent: cmp.Or(p.DefaultAgent, "claude")}}})
				return p
			})
		}).Outline()
		sections = append(sections, widget.Row(add).Gap(8))
		return widget.Column(sections...).Gap(16).Stretch()
	})
	return page(T("Teams"), T("Agents to start together: from a session's tab, as ones the session spawned, or with a new session, under the agent it starts."), list)
}

// teamCard is team i's card: its name over its agents, n of them.
func teamCard(m *model, i, n int, targets ggui.Readable[[]string]) ggui.Widget {
	team := func(f func(*agentTeam) *string) ggui.Binding[string] {
		return m.Prefs.Field(func(p *prefs) *string { return f(&p.Teams[i]) })
	}
	member := func(j int, f func(*teamMember) *string) ggui.Binding[string] {
		return m.Prefs.Field(func(p *prefs) *string { return f(&p.Teams[i].Members[j]) })
	}
	edit := func(f func(t *agentTeam)) {
		m.Prefs.Update(func(p prefs) prefs {
			p.Teams = slices.Clone(p.Teams)
			t := p.Teams[i]
			t.Members = slices.Clone(t.Members)
			f(&t)
			p.Teams[i] = t
			return p
		})
	}
	name := ui.TextField(team(func(t *agentTeam) *string { return &t.Name })).Placeholder(T("Team name")).Name(T("Team name"))
	remove := ui.Button(T("Delete team"), func() {
		m.Prefs.Update(func(p prefs) prefs { p.Teams = slices.Delete(slices.Clone(p.Teams), i, i+1); return p })
	}).Outline()
	rows := []ggui.Widget{widget.Box(widget.Row(widget.Expanded(name), remove).Gap(12).Align(widget.AlignCenter)).Pad(12, 16)}
	for j := range n {
		agent := ui.Select(member(j, func(mb *teamMember) *string { return &mb.Agent })).BindOptions(targets).Format(targetLabel).Name(T("Agent"))
		who := ui.TextField(member(j, func(mb *teamMember) *string { return &mb.Name })).Placeholder(T("Name")).Name(T("Name"))
		role := ui.TextField(member(j, func(mb *teamMember) *string { return &mb.Role })).Lines(2).
			Placeholder(T("Its role: what it does, and what it reports")).Name(T("Role"))
		drop := iconButton("x", T("Remove agent"), func() { edit(func(t *agentTeam) { t.Members = slices.Delete(t.Members, j, j+1) }) })
		rows = append(rows, widget.Box(widget.Column(
			widget.Row(widget.Box(who).Width(180), widget.Box(agent).Width(200), widget.Spacer(), drop).Gap(8).Align(widget.AlignCenter),
			role,
		).Gap(8).Stretch()).Pad(12, 16))
	}
	add := ui.ButtonOf(widget.Row(icon("plus").Size(14).Color(skin.Muted), widget.Text(T("Add agent")).Size(13).Color(skin.Muted)).Gap(6).Align(widget.AlignCenter),
		func() {
			edit(func(t *agentTeam) {
				t.Members = append(t.Members, teamMember{Name: Tf("Agent %d", len(t.Members)+1), Agent: cmp.Or(ggui.Peek(m.Prefs).DefaultAgent, "claude")})
			})
		}).Ghost().Pad(6, 10).Radius(6).Name(T("Add agent"))
	rows = append(rows, widget.Box(add).Pad(6, 10))
	return card(rows...)
}
