package gui

import (
	"slices"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	uitheme "github.com/ironpark/ggui/ui/theme"

	"github.com/ironpark/sanbo/internal/agentspec"
)

// launchable is an agent a node can start and its profiles.
type launchable struct {
	Agent    string
	Profiles []string
}

// launchOrder puts the built-ins first, in the order the bar shows them.
var launchOrder = []string{"shell", "claude", "codex", "agy"}

// launchables groups launch targets by agent: the built-ins first, then
// the rest as the node lists them.
func launchables(targets []string) []launchable {
	var out []launchable
	for _, t := range targets {
		agent, profile := agentspec.SplitTarget(t)
		i := slices.IndexFunc(out, func(l launchable) bool { return l.Agent == agent })
		if i < 0 {
			out = append(out, launchable{Agent: agent})
			i = len(out) - 1
		}
		if profile != "" {
			out[i].Profiles = append(out[i].Profiles, profile)
		}
	}
	rank := func(agent string) int {
		if i := slices.Index(launchOrder, agent); i >= 0 {
			return i
		}
		return len(launchOrder)
	}
	slices.SortStableFunc(out, func(a, b launchable) int { return rank(a.Agent) - rank(b.Agent) })
	return out
}

// quickLaunchers are a button for each agent the node can start, which
// starts it in project right away; an agent with profiles opens a menu of
// them instead. before runs ahead of the launch, as a pane taking focus.
func quickLaunchers(m *model, nodeID, project string, targets []string, before func()) ggui.Widget {
	start := func(target string) func() {
		return func() {
			before()
			m.launch(target, nodeID, project)
		}
	}
	var buttons []ggui.Widget
	for _, l := range launchables(targets) {
		label := Tf("New %s session", l.Agent)
		if l.Agent == "shell" {
			label = T("New terminal")
		}
		glyph := agentGlyph(l.Agent, tokMuted)
		if len(l.Profiles) == 0 {
			buttons = append(buttons, ui.Tooltip(ui.ButtonOf(glyph, start(l.Agent)).Ghost().Pad(6).Radius(6).Name(label), label))
			continue
		}
		items := []ggui.Widget{ui.MenuItem(l.Agent, start(l.Agent)), ui.MenuDivider()}
		for _, p := range l.Profiles {
			target := agentspec.Target(l.Agent, p)
			items = append(items, ui.MenuItem(targetLabel(target), start(target)))
		}
		face := ggui.Row(glyph, icon("chevron-down").Size(10)).Gap(1)
		// The face pads itself, so the trigger sits level with the buttons.
		menu := uitheme.Override(func(t *uitheme.Theme) { t.ButtonPad = ggui.Insets(0) },
			ui.MenuOf(ggui.Box(face).Pad(6, 3, 6, 6), items...).Width(180).Name(label))
		buttons = append(buttons, ui.Tooltip(borderless(menu), label))
	}
	return ggui.Row(buttons...).Gap(2)
}

// launchProject is where a quick launcher starts a session on nodeID
// without a tab to take the project from: the default working directory on
// this node, and on another the node's own choice, its home.
func (m *model) launchProject(nodeID string) string {
	if nodeID == "" || nodeID == ggui.Peek(m.Conn).Self.ID {
		return ggui.Peek(m.Prefs).DefaultProject
	}
	return ""
}
