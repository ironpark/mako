package gui

import (
	"fmt"
	"image/color"
	"slices"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"
	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// dagGraphRow is one row's slice of a DAG graph: the lane its dot sits in,
// how many lanes the row spans, and the line segments passing through it.
type dagGraphRow struct {
	Col   int
	Lanes int
	Lines []dagGraphLine
}

type dagGraphLine struct {
	From, To int
	Lower    bool // from middle to bottom; else top to middle
}

// dagGraph lays DAG rows out in topological lanes, children before their
// parents: each lane carries the row id it waits for, a row takes the lane
// waiting for it, and hands that lane to its first parent.
func dagGraph(items []*protocol.UiRow) []dagGraphRow {
	rows := make([]dagGraphRow, len(items))
	var lanes []string
	free := func(ls []string, not int) int {
		for i, id := range ls {
			if id == "" && i != not {
				return i
			}
		}
		return len(ls)
	}
	put := func(ls []string, i int, id string) []string {
		for len(ls) <= i {
			ls = append(ls, "")
		}
		ls[i] = id
		return ls
	}
	for n, r := range items {
		row := &rows[n]
		rid := r.GetId()
		col := slices.Index(lanes, rid)
		incoming := col >= 0
		if !incoming {
			col = free(lanes, -1)
			lanes = put(lanes, col, rid)
		}
		row.Col = col
		next := slices.Clone(lanes)
		for i, h := range lanes {
			switch {
			case h == "":
			case h == rid:
				if i != col || incoming {
					row.Lines = append(row.Lines, dagGraphLine{From: i, To: col})
				}
				next[i] = "" // every child's line ends at the dot
			default:
				row.Lines = append(row.Lines, dagGraphLine{From: i, To: i}, dagGraphLine{From: i, To: i, Lower: true})
			}
		}
		for k, p := range r.GetParents() {
			j := slices.Index(next, p)
			if j < 0 {
				j = col
				if k > 0 || next[col] != "" {
					j = free(next, col)
				}
				next = put(next, j, p)
			}
			row.Lines = append(row.Lines, dagGraphLine{From: col, To: j, Lower: true})
		}
		for len(next) > 0 && next[len(next)-1] == "" {
			next = next[:len(next)-1]
		}
		row.Lanes = max(len(lanes), len(next), col+1)
		lanes = next
	}
	return rows
}

// dagLaneColors tell the graph's lanes apart, readable on light and dark.
var dagLaneColors = []color.Color{
	skin.Hex(0x3B82F6), skin.Hex(0x22C55E), skin.Hex(0xF59E0B), skin.Hex(0xA855F7), skin.Hex(0xEC4899), skin.Hex(0x06B6D4),
}

const (
	dagLaneWidth = 14
	dagRowHeight = 40
	dagMaxLanes  = 8 // lanes beyond are left out
	dagLineWidth = 2 // the lines' width
	dagDotRadius = 4.5
)

// dagGraphWidth is how many lanes the graph of rows is wide: its widest row's,
// capped at dagMaxLanes, so every row's text starts at one aligned column.
func dagGraphWidth(rows []dagGraphRow) int {
	w := 1
	for _, r := range rows {
		w = max(w, r.Lanes)
	}
	return min(w, dagMaxLanes)
}

// dagGraphCell paints one row's slice of the DAG graph.
type dagGraphCell struct {
	row    dagGraphRow
	lanes  int         // how many lanes wide the graph is
	strong bool        // emphasized node (e.g. HEAD), drawn as a hollow ring
	hole   color.Color // background colour resolved at layout for halo and ring center
}

func (g *dagGraphCell) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	g.hole = ggui.ResolveColor(skin.SolidBg, env)
	return c.Constrain(ggui.Sz(float64(max(g.lanes, 1))*dagLaneWidth+4, dagRowHeight))
}

func (g *dagGraphCell) Paint(dst *ggui.Canvas, r ggui.Rect) {
	x := func(lane int) float64 { return r.Origin.X + 2 + float64(lane)*dagLaneWidth + dagLaneWidth/2 }
	top, mid, bottom := r.Origin.Y, r.Origin.Y+r.Size.H/2, r.Origin.Y+r.Size.H
	// A lane running straight through both halves is drawn as one continuous
	// line so no seam appears at the midpoint.
	through := map[int]bool{}
	for _, l := range g.row.Lines {
		if l.Lower && l.From == l.To && slices.Contains(g.row.Lines, dagGraphLine{From: l.From, To: l.To}) {
			through[l.From] = true
		}
	}
	for _, l := range g.row.Lines {
		if l.From >= dagMaxLanes || l.To >= dagMaxLanes {
			continue
		}
		lane := l.From
		a, b := ggui.Pt(x(l.From), top), ggui.Pt(x(l.To), mid)
		switch {
		case l.From == l.To && through[l.From]:
			if l.Lower {
				continue // drawn with the upper half
			}
			b = ggui.Pt(x(l.To), bottom)
		case l.Lower:
			lane = l.To
			a, b = ggui.Pt(x(l.From), mid), ggui.Pt(x(l.To), bottom)
		}
		strokeDagLane(dst, a, b, dagLaneColors[lane%len(dagLaneColors)])
	}
	if g.row.Col >= dagMaxLanes {
		return
	}
	dot, col := ggui.Pt(x(g.row.Col), mid), dagLaneColors[g.row.Col%len(dagLaneColors)]
	// Background halo sets the dot off from the lane lines it joins.
	dst.FillCircle(dot, dagDotRadius+1.5, g.hole)
	dst.FillCircle(dot, dagDotRadius, col)
	if g.strong {
		dst.FillCircle(dot, dagDotRadius-2, g.hole)
	}
}

// strokeDagLane draws a lane segment from a to b: straight down within a lane,
// or as a vertical-tangent cubic Bezier curve when crossing between lanes.
func strokeDagLane(dst *ggui.Canvas, a, b ggui.Point, col color.Color) {
	if a.X == b.X {
		dst.StrokeLine(a, b, dagLineWidth, col)
		return
	}
	var p ggui.Path
	p.MoveTo(a.X, a.Y)
	m := (a.Y + b.Y) / 2
	p.CubicTo(a.X, m, b.X, m, b.X, b.Y)
	dst.StrokePath(&p, dagLineWidth, col)
}

// dagRowEntry pairs one protocol row with its precomputed lane geometry so
// widget.Each can keep a stable slice signal backed by blockForm.
type dagRowEntry struct {
	row   *protocol.UiRow
	graph dagGraphRow
	lanes int
}

// dagViewState holds persistent row and scroll offset signals for one dag node
// in a blockForm so unrelated BlockUpdates do not jump the commit history scroll.
type dagViewState struct {
	entries *ggui.StateValue[[]dagRowEntry]
	offset  *ggui.StateValue[float64]
}

func equalDagEntries(a, b []dagRowEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].lanes != b[i].lanes ||
			a[i].graph.Col != b[i].graph.Col ||
			a[i].graph.Lanes != b[i].graph.Lanes ||
			!slices.Equal(a[i].graph.Lines, b[i].graph.Lines) ||
			!proto.Equal(a[i].row, b[i].row) {
			return false
		}
	}
	return true
}

func (f *blockForm) dagState(id string, rows []*protocol.UiRow) *dagViewState {
	if f.dags == nil {
		f.dags = map[string]*dagViewState{}
	}
	graph := dagGraph(rows)
	lanes := dagGraphWidth(graph)
	entries := make([]dagRowEntry, len(rows))
	for i, r := range rows {
		entries[i] = dagRowEntry{row: r, graph: graph[i], lanes: lanes}
	}
	if st, ok := f.dags[id]; ok {
		st.entries.Set(entries)
		return st
	}
	st := &dagViewState{
		entries: ggui.State(entries).WithEqual(equalDagEntries),
		offset:  ggui.State(0.0),
	}
	f.dags[id] = st
	return st
}

// dagNode renders a virtualized data-driven DAG (commit graph) node.
func (v *treeView) dagNode(n *protocol.UiNode, bare bool) ggui.Widget {
	rows := n.GetRows()
	if len(rows) == 0 {
		if note := n.GetText(); note != "" {
			return widget.Box(muted(widget.Text(note).Size(12.5))).Pad(14, 16)
		}
		return widget.Box()
	}
	id := n.GetId()
	ds := v.form.dagState(id, rows)
	sel := v.form.local(id+"\x00sel", n.GetValue())

	list := widget.Each(ds.entries, func(item widget.EachItem[dagRowEntry]) ggui.Widget {
		e := item.Item
		return v.dagRowWidget(n, e.row, e.graph, e.lanes, sel, bare)
	}).Stretch().ItemExtent(dagRowHeight)

	var out ggui.Widget = widget.Scroll(widget.Padding(list, 0, 0, 4, 0)).
		Key(v.block + "\x00" + v.scope + "\x00dag\x00" + id).
		BindOffset(ds.offset).
		Bar(skin.Surface3)
	if h := n.GetMaxHeight(); h > 0 {
		out = widget.Box(out).MaxHeight(h)
	}
	return v.maybeDragDrop(out, n, bare)
}

// dagRowWidget builds one virtualized row of a dag node: its lane graph cell,
// primary text, ref badges, detail/meta line, selection highlight, tap event,
// and optional row context menu.
func (v *treeView) dagRowWidget(n *protocol.UiNode, r *protocol.UiRow, g dagGraphRow, lanes int, sel *ggui.StateValue[string], bare bool) ggui.Widget {
	rowID := r.GetId()
	var top []ggui.Widget
	if r.GetIcon() != "" {
		top = append(top, pluginIcon(r.GetIcon()).Size(12).Color(toneColor(cmpTone(r.GetTone(), "muted"))))
	}
	subject := widget.Text(r.GetText()).Size(12.5).Color(toneColor(r.GetTone())).NoWrap().Ellipsis()
	if r.GetStrong() {
		subject = subject.Weight(ggui.WeightMedium)
	}
	top = append(top, widget.Expanded(subject))
	if bs := r.GetBadges(); len(bs) > 0 {
		top = append(top, renderDagBadges(bs)...)
	}

	var lines []ggui.Widget
	lines = append(lines, widget.Row(top...).Gap(5).Align(widget.AlignCenter))
	if r.GetDetail() != "" || r.GetMeta() != "" {
		var bottom []ggui.Widget
		bottom = append(bottom, widget.Expanded(widget.Text(r.GetDetail()).Size(11).Color(skin.Muted).NoWrap().Ellipsis()))
		if r.GetMeta() != "" {
			bottom = append(bottom, chip(r.GetMeta()))
		}
		lines = append(lines, widget.Row(bottom...).Gap(6).Align(widget.AlignCenter))
	}
	textCol := widget.Column(lines...).Gap(2).Stretch()
	content := widget.Row(&dagGraphCell{row: g, lanes: lanes, strong: r.GetStrong()}, widget.Expanded(textCol)).Gap(6).Align(widget.AlignCenter)

	chosen := ggui.Map(sel, func(cur string) bool { return cur != "" && cur == rowID })
	rowBox := ggui.View(chosen, func(on bool) ggui.Widget {
		box := widget.Box(content).Height(dagRowHeight).Pad(0, 10, 0, 6)
		if on {
			box = box.Fill(skin.Surface2)
		}
		return box
	})

	nodeID := n.GetId()
	rowName := cmpTone(r.GetText(), rowID)
	tap := ui.ButtonOf(rowBox, func() {
		if bare || n.GetDisabled() {
			return
		}
		sel.Set(rowID)
		if n.GetChange() {
			v.send(nodeID, protocol.UiChange, rowID)
		}
	}).Ghost().Pad(0).Radius(0).Name(rowName)

	if !bare && n.GetContextMenu() != "" {
		return v.withContextMenu(tap, n, rowID, cmpTone(r.GetMeta(), rowName))
	}
	return tap
}

// renderDagBadges renders up to 2 trailing badges on a DAG row plus a "+N"
// overflow indicator when more are present, matching native refBadges().
func renderDagBadges(badges []*protocol.UiBadge) []ggui.Widget {
	out := make([]ggui.Widget, 0, min(len(badges), 3))
	for i, b := range badges {
		if i == 2 {
			out = append(out, muted(widget.Text(fmt.Sprintf("+%d", len(badges)-i)).Size(10.5)))
			break
		}
		out = append(out, renderBadge(b))
	}
	return out
}
