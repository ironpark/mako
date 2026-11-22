package gui

import (
	"hash/fnv"
	"image/color"
	"math"
	"strconv"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	uitheme "github.com/ironpark/ggui/ui/theme"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// graphViewState keeps pan, zoom, hover and cached force-directed layout for
// a graph node across re-renders of its plugin block.
type graphViewState struct {
	panX, panY float64
	zoom       float64
	hoveredID  string

	sig    uint64
	layout graphLayout
}

func newGraphViewState() *graphViewState {
	return &graphViewState{zoom: 1}
}

func (s *graphViewState) reset() {
	s.panX, s.panY, s.zoom = 0, 0, 1
}

// graphLayout holds deterministic world-space coordinates for every node.
type graphLayout struct {
	pos                 map[string]ggui.Point
	minX, maxX          float64
	minY, maxY          float64
	centerX, centerY    float64
	spanW, spanH        float64
}

// graphSignature hashes the topology and weights so layout is recomputed only
// when the graph structure actually changes.
func graphSignature(nodes []*protocol.UiGraphNode, edges []*protocol.UiGraphEdge) uint64 {
	h := fnv.New64a()
	write := func(s string) {
		_, _ = h.Write([]byte(s))
		_, _ = h.Write([]byte{0})
	}
	for _, n := range nodes {
		write(n.GetId())
		write(n.GetGroup())
		write(strconv.FormatFloat(n.GetWeight(), 'g', 6, 64))
	}
	_, _ = h.Write([]byte{1})
	for _, e := range edges {
		write(e.GetFrom())
		write(e.GetTo())
	}
	return h.Sum64()
}

// computeGraphLayout runs a deterministic force-directed layout seeded from
// node IDs (repulsion + spring attraction + group cohesion + center gravity).
func computeGraphLayout(nodes []*protocol.UiGraphNode, edges []*protocol.UiGraphEdge) graphLayout {
	n := len(nodes)
	pos := make(map[string]ggui.Point, n)
	if n == 0 {
		return graphLayout{pos: pos, spanW: 1, spanH: 1}
	}
	if n == 1 {
		pos[nodes[0].GetId()] = ggui.Pt(0, 0)
		return graphLayout{pos: pos, spanW: 1, spanH: 1}
	}

	idxByID := make(map[string]int, n)
	xs := make([]float64, n)
	ys := make([]float64, n)
	groups := make([]string, n)

	// Deterministic initial placement seeded by each node's ID so adding/removing
	// unrelated nodes or re-rendering produces a stable visual layout.
	const goldenAngle = 2.399963229728653
	for i, nd := range nodes {
		id := nd.GetId()
		idxByID[id] = i
		groups[i] = nd.GetGroup()
		h := hashString64(id)
		angle := float64(h%3600)*math.Pi/1800.0 + float64(i)*goldenAngle*0.05
		rad := 1.0 + math.Sqrt(float64(i+1))*0.85 + float64((h>>16)%100)/250.0
		xs[i] = math.Cos(angle) * rad
		ys[i] = math.Sin(angle) * rad
	}

	type edgeIdx struct{ a, b int }
	el := make([]edgeIdx, 0, len(edges))
	for _, e := range edges {
		a, okA := idxByID[e.GetFrom()]
		b, okB := idxByID[e.GetTo()]
		if okA && okB && a != b {
			el = append(el, edgeIdx{a, b})
		}
	}

	// Fruchterman-Reingold style spring-embedder with center gravity.
	const iterations = 120
	area := float64(n) * 4.5
	k := math.Sqrt(area / float64(n))
	if k < 1.2 {
		k = 1.2
	}
	k2 := k * k
	dx := make([]float64, n)
	dy := make([]float64, n)

	for iter := range iterations {
		for i := range n {
			dx[i], dy[i] = 0, 0
		}

		// Pairwise repulsion + subtle same-group cohesion.
		for i := range n {
			for j := i + 1; j < n; j++ {
				vx := xs[i] - xs[j]
				vy := ys[i] - ys[j]
				dist2 := vx*vx + vy*vy
				if dist2 < 1e-4 {
					// Deterministic tiny nudge when two nodes coincide.
					vx = 0.01 * float64((i%3)-1)
					vy = 0.01 * float64(((j+1)%3)-1)
					if vx == 0 && vy == 0 {
						vx = 0.01
					}
					dist2 = vx*vx + vy*vy
				}
				dist := math.Sqrt(dist2)
				rep := k2 / dist
				ux, uy := vx/dist, vy/dist
				dx[i] += ux * rep
				dy[i] += uy * rep
				dx[j] -= ux * rep
				dy[j] -= uy * rep

				if groups[i] != "" && groups[i] == groups[j] {
					coh := dist * 0.06
					dx[i] -= ux * coh
					dy[i] -= uy * coh
					dx[j] += ux * coh
					dy[j] += uy * coh
				}
			}
		}

		// Spring attraction along edges.
		for _, e := range el {
			vx := xs[e.a] - xs[e.b]
			vy := ys[e.a] - ys[e.b]
			dist := math.Hypot(vx, vy)
			if dist < 1e-4 {
				continue
			}
			att := (dist * dist) / k
			ux, uy := vx/dist, vy/dist
			dx[e.a] -= ux * att
			dy[e.a] -= uy * att
			dx[e.b] += ux * att
			dy[e.b] += uy * att
		}

		// Center gravity keeps disconnected components/orphans cleanly framed.
		for i := range n {
			dx[i] -= xs[i] * 0.18
			dy[i] -= ys[i] * 0.18
		}

		// Cooling temperature limits per-step displacement.
		t := float64(iterations-iter) / float64(iterations)
		temp := 0.04 + 0.55*t*t
		for i := range n {
			disp := math.Hypot(dx[i], dy[i])
			if disp > 1e-6 {
				step := min(disp, temp)
				xs[i] += (dx[i] / disp) * step
				ys[i] += (dy[i] / disp) * step
			}
		}
	}

	minX, maxX := xs[0], xs[0]
	minY, maxY := ys[0], ys[0]
	for i := range n {
		if xs[i] < minX {
			minX = xs[i]
		}
		if xs[i] > maxX {
			maxX = xs[i]
		}
		if ys[i] < minY {
			minY = ys[i]
		}
		if ys[i] > maxY {
			maxY = ys[i]
		}
	}
	cx := (minX + maxX) / 2
	cy := (minY + maxY) / 2
	for i, nd := range nodes {
		pos[nd.GetId()] = ggui.Pt(xs[i]-cx, ys[i]-cy)
	}
	spanW := max(maxX-minX, 1.0)
	spanH := max(maxY-minY, 1.0)
	return graphLayout{
		pos:     pos,
		minX:    minX - cx,
		maxX:    maxX - cx,
		minY:    minY - cy,
		maxY:    maxY - cy,
		centerX: 0,
		centerY: 0,
		spanW:   spanW,
		spanH:   spanH,
	}
}

func hashString64(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}

// graphNode builds the interactive graph widget (canvas + zoom overlay + optional caption).
func (v *treeView) graphNode(n *protocol.UiNode) ggui.Widget {
	id := n.GetId()
	st := v.form.graphState(id)
	nodes := n.GetGraphNodes()
	edges := n.GetGraphEdges()
	sig := graphSignature(nodes, edges)
	if st.sig != sig || (len(nodes) > 0 && len(st.layout.pos) != len(nodes)) {
		st.sig = sig
		st.layout = computeGraphLayout(nodes, edges)
	}

	height := n.GetMaxHeight()
	if height <= 0 {
		height = 320
	}
	if height < 120 {
		height = 120
	}
	if height > 1200 {
		height = 1200
	}

	tick := ggui.State(0)
	bump := func() { tick.Update(func(x int) int { return x + 1 }) }

	var onSelect func(string)
	if n.GetChange() && !n.GetDisabled() {
		onSelect = func(nodeID string) {
			v.send(id, protocol.UiChange, nodeID)
		}
	}

	canvas := &pluginGraphCanvas{
		state:      st,
		nodes:      nodes,
		edges:      edges,
		selectedID: n.GetValue(),
		height:     height,
		onSelect:   onSelect,
		tick:       tick,
		name:       cmpTone(n.GetText(), T("Graph")),
	}
	canvas.Role = ggui.RoleGroup
	canvas.SetName(canvas.name)
	canvas.SetKey("graph:" + v.block + ":" + v.scope + ":" + id)
	canvas.SetInert(n.GetDisabled())

	controls := widget.Align(
		widget.Padding(
			widget.Box(widget.Row(
				ui.Tooltip(
					ui.ButtonOf(widget.Text("+").Size(13).Weight(ggui.WeightMedium).Color(skin.Fg).NoWrap(), func() {
						st.zoom = clampFloat(st.zoom*1.25, 0.35, 3.0)
						bump()
					}).Ghost().Pad(2, 6).Radius(5).Name(T("Zoom in")),
					T("Zoom in"),
				),
				ui.Tooltip(
					ui.ButtonOf(widget.Text("−").Size(13).Weight(ggui.WeightMedium).Color(skin.Fg).NoWrap(), func() {
						st.zoom = clampFloat(st.zoom/1.25, 0.35, 3.0)
						bump()
					}).Ghost().Pad(2, 6).Radius(5).Name(T("Zoom out")),
					T("Zoom out"),
				),
				ui.Tooltip(
					ui.ButtonOf(icon("refresh-cw").Size(12).Color(skin.Muted), func() {
						st.reset()
						bump()
					}).Ghost().Pad(4).Radius(5).Name(T("Reset")),
					T("Reset"),
				),
			).Gap(2).Align(widget.AlignCenter)).Fill(skin.Surface0).Border(1, skin.Border).Radius(7).Pad(2),
			8,
		),
	).At(1, 0)

	layered := widget.Stack(canvas, controls)
	if n.GetText() == "" {
		return layered
	}
	return widget.Column(
		widget.Text(n.GetText()).Size(textSize(n.GetSize())).Weight(textWeight(n.GetWeight())).Color(toneColor(cmpTone(n.GetTone(), "muted"))),
		layered,
	).Gap(6).Stretch()
}

type pluginGraphCanvas struct {
	ggui.Interactive
	state      *graphViewState
	nodes      []*protocol.UiGraphNode
	edges      []*protocol.UiGraphEdge
	selectedID string
	height     float64
	onSelect   func(string)
	tick       *ggui.StateValue[int]
	name       string

	env   ggui.Env
	theme *uitheme.Theme
	font  *ggui.Font
	rect  ggui.Rect

	// Resolved theme colors captured in Layout.
	colBg       color.Color
	colSurface0 color.Color
	colBorder   color.Color
	colFg       color.Color
	colMuted    color.Color
	colAccent   color.Color
	colGridDot  color.Color

	// Pointer drag tracking for pan vs click.
	dragging    bool
	moved       bool
	downPos     ggui.Point
	lastPos     ggui.Point
}

func (g *pluginGraphCanvas) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	g.Sync()
	if g.tick != nil {
		_ = g.tick.Get()
	}
	g.env = env
	g.theme = uitheme.Of(env)
	g.font = env.ResolveText(ggui.TextStyle{Size: 11}).Font
	g.colBg = resolveOr(skin.Surface1, env, g.theme.Card)
	g.colSurface0 = resolveOr(skin.Surface0, env, g.theme.Bg)
	g.colBorder = resolveOr(skin.Border, env, g.theme.Border)
	g.colFg = resolveOr(skin.Fg, env, g.theme.Fg)
	g.colMuted = resolveOr(skin.Muted, env, g.theme.MutedFg)
	g.colAccent = resolveOr(skin.AccentBright, env, g.theme.Primary)
	g.colGridDot = graphFade(g.colBorder, 0.55)

	w := c.MaxW
	if math.IsInf(w, 1) || w <= 0 {
		w = 420
	}
	h := g.height
	if !math.IsInf(c.MaxH, 1) && c.MaxH > 0 && h > c.MaxH {
		h = c.MaxH
	}
	return c.Constrain(ggui.Sz(w, h))
}

func (g *pluginGraphCanvas) Describe() ggui.Node {
	return ggui.Node{
		Role:     ggui.RoleGroup,
		Name:     g.name,
		Disabled: g.IsInert(),
	}
}

func (g *pluginGraphCanvas) CaptureDrag() bool      { return !g.IsInert() }
func (g *pluginGraphCanvas) CaptureTouchDrag() bool { return !g.IsInert() }

func (g *pluginGraphCanvas) Adopt(prev any) {
	g.Interactive.Adopt(prev)
	if p, ok := prev.(*pluginGraphCanvas); ok {
		g.dragging = p.dragging
		g.moved = p.moved
		g.downPos = p.downPos
		g.lastPos = p.lastPos
	}
}

// worldToScreen converts a world coordinate from the force layout to canvas logical coordinates.
func (g *pluginGraphCanvas) worldToScreen(r ggui.Rect, pt ggui.Point) ggui.Point {
	scale := g.baseScale(r) * g.state.zoom
	cx := r.Origin.X + r.Size.W/2 + g.state.panX
	cy := r.Origin.Y + r.Size.H/2 + g.state.panY
	return ggui.Pt(cx+(pt.X-g.state.layout.centerX)*scale, cy+(pt.Y-g.state.layout.centerY)*scale)
}

func (g *pluginGraphCanvas) baseScale(r ggui.Rect) float64 {
	availW := max(r.Size.W-96, 40)
	availH := max(r.Size.H-76, 40)
	s := min(availW/max(g.state.layout.spanW, 1), availH/max(g.state.layout.spanH, 1))
	return clampFloat(s, 10, 95)
}

func (g *pluginGraphCanvas) nodeRadius(nd *protocol.UiGraphNode) float64 {
	w := nd.GetWeight()
	if w <= 0 {
		w = 1
	}
	base := clampFloat(7.5+math.Sqrt(w)*4.2, 9, 22)
	return base * clampFloat(math.Sqrt(g.state.zoom), 0.72, 1.45)
}

func (g *pluginGraphCanvas) nodeColor(nd *protocol.UiGraphNode) color.Color {
	if nd.GetTone() != "" {
		return resolveOr(toneColor(nd.GetTone()), g.env, g.colAccent)
	}
	if grp := nd.GetGroup(); grp != "" && g.theme != nil && len(g.theme.Chart) > 0 {
		idx := int(hashString64(grp) % uint64(len(g.theme.Chart)))
		if c := g.theme.Chart[idx]; c != nil {
			return c
		}
	}
	return g.colAccent
}

// hitTestNode returns the topmost node whose circle covers p (in canvas coords), or "".
func (g *pluginGraphCanvas) hitTestNode(r ggui.Rect, p ggui.Point) string {
	for i := len(g.nodes) - 1; i >= 0; i-- {
		nd := g.nodes[i]
		wp, ok := g.state.layout.pos[nd.GetId()]
		if !ok {
			continue
		}
		sp := g.worldToScreen(r, wp)
		rad := max(g.nodeRadius(nd)+4, 14)
		dx := p.X - sp.X
		dy := p.Y - sp.Y
		if dx*dx+dy*dy <= rad*rad {
			return nd.GetId()
		}
	}
	return ""
}

func (g *pluginGraphCanvas) Paint(dst *ggui.Canvas, r ggui.Rect) {
	g.rect = r
	const radius = 8.0
	dst.FillRoundRect(r, radius, g.colBg)

	cursor := ggui.CursorShapeMove
	if g.state.hoveredID != "" {
		cursor = ggui.CursorShapePointer
	}
	g.Hit(dst, r, g, cursor)

	dst.Node(r, g.Describe(), func(dst *ggui.Canvas) {
		dst.ClipRoundRect(r, radius, func(dst *ggui.Canvas) {
			g.paintGrid(dst, r)
			if len(g.nodes) == 0 {
				msg := T("No nodes.")
				tw := dst.TextWidth(msg, g.font, 12)
				dst.DrawText(msg, g.font, 12, ggui.Pt(r.Origin.X+(r.Size.W-tw)/2, r.Origin.Y+r.Size.H/2-7), g.colMuted)
				return
			}
			g.paintEdges(dst, r)
			g.paintNodes(dst, r)
		})
	})

	dst.StrokeRoundRect(r, radius, 1, g.colBorder)
}

func (g *pluginGraphCanvas) paintGrid(dst *ggui.Canvas, r ggui.Rect) {
	step := clampFloat(28*g.state.zoom, 18, 56)
	offX := math.Mod(g.state.panX, step)
	offY := math.Mod(g.state.panY, step)
	if offX < 0 {
		offX += step
	}
	if offY < 0 {
		offY += step
	}
	for x := r.Origin.X + offX; x < r.Origin.X+r.Size.W; x += step {
		for y := r.Origin.Y + offY; y < r.Origin.Y+r.Size.H; y += step {
			dst.FillCircle(ggui.Pt(x, y), 1, g.colGridDot)
		}
	}
}

func (g *pluginGraphCanvas) paintEdges(dst *ggui.Canvas, r ggui.Rect) {
	nodeByID := make(map[string]*protocol.UiGraphNode, len(g.nodes))
	for _, nd := range g.nodes {
		nodeByID[nd.GetId()] = nd
	}
	focus := g.selectedID
	if g.state.hoveredID != "" {
		focus = g.state.hoveredID
	}

	for _, e := range g.edges {
		fromNode, okA := nodeByID[e.GetFrom()]
		toNode, okB := nodeByID[e.GetTo()]
		if !okA || !okB {
			continue
		}
		pA, okA := g.state.layout.pos[e.GetFrom()]
		pB, okB := g.state.layout.pos[e.GetTo()]
		if !okA || !okB {
			continue
		}
		a := g.worldToScreen(r, pA)
		b := g.worldToScreen(r, pB)
		vx, vy := b.X-a.X, b.Y-a.Y
		dist := math.Hypot(vx, vy)
		rA := g.nodeRadius(fromNode) + 2
		rB := g.nodeRadius(toNode) + 3
		if dist <= rA+rB+4 {
			continue
		}
		ux, uy := vx/dist, vy/dist
		start := ggui.Pt(a.X+ux*rA, a.Y+uy*rA)
		end := ggui.Pt(b.X-ux*rB, b.Y-uy*rB)

		incident := focus != "" && (e.GetFrom() == focus || e.GetTo() == focus)
		baseCol := g.colMuted
		if e.GetTone() != "" {
			baseCol = resolveOr(toneColor(e.GetTone()), g.env, g.colMuted)
		}
		alpha := 0.55
		strokeW := 1.4
		if incident {
			alpha = 0.95
			strokeW = 2.0
		} else if focus != "" {
			alpha = 0.28
		}
		col := graphFade(baseCol, alpha)

		if e.GetDashed() {
			drawDashedLine(dst, start, end, 6, 4, strokeW, col)
		} else {
			dst.StrokeLine(start, end, strokeW, col)
		}

		// Directional arrowhead pointing into target node.
		const arrowLen = 7.0
		const arrowHalfW = 3.2
		tip := end
		baseX, baseY := tip.X-ux*arrowLen, tip.Y-uy*arrowLen
		px, py := -uy*arrowHalfW, ux*arrowHalfW
		var arrow ggui.Path
		arrow.MoveTo(tip.X, tip.Y)
		arrow.LineTo(baseX+px, baseY+py)
		arrow.LineTo(baseX-px, baseY-py)
		arrow.Close()
		dst.FillPath(&arrow, col)

		// Short edge label at midpoint when there is room.
		if lbl := e.GetLabel(); lbl != "" && dist > 64 {
			mid := ggui.Pt((start.X+end.X)/2, (start.Y+end.Y)/2)
			tw := dst.TextWidth(lbl, g.font, 9.5)
			padX, padY := 4.0, 2.0
			pillRect := ggui.Rct(ggui.Pt(mid.X-tw/2-padX, mid.Y-6-padY), ggui.Sz(tw+padX*2, 12+padY*2))
			dst.FillRoundRect(pillRect, 4, graphFade(g.colSurface0, 0.88))
			dst.DrawText(lbl, g.font, 9.5, ggui.Pt(mid.X-tw/2, mid.Y-6), graphFade(g.colMuted, max(alpha, 0.75)))
		}
	}
}

func (g *pluginGraphCanvas) paintNodes(dst *ggui.Canvas, r ggui.Rect) {
	// Draw regular nodes first, then hovered/selected on top so their labels are never occluded.
	order := make([]*protocol.UiGraphNode, 0, len(g.nodes))
	var top []*protocol.UiGraphNode
	for _, nd := range g.nodes {
		if nd.GetId() == g.selectedID || nd.GetId() == g.state.hoveredID {
			top = append(top, nd)
		} else {
			order = append(order, nd)
		}
	}
	order = append(order, top...)

	for _, nd := range order {
		wp, ok := g.state.layout.pos[nd.GetId()]
		if !ok {
			continue
		}
		sp := g.worldToScreen(r, wp)
		rad := g.nodeRadius(nd)
		col := g.nodeColor(nd)
		isSelected := nd.GetId() == g.selectedID
		isHovered := nd.GetId() == g.state.hoveredID

		// Outer glow / hover halo.
		if isSelected {
			dst.FillCircle(sp, rad+7, graphFade(col, 0.24))
			var ring ggui.Path
			ring.Arc(sp.X, sp.Y, rad+3.5, 0, 2*math.Pi)
			dst.StrokePath(&ring, 2, col)
		} else if isHovered {
			dst.FillCircle(sp, rad+4.5, graphFade(col, 0.22))
		}

		// Node fill + crisp separator ring.
		dst.FillCircle(sp, rad, col)
		var rim ggui.Path
		rim.Arc(sp.X, sp.Y, rad, 0, 2*math.Pi)
		dst.StrokePath(&rim, 1.5, g.colSurface0)

		// Label pill underneath node.
		label := nd.GetLabel()
		if label != "" {
			const fontSize = 11.0
			fitted := dst.FitText(label, g.font, fontSize, 130)
			tw := dst.TextWidth(fitted, g.font, fontSize)
			padX, padY := 6.0, 2.5
			pillW := tw + padX*2
			pillH := 15.0 + padY*2
			pillRect := ggui.Rct(ggui.Pt(sp.X-pillW/2, sp.Y+rad+5), ggui.Sz(pillW, pillH))
			dst.FillRoundRect(pillRect, 6, graphFade(g.colSurface0, 0.9))
			borderCol := g.colBorder
			if isSelected || isHovered {
				borderCol = col
			}
			dst.StrokeRoundRect(pillRect, 6, 1, borderCol)
			dst.DrawText(fitted, g.font, fontSize, ggui.Pt(pillRect.Origin.X+padX, pillRect.Origin.Y+padY), g.colFg)

			// Show subtitle under label pill when selected or hovered.
			if (isSelected || isHovered) && nd.GetSubtitle() != "" {
				sub := dst.FitText(nd.GetSubtitle(), g.font, 9.5, 140)
				stw := dst.TextWidth(sub, g.font, 9.5)
				subRect := ggui.Rct(ggui.Pt(sp.X-stw/2-5, pillRect.Origin.Y+pillRect.Size.H+2), ggui.Sz(stw+10, 14))
				dst.FillRoundRect(subRect, 4, graphFade(g.colSurface0, 0.88))
				dst.DrawText(sub, g.font, 9.5, ggui.Pt(subRect.Origin.X+5, subRect.Origin.Y+1), g.colMuted)
			}
		}

		// Emit semantic button node for accessibility and Probe hit-test verification.
		hitR := ggui.Rct(ggui.Pt(sp.X-rad, sp.Y-rad), ggui.Sz(rad*2, rad*2))
		dst.Leaf(hitR, ggui.Node{
			Role:     ggui.RoleButton,
			Name:     nd.GetLabel(),
			Selected: isSelected,
			Disabled: g.IsInert() || g.onSelect == nil,
		})
	}
}

func (g *pluginGraphCanvas) HandleKey(ev ggui.KeyEvent) {
	g.Keyboard(ev, nil)
}

func (g *pluginGraphCanvas) HandlePointer(ev ggui.PointerEvent) bool {
	if g.IsInert() {
		return false
	}
	switch ev.Kind {
	case ggui.PointerMove:
		hit := g.hitTestNode(g.rect, ev.Pos)
		if hit != g.state.hoveredID {
			g.state.hoveredID = hit
			ggui.Invalidate(g.env)
		}
		return false
	case ggui.PointerExit:
		if g.state.hoveredID != "" {
			g.state.hoveredID = ""
			ggui.Invalidate(g.env)
		}
		return g.Pointer(ev, nil)
	case ggui.PointerDown:
		if ev.Button != ggui.MouseButtonLeft {
			return false
		}
		g.dragging = true
		g.moved = false
		g.downPos = ev.Pos
		g.lastPos = ev.Pos
		return g.Pointer(ev, nil)
	case ggui.PointerDrag:
		if g.dragging {
			if !g.moved && math.Hypot(ev.Pos.X-g.downPos.X, ev.Pos.Y-g.downPos.Y) > 4 {
				g.moved = true
			}
			if g.moved {
				g.state.panX += ev.Pos.X - g.lastPos.X
				g.state.panY += ev.Pos.Y - g.lastPos.Y
				ggui.Invalidate(g.env)
			}
			g.lastPos = ev.Pos
			return true
		}
	case ggui.PointerUp:
		wasDragging, wasMoved := g.dragging, g.moved
		g.dragging = false
		g.moved = false
		if wasDragging && !wasMoved && ev.Button == ggui.MouseButtonLeft {
			if hit := g.hitTestNode(g.rect, ev.Pos); hit != "" && g.onSelect != nil {
				g.onSelect(hit)
			}
		}
		return g.Pointer(ev, nil)
	case ggui.PointerScroll:
		if ev.Scroll.Y != 0 {
			factor := 1.0 - ev.Scroll.Y*0.08
			if factor < 0.75 {
				factor = 0.75
			}
			if factor > 1.35 {
				factor = 1.35
			}
			g.state.zoom = clampFloat(g.state.zoom*factor, 0.35, 3.0)
			ggui.Invalidate(g.env)
			return true
		}
		return false
	}
	return g.Pointer(ev, nil)
}

func drawDashedLine(dst *ggui.Canvas, a, b ggui.Point, dash, gap, width float64, col color.Color) {
	vx, vy := b.X-a.X, b.Y-a.Y
	total := math.Hypot(vx, vy)
	if total <= 0 {
		return
	}
	ux, uy := vx/total, vy/total
	for d := 0.0; d < total; d += dash + gap {
		end := min(d+dash, total)
		p0 := ggui.Pt(a.X+ux*d, a.Y+uy*d)
		p1 := ggui.Pt(a.X+ux*end, a.Y+uy*end)
		dst.StrokeLine(p0, p1, width, col)
	}
}

func resolveOr(c color.Color, env ggui.Env, fallback color.Color) color.Color {
	if col := ggui.ResolveColor(c, env); col != nil {
		return col
	}
	if fallback != nil {
		return fallback
	}
	return color.Black
}

func graphFade(c color.Color, alpha float64) color.Color {
	if c == nil {
		return color.Transparent
	}
	n := color.NRGBAModel.Convert(c).(color.NRGBA)
	a := clampFloat(float64(n.A)*alpha, 0, 255)
	return color.NRGBA{R: n.R, G: n.G, B: n.B, A: uint8(a)}
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
