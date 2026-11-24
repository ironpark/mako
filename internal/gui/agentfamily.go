package gui

import (
	"cmp"
	"image/color"
	"slices"
	"strconv"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// A session an agent spawned through Mako goes in the tab of the one that
// spawned it, where both are in one pane: the pane's bar shows the
// spawning agent's tab alone, marked with a dot for each agent it spawned,
// and while that tab is shown, a lane under the bar lists the agents in
// it, the first the spawning one, to show one at a time. The layout keeps
// each session's tab as it was, so a tab whose parent leaves the pane, or
// ends, shows in the bar again.

// sessionIndex is the sessions by ID, and by the session that spawned
// them on their node, built once for each read of them.
type sessionIndex struct {
	byID map[string]*protocol.SessionInfo
	kids map[string][]*protocol.SessionInfo // in the order sessions has them
}

func indexSessions(sessions []*protocol.SessionInfo) sessionIndex {
	x := sessionIndex{byID: make(map[string]*protocol.SessionInfo, len(sessions)), kids: map[string][]*protocol.SessionInfo{}}
	for _, s := range sessions {
		x.byID[s.GetId()] = s
	}
	for _, s := range sessions {
		if p := x.parent(s); p != nil {
			x.kids[p.GetId()] = append(x.kids[p.GetId()], s)
		}
	}
	return x
}

// parent is the session that spawned s, on its parent's node (or s's own
// node when parent_node_id is unset); nil for none known.
func (x sessionIndex) parent(s *protocol.SessionInfo) *protocol.SessionInfo {
	p := x.byID[s.GetParentSessionId()]
	if p == nil {
		return nil
	}
	wantNode := cmp.Or(s.GetParentNodeId(), s.GetNodeId())
	if p.GetNodeId() == wantNode {
		return p
	}
	return nil
}

// ancestors are the sessions that spawned session id, the nearest first.
// Sessions that spawned each other, which a daemon should never say, end
// it where it comes round.
func (x sessionIndex) ancestors(id string) []string {
	var out []string
	for s := x.parent(x.byID[id]); s != nil && s.GetId() != id && !slices.Contains(out, s.GetId()); s = x.parent(s) {
		out = append(out, s.GetId())
	}
	return out
}

// hostIn is the tab among tabs that shows session id, other than its own:
// the furthest of its ancestors there; "" for none.
func (x sessionIndex) hostIn(tabs []string, id string) string {
	host := ""
	for _, a := range x.ancestors(id) {
		if slices.Contains(tabs, a) {
			host = a
		}
	}
	return host
}

// tabHosts are, of a pane's tabs, those that show in another's, by each.
func (x sessionIndex) tabHosts(tabs []string) map[string]string {
	hosts := map[string]string{}
	for _, t := range tabs {
		if h := x.hostIn(tabs, t); h != "" {
			hosts[t] = h
		}
	}
	// Sessions that spawned each other would hide each other: both show.
	var cycled []string
	for t, host := range hosts {
		if hosts[host] != "" {
			cycled = append(cycled, t)
		}
	}
	for _, t := range cycled {
		delete(hosts, t)
	}
	return hosts
}

// spawned are those session root spawned, and they spawned, on, nearest
// first.
func (x sessionIndex) spawned(root string) []*protocol.SessionInfo {
	var out []*protocol.SessionInfo
	seen := map[string]bool{root: true}
	for next := x.kids[root]; len(next) > 0; {
		var deeper []*protocol.SessionInfo
		for _, s := range next {
			if !seen[s.GetId()] {
				seen[s.GetId()] = true
				out = append(out, s)
				deeper = append(deeper, x.kids[s.GetId()]...)
			}
		}
		next = deeper
	}
	return out
}

// family is session root and those it spawned, root first.
func (x sessionIndex) family(root string) []*protocol.SessionInfo {
	r := x.byID[root]
	if r == nil {
		return nil
	}
	return append([]*protocol.SessionInfo{r}, x.spawned(root)...)
}

// familyPlace is where in layout root a session in no pane goes: in the
// pane of the furthest of its ancestors a pane has, after the last tab
// shown in that one's; nil for none.
func (x sessionIndex) familyPlace(root *layoutNode, id string) (*tabGroup, int) {
	var host string
	var at *tabGroup
	for _, a := range x.ancestors(id) {
		if g := root.groupOf(a); g != nil {
			host, at = a, g
		}
	}
	if at == nil {
		return nil, 0
	}
	i := slices.Index(at.Tabs, host)
	for j, t := range at.Tabs {
		if x.hostIn(at.Tabs, t) == host {
			i = j
		}
	}
	return at, i + 1
}

// familyMarks are a tab's marks of the agents shown in it beside its own:
// a dot for each, as a tab's own dot says how its agent is, the one
// asking the user first, three at most and then how many more.
func familyMarks(m *model, kids []*protocol.SessionInfo) ggui.Widget {
	// Their order is read once, untracked: the dots follow their agents.
	type kid struct {
		id   string
		rank int
	}
	order := map[dotState]int{dotWaiting: 0, dotWorking: 1, dotDone: 2, dotRest: 3}
	ranked := ggui.Untrack(func() []kid {
		out := make([]kid, len(kids))
		for i, k := range kids {
			out[i] = kid{k.GetId(), order[m.dotOf(k)]}
		}
		return out
	})
	slices.SortStableFunc(ranked, func(a, b kid) int { return a.rank - b.rank })
	var dots []ggui.Widget
	for _, k := range ranked[:min(len(ranked), 3)] {
		dots = append(dots, &activityDot{state: m.dotFor(k.id)})
	}
	if n := len(kids) - 3; n > 0 {
		dots = append(dots, widget.Text("+"+strconv.Itoa(n)).Size(10.5).Color(skin.Muted).NoWrap())
	}
	return widget.Box(widget.Row(dots...).Gap(3).Align(widget.AlignCenter)).Pad(0, 5).Height(15).Radius(7.5).Border(1, skin.BorderAccent)
}

// familyMarksWidth is about how wide familyMarks draws for n agents.
func familyMarksWidth(n int) float64 {
	if n == 0 {
		return 0
	}
	w := float64(min(n, 3))*(dotSize+3) + 10 + 6
	if n > 3 {
		w += 20
	}
	return w
}

// laneHeight is how tall the lane of a tab's agents is.
const laneHeight = 36

// agentLane is the lane under pane id's tabs that lists the agents in the
// tab it shows, when there are several: the one that spawned the rest
// first, then a mark, then those it spawned; and at its end the teams that
// can join them. A tap shows that agent, in its own tab, which the bar
// keeps in the first one's.
func agentLane(m *model, id int, group ggui.Readable[tabGroup]) ggui.Widget {
	return ggui.Reactive(func() ggui.Widget {
		g := group.Get()
		titles := m.Titles.Get()
		m.Prefs.Get() // the teams
		if !isSessionTab(g.Active) {
			return widget.Box()
		}
		x := indexSessions(m.Sessions.Get())
		host := cmp.Or(x.hostIn(g.Tabs, g.Active), g.Active)
		agents := x.family(host)
		// Only a tab of agents spawned has the lane: a session alone is
		// drawn as ever, whatever teams there are to add to it, which its
		// tab's menu offers.
		if len(agents) < 2 {
			return widget.Box()
		}
		var chips []ggui.Widget
		for i, s := range agents {
			sid := s.GetId()
			on := sid == g.Active
			fg := pick[color.Color](on, skin.Fg, skin.Muted)
			title := sessionTitle(s, titles[sid])
			label := widget.Text(title).Size(12.5).Color(fg).NoWrap()
			if on {
				label.Weight(ggui.WeightMedium)
			}
			face := widget.Row(
				agentGlyph(s.GetAgent(), fg).Size(13),
				label,
				&activityDot{state: m.dotFor(sid), quiet: true},
			).Gap(6).Align(widget.AlignCenter)
			chip := ui.ButtonOf(face, func() { m.focusGroup(id); m.showTab(sid) }).Ghost().Pad(4, 9).Radius(7).
				Name(title)
			var w ggui.Widget = chip
			if on {
				w = widget.Box(chip).Fill(skin.Surface2).Radius(7)
			}
			chips = append(chips, w)
			if i == 0 {
				chips = append(chips, widget.Box(icon("git-fork").Size(14).Color(skin.ExtraMuted)).Pad(0, 2))
			}
		}
		row := widget.Scroll(widget.Box(widget.Row(chips...).Gap(2).Align(widget.AlignCenter)).Pad(0, 8)).
			Horizontal().Shrink().Bar(color.Transparent).FadeEdges(20)
		// As tall as the lane and no taller: the session under it has
		// the rest of the pane.
		bar := widget.Row(widget.Expanded(row), widget.Box(teamMenu(m, teamItems(m, host))).Pad(0, 8)).Align(widget.AlignCenter)
		return widget.Column(widget.Box(bar).Height(laneHeight), hline()).Stretch()
	})
}
