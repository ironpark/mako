package gui

import (
	"slices"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	uitheme "github.com/ironpark/ggui/ui/theme"

	"github.com/ironpark/mako/internal/agentspec"
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
// them instead, and those menus come back apart as well. before runs ahead
// of the launch, as a pane taking focus.
func quickLaunchers(m *model, nodeID, project string, targets []string, before func()) (buttons []ggui.Widget, menus []*ui.MenuWidget) {
	start := func(target string) func() {
		return func() {
			before()
			m.launch(target, nodeID, project)
		}
	}
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
		menu := ui.MenuOf(ggui.Box(face).Pad(6, 3, 6, 6), items...).Width(180).Name(label)
		menus = append(menus, menu)
		// The face pads itself, so the trigger sits level with the buttons.
		buttons = append(buttons, ui.Tooltip(iconMenu(menu, 0), label))
	}
	return buttons, menus
}

// launchButton is the + after a pane's tabs. A click runs more, the
// new-session dialog; the pointer resting on it fans the quick launchers
// out beside it, over the bar, so they take no room of their own: to its
// right, or where the window has no room there, to its left.
func launchButton(m *model, nodeID, project string, targets []string, before, more func()) ggui.Widget {
	plus := iconButton("plus", T("New session…"), more)
	buttons, menus := quickLaunchers(m, nodeID, project, targets, before)
	if len(buttons) == 0 {
		return plus
	}
	f := &launchFan{plus: plus, menus: menus}
	cells := make([]ggui.Widget, len(buttons))
	for i, b := range buttons {
		t := ggui.Transition(b).Fade().Easing(ggui.EaseLinear)
		f.items = append(f.items, t)
		cells[i] = t
	}
	pill := ggui.Reactive(func() ggui.Widget {
		return ggui.Box(ggui.Row(cells...).Gap(2)).Pad(2).
			Fill(tokSolidBg).Border(1, tokBorder).Radius(8).Shadow(uitheme.Use().PanelShadow)
	})
	f.pill = ggui.Transition(pill).Fade().Easing(ggui.EaseLinear)
	return f
}

// launchFan is a + with the quick launchers it fans out while the pointer
// or the keyboard is on it or on them, or one of their menus is open. They
// paint as an overlay, beside the + and over whatever is there.
type launchFan struct {
	plus  ggui.Widget
	pill  *ggui.TransitionWidget
	items []*ggui.TransitionWidget
	menus []*ui.MenuWidget

	plusSize, pillSize ggui.Size
	motion             time.Duration
}

// fanGrace is how long the launchers stay out after the pointer leaves, so
// it can cross from the + to them.
const fanGrace = 250 * time.Millisecond

// fanState is whether the launchers are out, and since when nothing has
// held them there.
type fanState struct {
	open bool
	left time.Time
}

var (
	fanSlot       = ggui.NewSlot[fanState]("launch fan")
	fanMotionSlot = ggui.NewSlot[*ggui.Motion]("launch fan motion")
)

func (f *launchFan) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	f.motion = env.Motion(uitheme.From(env).MotionSlow)
	f.pillSize = f.pill.Layout(ggui.Loose(ggui.Sz(ggui.Unbounded, ggui.Unbounded)), env)
	f.plusSize = f.plus.Layout(c, env)
	return f.plusSize
}

func (f *launchFan) Paint(dst *ggui.Canvas, r ggui.Rect) {
	dst.Paint(f.plus, r)
	// Out to the right, or where the window ends first, to the left.
	right := r.Origin.X + r.Size.W + 2
	w := dst.Size().W
	left := w > 0 && right+f.pillSize.W > w
	x := pick(left, r.Origin.X-2-f.pillSize.W, right)
	at := ggui.Rct(ggui.Pt(x, r.Origin.Y+(r.Size.H-f.pillSize.H)/2), f.pillSize)
	anchor := ggui.Anchor{Rect: r}
	state, _ := dst.Retained(anchor, fanSlot)
	now := ggui.FrameTime()
	p, ok := dst.Pointer()
	// The gap between the + and the launchers counts as theirs.
	held := ok && (r.Contains(p) || state.open && r.Union(at).Contains(p)) ||
		dst.FocusVisibleWithin(r) || state.open && dst.FocusWithin(at) ||
		slices.ContainsFunc(f.menus, func(m *ui.MenuWidget) bool { return m.Popup().IsOpen() })
	switch {
	case held:
		state.open, state.left = true, time.Time{}
	case state.open && state.left.IsZero():
		state.left = now
		ggui.WakeAt(now.Add(fanGrace))
	case state.open && now.Sub(state.left) >= fanGrace:
		state.open = false
	}
	dst.Retain(anchor, fanSlot, state)
	k := dst.Ease(anchor, fanMotionSlot, pick(state.open, 1.0, 0.0), f.motion)
	if k <= 0 {
		return
	}
	// The launchers follow one another out from under the +, a step
	// behind each, the farther from farther: to the left the nearest is
	// the last.
	n := len(f.items)
	dir := pick(left, 1.0, -1.0)
	step := min(.15, .6/float64(max(n-1, 1)))
	span := 1 - step*float64(n-1)
	for i, item := range f.items {
		order := pick(left, n-1-i, i)
		item.Slide(dir*float64(order+1)*14, 0).Progress((k-step*float64(order))/span, !state.open)
	}
	f.pill.Slide(dir*8, 0).Progress(k, !state.open)
	dst.Overlay(func(dst *ggui.Canvas) { dst.Paint(f.pill, at) })
}

// launchProject is where a quick launcher starts a session on nodeID
// without a tab to take the project from: the default working directory on
// this node, and on another the node's own choice, its home.
func (m *model) launchProject(nodeID string) string {
	if nodeID == "" || nodeID == ggui.Peek(m.Conn).Self.GetId() {
		return ggui.Peek(m.Prefs).DefaultProject
	}
	return ""
}
