package gui

import (
	"cmp"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"image/color"
	"maps"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/ui/icons"
	uitheme "github.com/ironpark/ggui/ui/theme"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/gui/iconset"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

const (
	graphLayoutForce     = "force"
	graphLayoutHierarchy = "hierarchy"
	graphLayoutRadial    = "radial"

	graphCoordLimit = 1e6

	graphDoubleTapWindow = 400 * time.Millisecond
	graphDoubleTapDist   = 8.0
)

var (
	graphIconMu    sync.Mutex
	graphIconCache = map[string]*icons.SVG{}
)

func graphIconSVG(name string) *icons.SVG {
	if name == "" {
		return nil
	}
	graphIconMu.Lock()
	defer graphIconMu.Unlock()
	if svg, ok := graphIconCache[name]; ok {
		return svg
	}
	if !iconset.Has(name) {
		graphIconCache[name] = nil
		return nil
	}
	svg, err := iconset.Load(name)
	if err != nil {
		graphIconCache[name] = nil
		return nil
	}
	graphIconCache[name] = svg
	return svg
}

// graphViewState keeps pan, zoom, hover, layout mode, tweens, pinned positions,
// minimap state, and popup fallback state for a graph node across re-renders.
type graphViewState struct {
	// Logical camera targets (read synchronously by layout & unit tests).
	panX, panY float64
	zoom       float64

	// Persistent reactive owner & TweenOf/TweenOfWith animations (F1.5).
	dispose         func()
	layoutTarget    *ggui.StateValue[map[string]ggui.Point]
	layoutTween     *ggui.Tweened[map[string]ggui.Point]
	camZoomTarget   *ggui.StateValue[float64]
	camZoomTween    *ggui.Tweened[float64]
	camPanTarget    *ggui.StateValue[ggui.Point]
	camPanTween     *ggui.Tweened[ggui.Point]
	camBoundsTarget *ggui.StateValue[graphBounds]
	camBoundsTween  *ggui.Tweened[graphBounds]
	tweenInit       bool

	// Layout & mode state (F1.4, F1.8).
	givenLayout  string // last n.GetGraphLayout()
	layoutMode   string // "force" | "hierarchy" | "radial"
	pinnedPos    map[string]ggui.Point
	givenHasPos  map[string]bool
	sig          uint64
	structSig    uint64
	layout       graphLayout
	camBounds    graphLayout
	hasCamBounds bool
	draggingID   string

	// Selection, focus & double-tap tracking (F1.5, F1.9, F1.10).
	givenSelected string
	localSelected string
	hoveredID     string
	lastTapID     string
	lastTapAt     time.Time
	lastTapPos    ggui.Point
	lastRect      ggui.Rect

	// Minimap & popup fallback state (F1.7, F1.9).
	givenMinimap bool
	minimapOpen  bool
	ctxStates    map[*spaceWindowState]*graphCtxState
}

type graphCtxState struct {
	open  *ggui.StateValue[bool]
	items *ggui.StateValue[[]ggui.Widget]
	popup *widget.PopupWidget
}

func newGraphViewState() *graphViewState {
	st := &graphViewState{
		zoom:        1,
		layoutMode:  graphLayoutForce,
		pinnedPos:   map[string]ggui.Point{},
		givenHasPos: map[string]bool{},
		ctxStates:   map[*spaceWindowState]*graphCtxState{},
	}

	st.dispose = ggui.Root(func() {
		st.layoutTarget = ggui.State(map[string]ggui.Point{}).WithEqual(maps.Equal[map[string]ggui.Point])
		st.layoutTween = ggui.TweenOfWith(st.layoutTarget, 260*time.Millisecond, lerpGraphPos).Easing(ggui.EaseOut)
		st.camZoomTarget = ggui.State(1.0)
		st.camZoomTween = ggui.TweenOf(st.camZoomTarget, 240*time.Millisecond).Easing(ggui.EaseOut)
		st.camPanTarget = ggui.State(ggui.Pt(0, 0))
		st.camPanTween = ggui.TweenOfWith(st.camPanTarget, 240*time.Millisecond, ggui.LerpPoint).Easing(ggui.EaseOut)
		st.camBoundsTarget = ggui.State(graphBounds{})
		st.camBoundsTween = ggui.TweenOfWith(st.camBoundsTarget, 240*time.Millisecond, lerpGraphBounds).Easing(ggui.EaseOut)
	})
	return st
}

func (s *graphViewState) peekCtxState(sw *spaceWindowState) *graphCtxState {
	if s == nil {
		return nil
	}
	return s.ctxStates[sw]
}

func (s *graphViewState) ctxStateFor(sw *spaceWindowState) *graphCtxState {
	if s == nil {
		return nil
	}
	if s.ctxStates == nil {
		s.ctxStates = map[*spaceWindowState]*graphCtxState{}
	}
	if cs := s.ctxStates[sw]; cs != nil {
		return cs
	}
	cs := &graphCtxState{
		open: ggui.State(false),
		items: ggui.State([]ggui.Widget(nil)).WithEqual(func(a, b []ggui.Widget) bool {
			return len(a) == 0 && len(b) == 0
		}),
	}
	s.ctxStates[sw] = cs
	return cs
}

func newGraphCtxPopup(st *graphViewState, sw *spaceWindowState) *widget.PopupWidget {
	if st == nil {
		return nil
	}
	cs := st.ctxStateFor(sw)
	ctxContent := ggui.Reactive(func() ggui.Widget {
		items := cs.items.Get()
		if len(items) == 0 {
			return widget.Box()
		}
		box := widget.Box(widget.Column(items...).Stretch()).
			Fill(skin.Surface2).Border(1, skin.Border).Radius(8).Pad(4)
		return &graphCtxPopupSurface{child: box, open: cs.open}
	})
	return widget.Popup(widget.Box(), ctxContent).Gap(0).
		Side(widget.PopupRight).Align(widget.PopupStart).AlignOffset(4).
		Key(cs).
		BindOpen(cs.open)
}

func (s *graphViewState) close() {
	if s == nil {
		return
	}
	if s.dispose != nil {
		s.dispose()
		s.dispose = nil
	}
	clear(s.ctxStates)
}

func (s *graphViewState) setCamera(panX, panY, zoom float64, animate bool) {
	zoom = clampFloat(zoom, 0.35, 3.0)
	s.panX, s.panY, s.zoom = panX, panY, zoom
	if s.camPanTarget != nil {
		s.camPanTarget.Set(ggui.Pt(panX, panY))
		if !animate && s.camPanTween != nil {
			s.camPanTween.Jump(ggui.Pt(panX, panY))
		}
	}
	if s.camZoomTarget != nil {
		s.camZoomTarget.Set(zoom)
		if !animate && s.camZoomTween != nil {
			s.camZoomTween.Jump(zoom)
		}
	}
}

func (s *graphViewState) setCamBounds(lay graphLayout, animate bool) {
	s.camBounds = lay
	s.hasCamBounds = true
	b := graphBoundsFromLayout(lay)
	if s.camBoundsTarget != nil {
		s.camBoundsTarget.Set(b)
		if !animate && s.camBoundsTween != nil {
			s.camBoundsTween.Jump(b)
		}
	}
}

func (s *graphViewState) cameraLayout() graphLayout {
	if s.hasCamBounds {
		if s.camBoundsTween != nil {
			return s.camBoundsTween.Get().toLayout(s.camBounds.pos)
		}
		return s.camBounds
	}
	return s.layout
}

func (s *graphViewState) reset() {
	s.setCamBounds(s.layout, false)
	s.setCamera(0, 0, 1, false)
}

func (s *graphViewState) renderedCamera() (panX, panY, zoom float64) {
	panX, panY, zoom = s.panX, s.panY, s.zoom
	if s.camPanTween != nil {
		pt := s.camPanTween.Get()
		panX, panY = pt.X, pt.Y
	}
	if s.camZoomTween != nil {
		z := s.camZoomTween.Get()
		if z > 0 {
			zoom = z
		}
	}
	return panX, panY, zoom
}

func (s *graphViewState) renderedPos(id string) (ggui.Point, bool) {
	if id != "" && id == s.draggingID {
		p, ok := s.layout.pos[id]
		return p, ok
	}
	if s.layoutTween != nil {
		if m := s.layoutTween.Get(); m != nil {
			if p, ok := m[id]; ok {
				return p, true
			}
		}
	}
	p, ok := s.layout.pos[id]
	return p, ok
}

func (s *graphViewState) focusNode(id string, r ggui.Rect) {
	if id == "" || r.Size.W <= 0 || r.Size.H <= 0 {
		return
	}
	wp, ok := s.layout.pos[id]
	if !ok {
		return
	}
	cam := s.layout
	if s.hasCamBounds {
		cam = s.camBounds
	}
	z := max(s.zoom, 1.0)
	scale := graphBaseScale(r, cam) * z
	targetPanX := -(wp.X - cam.centerX) * scale
	targetPanY := -(wp.Y - cam.centerY) * scale
	s.setCamera(targetPanX, targetPanY, z, true)
}

func (s *graphViewState) fitView(r ggui.Rect) {
	if r.Size.W <= 0 || r.Size.H <= 0 || len(s.layout.pos) == 0 {
		s.setCamBounds(s.layout, true)
		s.setCamera(0, 0, 1, true)
		return
	}
	s.setCamBounds(s.layout, true)
	// Compute optimal zoom that keeps span comfortably within canvas bounds.
	availW := max(r.Size.W-140, 40)
	availH := max(r.Size.H-120, 40)
	base := graphBaseScale(r, s.layout)
	if base <= 0 {
		base = 1
	}
	fitW := availW / max(s.layout.spanW*base, 1)
	fitH := availH / max(s.layout.spanH*base, 1)
	z := clampFloat(min(fitW, fitH), 0.55, 1.8)
	s.setCamera(0, 0, z, true)
}

func lerpGraphPos(a, b map[string]ggui.Point, t float64) map[string]ggui.Point {
	if len(b) == 0 {
		return nil
	}
	out := make(map[string]ggui.Point, len(b))
	for id, pb := range b {
		if pa, ok := a[id]; ok {
			out[id] = ggui.LerpPoint(pa, pb, t)
		} else {
			out[id] = pb
		}
	}
	return out
}

// graphBounds holds camera-space bounding metrics without the node position map,
// allowing ggui.Tweened to interpolate bounds cleanly during fitView transitions.
type graphBounds struct {
	minX, maxX       float64
	minY, maxY       float64
	centerX, centerY float64
	spanW, spanH     float64
}

func graphBoundsFromLayout(lay graphLayout) graphBounds {
	return graphBounds{
		minX:    lay.minX,
		maxX:    lay.maxX,
		minY:    lay.minY,
		maxY:    lay.maxY,
		centerX: lay.centerX,
		centerY: lay.centerY,
		spanW:   lay.spanW,
		spanH:   lay.spanH,
	}
}

func (b graphBounds) toLayout(pos map[string]ggui.Point) graphLayout {
	return graphLayout{
		pos:     pos,
		minX:    b.minX,
		maxX:    b.maxX,
		minY:    b.minY,
		maxY:    b.maxY,
		centerX: b.centerX,
		centerY: b.centerY,
		spanW:   b.spanW,
		spanH:   b.spanH,
	}
}

func lerpGraphBounds(a, b graphBounds, t float64) graphBounds {
	if t <= 0 {
		return a
	}
	if t >= 1 {
		return b
	}
	lerp := func(x, y float64) float64 { return x + (y-x)*t }
	return graphBounds{
		minX:    lerp(a.minX, b.minX),
		maxX:    lerp(a.maxX, b.maxX),
		minY:    lerp(a.minY, b.minY),
		maxY:    lerp(a.maxY, b.maxY),
		centerX: lerp(a.centerX, b.centerX),
		centerY: lerp(a.centerY, b.centerY),
		spanW:   lerp(a.spanW, b.spanW),
		spanH:   lerp(a.spanH, b.spanH),
	}
}

// graphLayout holds deterministic world-space coordinates for every node.
type graphLayout struct {
	pos              map[string]ggui.Point
	minX, maxX       float64
	minY, maxY       float64
	centerX, centerY float64
	spanW, spanH     float64
}

func graphBaseScale(r ggui.Rect, lay graphLayout) float64 {
	availW := max(r.Size.W-96, 40)
	availH := max(r.Size.H-76, 40)
	s := min(availW/max(lay.spanW, 1), availH/max(lay.spanH, 1))
	if math.IsNaN(s) || math.IsInf(s, 0) || s <= 0 {
		return 10
	}
	return clampFloat(s, 1e-5, 95)
}

func normalizeGraphLayoutMode(mode string) string {
	switch mode {
	case graphLayoutHierarchy, graphLayoutRadial:
		return mode
	default:
		return graphLayoutForce
	}
}

// graphSignature hashes the topology, weights, layout mode, and explicit node positions
// so layout is recomputed only when structure or placement inputs change.
func graphSignature(nodes []*protocol.UiGraphNode, edges []*protocol.UiGraphEdge) uint64 {
	return graphSignatureWithMode(nodes, edges, graphLayoutForce, false)
}

func graphSignatureWithMode(nodes []*protocol.UiGraphNode, edges []*protocol.UiGraphEdge, mode string, isCards bool) uint64 {
	return hashGraphSignature(nodes, edges, mode, isCards, true, false)
}

func graphStructSignatureWithMode(nodes []*protocol.UiGraphNode, edges []*protocol.UiGraphEdge, mode string, isCards bool) uint64 {
	return hashGraphSignature(nodes, edges, mode, isCards, false, true)
}

func hashGraphSignature(nodes []*protocol.UiGraphNode, edges []*protocol.UiGraphEdge, mode string, isCards, includePos, includeLabel bool) uint64 {
	h := fnv.New64a()
	write := func(s string) {
		_, _ = h.Write([]byte(s))
		_, _ = h.Write([]byte{0})
	}
	writeFloat := func(v float64) {
		if v == 0 {
			v = 0
		}
		write(strconv.FormatFloat(v, 'g', -1, 64))
	}
	write(normalizeGraphLayoutMode(mode))
	if isCards {
		write("cards")
	}
	for _, n := range nodes {
		write(n.GetId())
		if includeLabel {
			write(n.GetLabel())
		}
		write(n.GetGroup())
		write(n.GetShape())
		writeFloat(n.GetWeight())
		if includePos && n.GetHasPos() {
			write("pos")
			writeFloat(n.GetX())
			writeFloat(n.GetY())
		}
	}
	_, _ = h.Write([]byte{1})
	for _, e := range edges {
		write(e.GetFrom())
		write(e.GetTo())
		write(e.GetKind())
	}
	return h.Sum64()
}

func refreshGraphBounds(lay graphLayout) graphLayout {
	if len(lay.pos) == 0 {
		lay.minX, lay.maxX, lay.minY, lay.maxY = 0, 0, 0, 0
		lay.centerX, lay.centerY = 0, 0
		lay.spanW, lay.spanH = 1, 1
		return lay
	}
	first := true
	var minX, maxX, minY, maxY float64
	for _, p := range lay.pos {
		if first {
			minX, maxX, minY, maxY = p.X, p.X, p.Y, p.Y
			first = false
		} else {
			minX, maxX = min(minX, p.X), max(maxX, p.X)
			minY, maxY = min(minY, p.Y), max(maxY, p.Y)
		}
	}
	lay.minX, lay.maxX = minX, maxX
	lay.minY, lay.maxY = minY, maxY
	lay.centerX = (minX + maxX) / 2
	lay.centerY = (minY + maxY) / 2
	lay.spanW = max(maxX-minX, 1.0)
	lay.spanH = max(maxY-minY, 1.0)
	return lay
}

// computeGraphLayout runs the default deterministic force-directed layout.
func computeGraphLayout(nodes []*protocol.UiGraphNode, edges []*protocol.UiGraphEdge) graphLayout {
	return computeGraphLayoutWithMode(nodes, edges, graphLayoutForce, nil, false)
}

// computeGraphLayoutWithMode computes deterministic coordinates using the requested
// mode ("force", "hierarchy", "radial") and applies explicit/pinned overrides.
func computeGraphLayoutWithMode(nodes []*protocol.UiGraphNode, edges []*protocol.UiGraphEdge, mode string, pinned map[string]ggui.Point, isCards bool) graphLayout {
	n := len(nodes)
	pos := make(map[string]ggui.Point, n)
	if n == 0 {
		return refreshGraphBounds(graphLayout{pos: pos})
	}
	if n == 1 {
		id := nodes[0].GetId()
		pt := ggui.Pt(0, 0)
		if nodes[0].GetHasPos() {
			pt = ggui.Pt(nodes[0].GetX(), nodes[0].GetY())
		}
		if p, ok := pinned[id]; ok {
			pt = p
		}
		pos[id] = pt
		return refreshGraphBounds(graphLayout{pos: pos})
	}

	idxByID := make(map[string]int, n)
	for i, nd := range nodes {
		idxByID[nd.GetId()] = i
	}

	var xs, ys []float64
	switch normalizeGraphLayoutMode(mode) {
	case graphLayoutHierarchy:
		xs, ys = layoutHierarchy(nodes, edges, idxByID, isCards)
	case graphLayoutRadial:
		xs, ys = layoutRadial(nodes, edges, idxByID, isCards)
	default:
		xs, ys = layoutForce(nodes, edges, idxByID, isCards)
	}

	// Center algorithmic coordinates before applying explicit/pinned positions.
	minX, maxX := xs[0], xs[0]
	minY, maxY := ys[0], ys[0]
	for i := range n {
		minX, maxX = min(minX, xs[i]), max(maxX, xs[i])
		minY, maxY = min(minY, ys[i]), max(maxY, ys[i])
	}
	cx := (minX + maxX) / 2
	cy := (minY + maxY) / 2

	for i, nd := range nodes {
		id := nd.GetId()
		pt := ggui.Pt(xs[i]-cx, ys[i]-cy)
		if nd.GetHasPos() {
			pt = ggui.Pt(nd.GetX(), nd.GetY())
		}
		if p, ok := pinned[id]; ok {
			pt = p
		}
		pos[id] = pt
	}

	return refreshGraphBounds(graphLayout{pos: pos})
}

func layoutForce(nodes []*protocol.UiGraphNode, edges []*protocol.UiGraphEdge, idxByID map[string]int, isCards bool) ([]float64, []float64) {
	n := len(nodes)
	xs := make([]float64, n)
	ys := make([]float64, n)
	groups := make([]string, n)

	const goldenAngle = 2.399963229728653
	for i, nd := range nodes {
		id := nd.GetId()
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

	const iterations = 120
	areaMult := 4.5
	if isCards {
		areaMult = 8.5
	}
	area := float64(n) * areaMult
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

		for i := range n {
			for j := i + 1; j < n; j++ {
				vx := xs[i] - xs[j]
				vy := ys[i] - ys[j]
				dist2 := vx*vx + vy*vy
				if dist2 < 1e-4 {
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

		for i := range n {
			dx[i] -= xs[i] * 0.18
			dy[i] -= ys[i] * 0.18
		}

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
	return xs, ys
}

func layoutHierarchy(nodes []*protocol.UiGraphNode, edges []*protocol.UiGraphEdge, idxByID map[string]int, isCards bool) ([]float64, []float64) {
	n := len(nodes)
	adj := make([][]int, n)
	parents := make([][]int, n)
	inDeg := make([]int, n)
	for _, e := range edges {
		a, okA := idxByID[e.GetFrom()]
		b, okB := idxByID[e.GetTo()]
		if okA && okB && a != b {
			adj[a] = append(adj[a], b)
			parents[b] = append(parents[b], a)
			inDeg[b]++
		}
	}

	rank := make([]int, n)
	remDeg := slices.Clone(inDeg)
	done := make([]bool, n)
	queue := make([]int, 0, n)
	for i := range n {
		if remDeg[i] == 0 {
			queue = append(queue, i)
		}
	}

	processed := 0
	for processed < n {
		if len(queue) == 0 {
			// Deterministic cycle break: pick remaining node with lowest in-degree, highest weight.
			best := -1
			for i := range n {
				if done[i] {
					continue
				}
				if best < 0 || remDeg[i] < remDeg[best] || (remDeg[i] == remDeg[best] && nodes[i].GetWeight() > nodes[best].GetWeight()) {
					best = i
				}
			}
			if best < 0 {
				break
			}
			queue = append(queue, best)
		}
		u := queue[0]
		queue = queue[1:]
		if done[u] {
			continue
		}
		done[u] = true
		processed++
		for _, v := range adj[u] {
			if !done[v] {
				if rank[v] < rank[u]+1 {
					rank[v] = rank[u] + 1
				}
				remDeg[v]--
				if remDeg[v] <= 0 {
					queue = append(queue, v)
				}
			}
		}
	}

	maxRank := 0
	for _, r := range rank {
		if r > maxRank {
			maxRank = r
		}
	}
	layers := make([][]int, maxRank+1)
	for i, r := range rank {
		layers[r] = append(layers[r], i)
	}

	// Deterministic initial ordering by group then ID, followed by 2 barycenter sweeps.
	colPos := make([]float64, n)
	for r := range layers {
		sort.SliceStable(layers[r], func(i, j int) bool {
			ni, nj := nodes[layers[r][i]], nodes[layers[r][j]]
			if ni.GetGroup() != nj.GetGroup() {
				return ni.GetGroup() < nj.GetGroup()
			}
			return ni.GetId() < nj.GetId()
		})
		for c, idx := range layers[r] {
			colPos[idx] = float64(c)
		}
	}
	for range 2 {
		for r := 1; r <= maxRank; r++ {
			bary := make(map[int]float64, len(layers[r]))
			for _, u := range layers[r] {
				if len(parents[u]) == 0 {
					bary[u] = colPos[u]
					continue
				}
				sum := 0.0
				for _, p := range parents[u] {
					sum += colPos[p]
				}
				bary[u] = sum / float64(len(parents[u]))
			}
			sort.SliceStable(layers[r], func(i, j int) bool {
				return bary[layers[r][i]] < bary[layers[r][j]]
			})
			for c, idx := range layers[r] {
				colPos[idx] = float64(c)
			}
		}
	}

	stepX := 2.2
	stepY := 1.9
	if isCards {
		stepX = 3.2
		stepY = 2.4
	}
	xs := make([]float64, n)
	ys := make([]float64, n)
	for r, row := range layers {
		half := float64(len(row)-1) / 2.0
		for c, idx := range row {
			xs[idx] = (float64(c) - half) * stepX
			ys[idx] = float64(r) * stepY
		}
	}
	return xs, ys
}

func layoutRadial(nodes []*protocol.UiGraphNode, edges []*protocol.UiGraphEdge, idxByID map[string]int, isCards bool) ([]float64, []float64) {
	n := len(nodes)
	undir := make([][]int, n)
	inDeg := make([]int, n)
	for _, e := range edges {
		a, okA := idxByID[e.GetFrom()]
		b, okB := idxByID[e.GetTo()]
		if okA && okB && a != b {
			undir[a] = append(undir[a], b)
			undir[b] = append(undir[b], a)
			inDeg[b]++
		}
	}

	// Pick root: lowest in-degree, highest weight/degree.
	root := 0
	for i := 1; i < n; i++ {
		if inDeg[i] < inDeg[root] || (inDeg[i] == inDeg[root] && nodes[i].GetWeight() > nodes[root].GetWeight()) {
			root = i
		}
	}

	dist := make([]int, n)
	for i := range dist {
		dist[i] = -1
	}
	dist[root] = 0
	q := []int{root}
	for len(q) > 0 {
		u := q[0]
		q = q[1:]
		for _, v := range undir[u] {
			if dist[v] < 0 {
				dist[v] = dist[u] + 1
				q = append(q, v)
			}
		}
	}
	maxRing := 0
	for _, d := range dist {
		if d > maxRing {
			maxRing = d
		}
	}
	orphanRing := maxRing + 1
	for i, d := range dist {
		if d < 0 {
			dist[i] = orphanRing
			maxRing = orphanRing
		}
	}

	rings := make([][]int, maxRing+1)
	for i, d := range dist {
		rings[d] = append(rings[d], i)
	}

	stepR := 2.0
	if isCards {
		stepR = 2.9
	}
	xs := make([]float64, n)
	ys := make([]float64, n)
	for r, ring := range rings {
		if r == 0 && len(ring) == 1 {
			xs[ring[0]] = 0
			ys[ring[0]] = 0
			continue
		}
		sort.SliceStable(ring, func(i, j int) bool {
			ni, nj := nodes[ring[i]], nodes[ring[j]]
			if ni.GetGroup() != nj.GetGroup() {
				return ni.GetGroup() < nj.GetGroup()
			}
			return ni.GetId() < nj.GetId()
		})
		rad := float64(r) * stepR
		if r == 0 {
			rad = stepR * 0.65
		}
		for i, idx := range ring {
			ang := 2*math.Pi*float64(i)/float64(len(ring)) - math.Pi/2
			xs[idx] = math.Cos(ang) * rad
			ys[idx] = math.Sin(ang) * rad
		}
	}
	return xs, ys
}

func hashString64(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}

// graphNode builds the interactive graph widget (canvas + layout/camera/minimap overlay + optional caption).
func (v *treeView) graphNode(n *protocol.UiNode) ggui.Widget {
	id := n.GetId()
	st := v.form.graphState(id)
	nodes := n.GetGraphNodes()
	edges := n.GetGraphEdges()
	isCards := n.GetVariant() == "cards"
	for _, nd := range nodes {
		if nd.GetShape() == "card" {
			isCards = true
			break
		}
	}

	// Sync plugin-specified layout mode when it changes from tree.
	givenLayout := n.GetGraphLayout()
	normLayout := normalizeGraphLayoutMode(givenLayout)
	layoutChanged := st.givenLayout != "" && normLayout != normalizeGraphLayoutMode(st.givenLayout)
	if !st.tweenInit && givenLayout != "" {
		st.givenLayout = givenLayout
		st.layoutMode = normLayout
	} else if layoutChanged {
		st.givenLayout = givenLayout
		st.layoutMode = normLayout
	} else if st.givenLayout == "" && givenLayout != "" {
		st.givenLayout = givenLayout
	}
	if st.layoutMode == "" {
		st.layoutMode = graphLayoutForce
	}

	// Sync plugin-specified minimap flag when it changes from tree.
	if n.GetMinimap() != st.givenMinimap {
		st.givenMinimap = n.GetMinimap()
		st.minimapOpen = n.GetMinimap()
	}

	sig := graphSignatureWithMode(nodes, edges, st.layoutMode, isCards)
	structSig := graphStructSignatureWithMode(nodes, edges, st.layoutMode, isCards)
	if st.sig != sig || (len(nodes) > 0 && len(st.layout.pos) != len(nodes)) {
		prevPos := st.layout.pos
		curIDs := make(map[string]bool, len(nodes))
		hasPosCleared := false
		allHasPosMatch := true
		for _, nd := range nodes {
			nid := nd.GetId()
			curIDs[nid] = true
			if st.givenHasPos[nid] && !nd.GetHasPos() {
				hasPosCleared = true
				if !nd.GetPinned() && nid != st.draggingID {
					delete(st.pinnedPos, nid)
				}
			}
			if nd.GetHasPos() {
				p, ok := prevPos[nid]
				if !ok || !(math.Hypot(nd.GetX()-p.X, nd.GetY()-p.Y) <= 0.02) {
					allHasPosMatch = false
				}
			}
		}
		for nid := range st.pinnedPos {
			if !curIDs[nid] {
				delete(st.pinnedPos, nid)
			}
		}
		for nid := range st.givenHasPos {
			if !curIDs[nid] {
				delete(st.givenHasPos, nid)
			}
		}
		if st.draggingID != "" && !curIDs[st.draggingID] {
			st.draggingID = ""
		}

		preserveCamBounds := st.tweenInit &&
			st.hasCamBounds &&
			(st.draggingID != "" || (!layoutChanged && st.structSig == structSig && len(nodes) == len(prevPos) && !hasPosCleared && allHasPosMatch))

		st.sig = sig
		st.structSig = structSig
		if st.givenHasPos == nil {
			st.givenHasPos = make(map[string]bool, len(nodes))
		}
		for _, nd := range nodes {
			st.givenHasPos[nd.GetId()] = nd.GetHasPos()
		}
		st.layout = computeGraphLayoutWithMode(nodes, edges, st.layoutMode, st.pinnedPos, isCards)
		if !preserveCamBounds {
			st.setCamBounds(st.layout, false)
		}
		if st.layoutTarget != nil {
			posCopy := maps.Clone(st.layout.pos)
			st.layoutTarget.Set(posCopy)
			if !st.tweenInit && st.layoutTween != nil {
				st.layoutTween.Jump(posCopy)
			}
			st.tweenInit = true
		}
	}

	// Track selection changes coming from plugin re-render for auto camera focus.
	selectedID := n.GetValue()
	if selectedID != st.givenSelected {
		st.givenSelected = selectedID
		st.localSelected = selectedID
		if selectedID != "" && st.draggingID == "" && st.lastRect.Size.W > 0 && st.lastRect.Size.H > 0 {
			st.focusNode(selectedID, st.lastRect)
		}
	} else if st.localSelected != "" {
		selectedID = st.localSelected
	}

	fill := n.GetFill() || n.GetGrow() > 0
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
	var onDoubleTap func(string)
	if n.GetDoubleTap() && !n.GetDisabled() {
		onDoubleTap = func(nodeID string) {
			v.send(id, protocol.UiDoubleTap, nodeID)
		}
	}
	var onDrop func(string, ggui.Point)
	if n.GetDrop() && !n.GetDisabled() {
		onDrop = func(nodeID string, pos ggui.Point) {
			round2 := func(val float64) float64 {
				clamped := clampGraphCoord(val)
				r := math.Round(clamped*100) / 100
				if r == 0 {
					return 0
				}
				return r
			}
			payload, err := json.Marshal(struct {
				ID string  `json:"id"`
				X  float64 `json:"x"`
				Y  float64 `json:"y"`
			}{
				ID: nodeID,
				X:  round2(pos.X),
				Y:  round2(pos.Y),
			})
			if err != nil {
				return
			}
			v.send(id, protocol.UiDrop, string(payload))
		}
	}

	canvas := &pluginGraphCanvas{
		m:           v.m,
		spaceWindow: v.spaceWindow,
		openSession: v.openSession,
		state:       st,
		variant:     n.GetVariant(),
		nodes:       nodes,
		edges:       edges,
		selectedID:  selectedID,
		height:      height,
		fill:        fill,
		grow:        n.GetGrow(),
		minimapProp: n.GetMinimap(),
		onSelect:    onSelect,
		onDoubleTap: onDoubleTap,
		onDrop:      onDrop,
		pluginMenu: func(nd *protocol.UiGraphNode) ([]ggui.MenuItem, []ggui.Widget) {
			return v.graphNodePluginMenu(n, nd)
		},
		tick: tick,
		name: cmpTone(n.GetText(), T("Graph")),
	}
	canvas.Role = ggui.RoleGroup
	canvas.SetName(canvas.name)
	canvas.SetKey("graph:" + v.block + ":" + v.scope + ":" + id)
	canvas.SetInert(n.GetDisabled())
	sw := v.spaceWindow
	canvas.ctxPopupWidget = ggui.Component(func() ggui.Widget {
		p := newGraphCtxPopup(st, sw)
		cs := st.ctxStateFor(sw)
		canvas.ctxPopup = p
		if cs != nil {
			cs.popup = p
		}
		ggui.OnCleanup(func() {
			if canvas.ctxPopup == p {
				canvas.ctxPopup = nil
			}
			if cs != nil && cs.popup == p {
				cs.popup = nil
			}
		})
		return p
	})

	recomputeLayout := func(mode string) {
		st.layoutMode = mode
		st.sig = graphSignatureWithMode(nodes, edges, mode, isCards)
		st.structSig = graphStructSignatureWithMode(nodes, edges, mode, isCards)
		st.layout = computeGraphLayoutWithMode(nodes, edges, mode, st.pinnedPos, isCards)
		st.setCamBounds(st.layout, false)
		if st.layoutTarget != nil {
			st.layoutTarget.Set(maps.Clone(st.layout.pos))
		}
		bump()
	}

	layoutBtn := func(iconName, label, mode string) ggui.Widget {
		icColor := skin.Muted
		if st.layoutMode == mode {
			icColor = skin.AccentBright
		}
		btn := ui.ButtonOf(icon(iconName).Size(12).Color(icColor), func() {
			recomputeLayout(mode)
		}).Ghost().Pad(4).Radius(5).Disabled(n.GetDisabled()).Name(label)
		if st.layoutMode == mode {
			btn = btn.Secondary()
		}
		return ui.Tooltip(btn, label)
	}

	minimapColor := skin.Muted
	if st.minimapOpen {
		minimapColor = skin.AccentBright
	}

	controls := widget.Align(
		widget.Padding(
			widget.Box(widget.Row(
				layoutBtn("network", T("Force layout"), graphLayoutForce),
				layoutBtn("git-fork", T("Hierarchy layout"), graphLayoutHierarchy),
				layoutBtn("radar", T("Radial layout"), graphLayoutRadial),
				ui.Tooltip(
					ui.ButtonOf(widget.Text("+").Size(13).Weight(ggui.WeightMedium).Color(skin.Fg).NoWrap(), func() {
						_, _, curZoom := st.renderedCamera()
						st.setCamera(st.panX, st.panY, max(st.zoom, curZoom)*1.25, true)
						bump()
					}).Ghost().Pad(2, 6).Radius(5).Disabled(n.GetDisabled()).Name(T("Zoom in")),
					T("Zoom in"),
				),
				ui.Tooltip(
					ui.ButtonOf(widget.Text("−").Size(13).Weight(ggui.WeightMedium).Color(skin.Fg).NoWrap(), func() {
						_, _, curZoom := st.renderedCamera()
						st.setCamera(st.panX, st.panY, min(st.zoom, curZoom)/1.25, true)
						bump()
					}).Ghost().Pad(2, 6).Radius(5).Disabled(n.GetDisabled()).Name(T("Zoom out")),
					T("Zoom out"),
				),
				ui.Tooltip(
					ui.ButtonOf(icon("maximize-2").Size(12).Color(skin.Muted), func() {
						st.fitView(st.lastRect)
						bump()
					}).Ghost().Pad(4).Radius(5).Disabled(n.GetDisabled()).Name(T("Fit view")),
					T("Fit view"),
				),
				ui.Tooltip(
					ui.ButtonOf(icon("refresh-cw").Size(12).Color(skin.Muted), func() {
						st.reset()
						bump()
					}).Ghost().Pad(4).Radius(5).Disabled(n.GetDisabled()).Name(T("Reset")),
					T("Reset"),
				),
				ui.Tooltip(
					ui.ButtonOf(icon("layout-grid").Size(12).Color(minimapColor), func() {
						st.minimapOpen = !st.minimapOpen
						bump()
					}).Ghost().Pad(4).Radius(5).Disabled(n.GetDisabled()).Name(T("Toggle minimap")),
					T("Toggle minimap"),
				),
			).Gap(2).Align(widget.AlignCenter)).Fill(skin.Surface0).Border(1, skin.Border).Radius(7).Pad(2),
			8,
		),
	).At(1, 0)

	layered := ggui.Widget(widget.Stack(canvas, controls))
	if n.GetText() == "" {
		return layered
	}
	if fill {
		layered = widget.Expanded(layered)
	}
	return widget.Column(
		widget.Text(n.GetText()).Size(textSize(n.GetSize())).Weight(textWeight(n.GetWeight())).Color(toneColor(cmpTone(n.GetTone(), "muted"))),
		layered,
	).Gap(6).Stretch()
}

type graphDragMode uint8

const (
	graphDragNone graphDragMode = iota
	graphDragPan
	graphDragNode
	graphDragMinimap
)

type graphCtxPopupSurface struct {
	child ggui.Widget
	open  *ggui.StateValue[bool]
}

func (s *graphCtxPopupSurface) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	return s.child.Layout(c, env)
}

func (s *graphCtxPopupSurface) Paint(dst *ggui.Canvas, r ggui.Rect) {
	open := s.open != nil && ggui.Untrack(s.open.Get)
	dst.Node(r, ggui.Node{
		Role:     ggui.RoleMenu,
		Name:     T("Menu"),
		Expanded: ggui.Expandable(open),
	}, func(dst *ggui.Canvas) {
		dst.Paint(s.child, r)
	})
}

type pluginGraphCanvas struct {
	ggui.Interactive
	m              *model
	spaceWindow    *spaceWindowState
	openSession    func(string)
	state          *graphViewState
	variant        string
	nodes          []*protocol.UiGraphNode
	edges          []*protocol.UiGraphEdge
	selectedID     string
	height         float64
	fill           bool
	grow           float64
	minimapProp    bool
	onSelect       func(string)
	onDoubleTap    func(string)
	onDrop         func(string, ggui.Point)
	pluginMenu     func(*protocol.UiGraphNode) ([]ggui.MenuItem, []ggui.Widget)
	tick           *ggui.StateValue[int]
	name           string
	ctxPopup       *widget.PopupWidget
	ctxPopupWidget ggui.Widget

	env     ggui.Env
	theme   *uitheme.Theme
	font    *ggui.Font
	rect    ggui.Rect
	reduced bool

	// Resolved theme colors captured in Layout.
	colBg       color.Color
	colSurface0 color.Color
	colSurface2 color.Color
	colBorder   color.Color
	colFg       color.Color
	colMuted    color.Color
	colAccent   color.Color
	colGridDot  color.Color
	colRunning  color.Color
	colWaiting  color.Color
	colError    color.Color
	colDone     color.Color

	// Pointer drag tracking for pan vs node drag vs minimap pan.
	dragMode       graphDragMode
	dragNodeID     string
	dragStartWorld ggui.Point
	dragGrabOffset ggui.Point
	panStartX      float64
	panStartY      float64
	moved          bool
	downPos        ggui.Point
	lastPos        ggui.Point
	wantFocus      bool
}

func (g *pluginGraphCanvas) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	g.Sync()
	if g.tick != nil {
		_ = g.tick.Get()
	}
	g.env = env
	g.reduced = env.ReducedMotion()
	g.theme = uitheme.Of(env)
	g.font = env.ResolveText(ggui.TextStyle{Size: 11}).Font
	g.colBg = resolveOr(skin.Surface1, env, g.theme.Card)
	g.colSurface0 = resolveOr(skin.Surface0, env, g.theme.Bg)
	g.colSurface2 = resolveOr(skin.Surface2, env, g.theme.Muted)
	g.colBorder = resolveOr(skin.Border, env, g.theme.Border)
	g.colFg = resolveOr(skin.Fg, env, g.theme.Fg)
	g.colMuted = resolveOr(skin.Muted, env, g.theme.MutedFg)
	g.colAccent = resolveOr(skin.AccentBright, env, g.theme.Primary)
	g.colGridDot = graphFade(g.colBorder, 0.55)
	g.colRunning = resolveOr(skin.DotRunning, env, g.colAccent)
	g.colWaiting = resolveOr(skin.DotWaiting, env, g.colAccent)
	g.colError = resolveOr(skin.DotDown, env, g.colAccent)
	g.colDone = resolveOr(skin.DotDone, env, g.colAccent)

	if g.ctxPopupWidget != nil {
		g.ctxPopupWidget.Layout(ggui.Constraints{}, env)
	} else if cs := g.state.peekCtxState(g.spaceWindow); cs != nil && cs.popup != nil {
		cs.popup.Layout(ggui.Constraints{}, env)
	}

	w := c.MaxW
	if math.IsInf(w, 1) || w <= 0 {
		w = 420
	}
	h := g.height
	if (g.fill || g.grow > 0) && !math.IsInf(c.MaxH, 1) && c.MaxH > 0 {
		h = c.MaxH
	} else if !math.IsInf(c.MaxH, 1) && c.MaxH > 0 && h > c.MaxH {
		h = c.MaxH
	}
	return c.Constrain(ggui.Sz(w, h))
}

func (g *pluginGraphCanvas) Describe() ggui.Node {
	return ggui.Node{
		Role:     ggui.RoleGroup,
		Name:     g.name,
		Disabled: g.IsInert(),
		Actions:  ggui.ActionFocus,
	}
}

func (g *pluginGraphCanvas) CaptureDrag() bool      { return !g.IsInert() }
func (g *pluginGraphCanvas) CaptureTouchDrag() bool { return !g.IsInert() }

func (g *pluginGraphCanvas) ConsumesKey(ev ggui.KeyEvent) bool {
	if g.IsInert() || ev.Kind != ggui.KeyPress {
		return false
	}
	switch ev.Key {
	case ggui.KeyArrowLeft, ggui.KeyArrowRight, ggui.KeyArrowUp, ggui.KeyArrowDown,
		ggui.KeyEnter, ggui.KeyNumpadEnter, ggui.KeySpace:
		return true
	}
	return false
}

func (g *pluginGraphCanvas) Adopt(prev any) {
	g.Interactive.Adopt(prev)
	if p, ok := prev.(*pluginGraphCanvas); ok {
		g.rect = p.rect
		g.dragMode = p.dragMode
		g.dragNodeID = p.dragNodeID
		g.dragStartWorld = p.dragStartWorld
		g.dragGrabOffset = p.dragGrabOffset
		g.panStartX = p.panStartX
		g.panStartY = p.panStartY
		g.moved = p.moved
		g.downPos = p.downPos
		g.lastPos = p.lastPos
		g.wantFocus = p.wantFocus
	}
}

func (g *pluginGraphCanvas) worldToScreen(r ggui.Rect, pt ggui.Point) ggui.Point {
	panX, panY, zoom := g.state.renderedCamera()
	cam := g.state.cameraLayout()
	scale := graphBaseScale(r, cam) * zoom
	cx := r.Origin.X + r.Size.W/2 + panX
	cy := r.Origin.Y + r.Size.H/2 + panY
	return ggui.Pt(cx+(pt.X-cam.centerX)*scale, cy+(pt.Y-cam.centerY)*scale)
}

func (g *pluginGraphCanvas) nodeShape(nd *protocol.UiGraphNode) string {
	if s := nd.GetShape(); s != "" {
		return s
	}
	if g.variant == "cards" {
		return "card"
	}
	return "circle"
}

func (g *pluginGraphCanvas) cardSize(nd *protocol.UiGraphNode) (float64, float64) {
	_, _, zoom := g.state.renderedCamera()
	s := clampFloat(math.Sqrt(zoom), 0.75, 1.35)
	w := 196.0 * s
	h := 64.0 * s
	if nd.GetSubtitle() == "" && len(nd.GetBadges()) == 0 {
		h = 48.0 * s
	} else if len(nd.GetBadges()) > 0 {
		h = 80.0 * s
	}
	return w, h
}

func (g *pluginGraphCanvas) nodeRadius(nd *protocol.UiGraphNode) float64 {
	_, _, zoom := g.state.renderedCamera()
	w := nd.GetWeight()
	if w <= 0 {
		w = 1
	}
	base := clampFloat(7.5+math.Sqrt(w)*4.2, 9, 22)
	return base * clampFloat(math.Sqrt(zoom), 0.72, 1.45)
}

func (g *pluginGraphCanvas) nodeColor(nd *protocol.UiGraphNode) color.Color {
	if nd.GetTone() != "" {
		return resolveOr(toneColor(nd.GetTone()), g.env, g.colAccent)
	}
	if grp := nd.GetGroup(); grp != "" {
		return g.groupColor(grp)
	}
	return g.colAccent
}

func (g *pluginGraphCanvas) groupColor(grp string) color.Color {
	if grp != "" && g.theme != nil && len(g.theme.Chart) > 0 {
		idx := int(hashString64(grp) % uint64(len(g.theme.Chart)))
		if c := g.theme.Chart[idx]; c != nil {
			return c
		}
	}
	return g.colAccent
}

func (g *pluginGraphCanvas) statusColor(status string) color.Color {
	switch status {
	case "working", "running":
		return g.colRunning
	case "waiting":
		return g.colWaiting
	case "error", "down":
		return g.colError
	case "done":
		return g.colDone
	case "compacting":
		return g.colAccent
	default:
		return g.colMuted
	}
}

func (g *pluginGraphCanvas) nodeScreenRect(r ggui.Rect, nd *protocol.UiGraphNode) (ggui.Rect, bool) {
	wp, ok := g.state.renderedPos(nd.GetId())
	if !ok {
		return ggui.Rect{}, false
	}
	sp := g.worldToScreen(r, wp)
	if g.nodeShape(nd) == "card" {
		cw, ch := g.cardSize(nd)
		return ggui.Rct(ggui.Pt(sp.X-cw/2, sp.Y-ch/2), ggui.Sz(cw, ch)), true
	}
	rad := g.nodeRadius(nd)
	return ggui.Rct(ggui.Pt(sp.X-rad, sp.Y-rad), ggui.Sz(rad*2, rad*2)), true
}

func (g *pluginGraphCanvas) hitTestNodeObj(r ggui.Rect, p ggui.Point) *protocol.UiGraphNode {
	for i := len(g.nodes) - 1; i >= 0; i-- {
		nd := g.nodes[i]
		wp, ok := g.state.renderedPos(nd.GetId())
		if !ok {
			continue
		}
		sp := g.worldToScreen(r, wp)
		if g.nodeShape(nd) == "card" {
			cw, ch := g.cardSize(nd)
			cr := ggui.Rct(ggui.Pt(sp.X-cw/2-2, sp.Y-ch/2-2), ggui.Sz(cw+4, ch+4))
			if cr.Contains(p) {
				return nd
			}
		} else {
			rad := max(g.nodeRadius(nd)+4, 14)
			dx := p.X - sp.X
			dy := p.Y - sp.Y
			if dx*dx+dy*dy <= rad*rad {
				return nd
			}
		}
	}
	return nil
}

// hitTestNode returns the topmost node whose shape covers p (in canvas coords), or "".
func (g *pluginGraphCanvas) hitTestNode(r ggui.Rect, p ggui.Point) string {
	if nd := g.hitTestNodeObj(r, p); nd != nil {
		return nd.GetId()
	}
	return ""
}

func (g *pluginGraphCanvas) findNode(id string) *protocol.UiGraphNode {
	if id == "" {
		return nil
	}
	for _, nd := range g.nodes {
		if nd.GetId() == id {
			return nd
		}
	}
	return nil
}

func (g *pluginGraphCanvas) minimapRect(r ggui.Rect) (ggui.Rect, bool) {
	if !g.state.minimapOpen || r.Size.W < 260 || r.Size.H < 200 {
		return ggui.Rect{}, false
	}
	const mmW, mmH, pad = 148.0, 104.0, 10.0
	return ggui.Rct(
		ggui.Pt(r.Origin.X+r.Size.W-mmW-pad, r.Origin.Y+r.Size.H-mmH-pad),
		ggui.Sz(mmW, mmH),
	), true
}

func (g *pluginGraphCanvas) panToMinimapPoint(r, mmRect ggui.Rect, p ggui.Point) {
	if len(g.state.layout.pos) == 0 || mmRect.Size.W <= 0 || mmRect.Size.H <= 0 {
		return
	}
	pad := 10.0
	innerW := max(mmRect.Size.W-pad*2, 1)
	innerH := max(mmRect.Size.H-pad*2, 1)
	spanW := max(g.state.layout.spanW, 1.0)
	spanH := max(g.state.layout.spanH, 1.0)
	mmScale := min(innerW/spanW, innerH/spanH)
	mmCX := mmRect.Origin.X + mmRect.Size.W/2
	mmCY := mmRect.Origin.Y + mmRect.Size.H/2
	wx := g.state.layout.centerX + (p.X-mmCX)/mmScale
	wy := g.state.layout.centerY + (p.Y-mmCY)/mmScale
	_, _, curZoom := g.state.renderedCamera()
	g.state.setCamBounds(g.state.layout, false)
	scale := graphBaseScale(r, g.state.layout) * curZoom
	targetPanX := -(wx - g.state.layout.centerX) * scale
	targetPanY := -(wy - g.state.layout.centerY) * scale
	g.state.setCamera(targetPanX, targetPanY, curZoom, false)
}

func (g *pluginGraphCanvas) activeSelectedID() string {
	if g.state != nil && g.state.localSelected != "" {
		return g.state.localSelected
	}
	return g.selectedID
}

func (g *pluginGraphCanvas) hasLiveMotion() bool {
	for _, nd := range g.nodes {
		switch nd.GetStatus() {
		case "working", "waiting":
			return true
		}
	}
	for _, e := range g.edges {
		if e.GetActive() || e.GetKind() == "agentMessage" {
			return true
		}
	}
	return false
}

func (g *pluginGraphCanvas) hopNeighbors(focusID string) map[string]bool {
	if focusID == "" {
		return nil
	}
	set := map[string]bool{focusID: true}
	for _, e := range g.edges {
		if e.GetFrom() == focusID {
			set[e.GetTo()] = true
		} else if e.GetTo() == focusID {
			set[e.GetFrom()] = true
		}
	}
	return set
}

func (g *pluginGraphCanvas) Paint(dst *ggui.Canvas, r ggui.Rect) {
	g.rect = r
	g.state.lastRect = r
	if g.wantFocus {
		g.wantFocus = false
		dst.RequestFocus(g)
	}
	const radius = 8.0
	dst.FillRoundRect(r, radius, g.colBg)

	cursor := ggui.CursorShapeMove
	if g.state.hoveredID != "" {
		cursor = ggui.CursorShapePointer
	}

	var now time.Time
	if g.hasLiveMotion() && !g.reduced {
		now = ggui.Paced(16 * time.Millisecond)
	}

	dst.DescribeNode(r, g, func(dst *ggui.Canvas) {
		g.Hit(dst, r, g, cursor)
		dst.ClipRoundRect(r, radius, func(dst *ggui.Canvas) {
			g.paintGrid(dst, r)
			if len(g.nodes) == 0 {
				msg := T("No nodes.")
				tw := dst.TextWidth(msg, g.font, 12)
				dst.DrawText(msg, g.font, 12, ggui.Pt(r.Origin.X+(r.Size.W-tw)/2, r.Origin.Y+r.Size.H/2-7), g.colMuted)
				return
			}
			hoverHop := g.hopNeighbors(g.state.hoveredID)
			g.paintGroupHulls(dst, r, hoverHop)
			g.paintEdges(dst, r, now, hoverHop)
			g.paintNodes(dst, r, now, hoverHop)
			g.paintMinimap(dst, r)
			g.paintHoverDetailCard(dst, r)
		})
	})

	if g.ctxPopupWidget != nil {
		dst.Paint(g.ctxPopupWidget, ggui.Rct(r.Origin, ggui.Sz(0, 0)))
	} else if cs := g.state.peekCtxState(g.spaceWindow); cs != nil && cs.popup != nil {
		dst.Paint(cs.popup, ggui.Rct(r.Origin, ggui.Sz(0, 0)))
	}

	dst.StrokeRoundRect(r, radius, 1, g.colBorder)
}

func (g *pluginGraphCanvas) paintGrid(dst *ggui.Canvas, r ggui.Rect) {
	panX, panY, zoom := g.state.renderedCamera()
	step := clampFloat(28*zoom, 18, 56)
	offX := math.Mod(panX, step)
	offY := math.Mod(panY, step)
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

func (g *pluginGraphCanvas) paintGroupHulls(dst *ggui.Canvas, r ggui.Rect, hoverHop map[string]bool) {
	type groupBox struct {
		name  string
		rect  ggui.Rect
		valid bool
		hot   bool
	}
	groups := map[string]*groupBox{}
	var order []string
	for _, nd := range g.nodes {
		grp := nd.GetGroup()
		if grp == "" {
			continue
		}
		nr, ok := g.nodeScreenRect(r, nd)
		if !ok {
			continue
		}
		if g.nodeShape(nd) == "circle" && nd.GetLabel() != "" {
			nr.Size.H += 22
		}
		gb, exists := groups[grp]
		if !exists {
			gb = &groupBox{name: grp, rect: nr, valid: true}
			groups[grp] = gb
			order = append(order, grp)
		} else {
			x0 := min(gb.rect.Origin.X, nr.Origin.X)
			y0 := min(gb.rect.Origin.Y, nr.Origin.Y)
			x1 := max(gb.rect.Origin.X+gb.rect.Size.W, nr.Origin.X+nr.Size.W)
			y1 := max(gb.rect.Origin.Y+gb.rect.Size.H, nr.Origin.Y+nr.Size.H)
			gb.rect = ggui.Rct(ggui.Pt(x0, y0), ggui.Sz(x1-x0, y1-y0))
		}
		if hoverHop != nil && hoverHop[nd.GetId()] {
			gb.hot = true
		}
	}
	for _, grp := range order {
		gb := groups[grp]
		if !gb.valid {
			continue
		}
		alphaScale := 1.0
		if hoverHop != nil && !gb.hot {
			alphaScale = 0.35
		}
		hull := ggui.Rct(
			ggui.Pt(gb.rect.Origin.X-24, gb.rect.Origin.Y-34),
			ggui.Sz(gb.rect.Size.W+48, gb.rect.Size.H+52),
		)
		col := g.groupColor(grp)
		dst.FillRoundRect(hull, 14, graphFade(col, 0.08*alphaScale))
		dst.StrokeRoundRect(hull, 14, 1.0, graphFade(col, 0.28*alphaScale))

		dotCenter := ggui.Pt(hull.Origin.X+14, hull.Origin.Y+15)
		dst.FillCircle(dotCenter, 3.5, graphFade(col, 0.9*alphaScale))
		lbl := dst.FitText(grp, g.font, 10.5, max(hull.Size.W-34, 24))
		dst.DrawText(lbl, g.font, 10.5, ggui.Pt(hull.Origin.X+23, hull.Origin.Y+8), graphFade(g.colMuted, 0.95*alphaScale))
	}
}

func (g *pluginGraphCanvas) paintEdges(dst *ggui.Canvas, r ggui.Rect, now time.Time, hoverHop map[string]bool) {
	nodeByID := make(map[string]*protocol.UiGraphNode, len(g.nodes))
	for _, nd := range g.nodes {
		nodeByID[nd.GetId()] = nd
	}
	focus := g.activeSelectedID()
	if g.state.hoveredID != "" {
		focus = g.state.hoveredID
	}

	type pairKey struct{ a, b string }
	pairCounts := map[pairKey]int{}
	for _, e := range g.edges {
		a, b := e.GetFrom(), e.GetTo()
		if a > b {
			a, b = b, a
		}
		pairCounts[pairKey{a, b}]++
	}
	pairSeen := map[pairKey]int{}

	for _, e := range g.edges {
		fromNode, okA := nodeByID[e.GetFrom()]
		toNode, okB := nodeByID[e.GetTo()]
		if !okA || !okB {
			continue
		}
		pA, okA := g.state.renderedPos(e.GetFrom())
		pB, okB := g.state.renderedPos(e.GetTo())
		if !okA || !okB {
			continue
		}
		a := g.worldToScreen(r, pA)
		b := g.worldToScreen(r, pB)
		vx, vy := b.X-a.X, b.Y-a.Y
		dist := math.Hypot(vx, vy)
		if dist < 4 {
			continue
		}
		ux, uy := vx/dist, vy/dist

		start := g.nodeBoundaryPoint(a, fromNode, ux, uy, 2)
		end := g.nodeBoundaryPoint(b, toNode, -ux, -uy, 3)
		if math.Hypot(end.X-start.X, end.Y-start.Y) < 6 {
			continue
		}

		pk := pairKey{e.GetFrom(), e.GetTo()}
		if pk.a > pk.b {
			pk.a, pk.b = pk.b, pk.a
		}
		idx := pairSeen[pk]
		pairSeen[pk] = idx + 1
		count := pairCounts[pk]

		// Perpendicular bow for parallel/bidirectional edges or gentle curve.
		nx, ny := -uy, ux
		bow := 0.0
		if count > 1 {
			bow = (float64(idx) - float64(count-1)/2.0) * 28.0
			if e.GetFrom() > e.GetTo() {
				bow = -bow
			}
		} else {
			bow = min(dist*0.10, 18.0)
			if e.GetFrom() > e.GetTo() {
				bow = -bow
			}
		}

		var c1, c2 ggui.Point
		if g.state.layoutMode == graphLayoutHierarchy {
			midY := (start.Y + end.Y) / 2
			c1 = ggui.Pt(start.X+nx*bow*0.5, midY)
			c2 = ggui.Pt(end.X+nx*bow*0.5, midY)
		} else {
			c1 = ggui.Pt(start.X+(end.X-start.X)*0.32+nx*bow, start.Y+(end.Y-start.Y)*0.32+ny*bow)
			c2 = ggui.Pt(start.X+(end.X-start.X)*0.68+nx*bow, start.Y+(end.Y-start.Y)*0.68+ny*bow)
		}

		incident := focus != "" && (e.GetFrom() == focus || e.GetTo() == focus)
		isActive := e.GetActive() || e.GetKind() == "agentMessage"
		baseCol := g.colMuted
		if e.GetTone() != "" {
			baseCol = resolveOr(toneColor(e.GetTone()), g.env, g.colMuted)
		} else if isActive {
			baseCol = g.colAccent
		}

		alpha := 0.55
		strokeW := 1.4
		if hoverHop != nil {
			if hoverHop[e.GetFrom()] && hoverHop[e.GetTo()] && (e.GetFrom() == g.state.hoveredID || e.GetTo() == g.state.hoveredID) {
				alpha = 0.95
				strokeW = 2.1
			} else {
				alpha = 0.15
			}
		} else if incident {
			alpha = 0.95
			strokeW = 2.0
		} else if focus != "" {
			alpha = 0.28
		}
		if isActive && (hoverHop == nil || alpha > 0.2) {
			alpha = max(alpha, 0.85)
			strokeW = max(strokeW, 1.9)
		}
		col := graphFade(baseCol, alpha)

		dashOffset := 0.0
		if isActive && !g.reduced {
			phase := math.Mod(float64(now.UnixMilli()), 1200.0) / 1200.0
			dashOffset = phase * 10.0
		}
		if e.GetDashed() {
			drawDashedCubic(dst, start, c1, c2, end, 6, 4, dashOffset, strokeW, col)
		} else {
			var path ggui.Path
			path.MoveTo(start.X, start.Y)
			path.CubicTo(c1.X, c1.Y, c2.X, c2.Y, end.X, end.Y)
			dst.StrokePath(&path, strokeW, col)
		}

		// Flow particle along active edges.
		if isActive {
			t := 0.5
			if !g.reduced {
				t = math.Mod(float64(now.UnixMilli()), 1200.0) / 1200.0
			}
			pt := cubicPoint(start, c1, c2, end, t)
			dst.FillCircle(pt, 4.5, graphFade(baseCol, 0.28*alpha))
			dst.FillCircle(pt, 2.5, graphFade(baseCol, alpha))
		}

		// Tangent arrowhead at t=1.
		tx, ty := 3*(end.X-c2.X), 3*(end.Y-c2.Y)
		tLen := math.Hypot(tx, ty)
		if tLen < 1e-4 {
			tx, ty = end.X-start.X, end.Y-start.Y
			tLen = math.Hypot(tx, ty)
		}
		if tLen > 1e-4 {
			aux, auy := tx/tLen, ty/tLen
			const arrowLen = 7.0
			const arrowHalfW = 3.2
			tip := end
			baseX, baseY := tip.X-aux*arrowLen, tip.Y-auy*arrowLen
			px, py := -auy*arrowHalfW, aux*arrowHalfW
			var arrow ggui.Path
			arrow.MoveTo(tip.X, tip.Y)
			arrow.LineTo(baseX+px, baseY+py)
			arrow.LineTo(baseX-px, baseY-py)
			arrow.Close()
			dst.FillPath(&arrow, col)
		}

		// Short edge label at cubic midpoint t=0.5.
		if lbl := e.GetLabel(); lbl != "" && dist > 64 {
			mid := cubicPoint(start, c1, c2, end, 0.5)
			tw := dst.TextWidth(lbl, g.font, 9.5)
			padX, padY := 4.0, 2.0
			pillRect := ggui.Rct(ggui.Pt(mid.X-tw/2-padX, mid.Y-6-padY), ggui.Sz(tw+padX*2, 12+padY*2))
			dst.FillRoundRect(pillRect, 4, graphFade(g.colSurface0, 0.88))
			dst.DrawText(lbl, g.font, 9.5, ggui.Pt(mid.X-tw/2, mid.Y-6), graphFade(g.colMuted, max(alpha, 0.75)))
		}
	}
}

func (g *pluginGraphCanvas) nodeBoundaryPoint(center ggui.Point, nd *protocol.UiGraphNode, dx, dy, margin float64) ggui.Point {
	if g.nodeShape(nd) == "card" {
		cw, ch := g.cardSize(nd)
		return cardBoundary(center, cw, ch, dx, dy, margin)
	}
	rad := g.nodeRadius(nd) + margin
	return ggui.Pt(center.X+dx*rad, center.Y+dy*rad)
}

func cardBoundary(c ggui.Point, w, h, dx, dy, margin float64) ggui.Point {
	hw := w/2 + margin
	hh := h/2 + margin
	tx := math.Inf(1)
	if math.Abs(dx) > 1e-6 {
		tx = hw / math.Abs(dx)
	}
	ty := math.Inf(1)
	if math.Abs(dy) > 1e-6 {
		ty = hh / math.Abs(dy)
	}
	t := min(tx, ty)
	return ggui.Pt(c.X+dx*t, c.Y+dy*t)
}

func cubicPoint(p0, p1, p2, p3 ggui.Point, t float64) ggui.Point {
	u := 1 - t
	u2 := u * u
	t2 := t * t
	return ggui.Pt(
		u2*u*p0.X+3*u2*t*p1.X+3*u*t2*p2.X+t2*t*p3.X,
		u2*u*p0.Y+3*u2*t*p1.Y+3*u*t2*p2.Y+t2*t*p3.Y,
	)
}

func drawDashedCubic(dst *ggui.Canvas, p0, p1, p2, p3 ggui.Point, dash, gap, offset, width float64, col color.Color) {
	const steps = 28
	pts := make([]ggui.Point, steps+1)
	cum := make([]float64, steps+1)
	pts[0] = p0
	for i := 1; i <= steps; i++ {
		t := float64(i) / float64(steps)
		pts[i] = cubicPoint(p0, p1, p2, p3, t)
		cum[i] = cum[i-1] + math.Hypot(pts[i].X-pts[i-1].X, pts[i].Y-pts[i-1].Y)
	}
	total := cum[steps]
	if total <= 0 {
		return
	}
	period := dash + gap
	posAtDist := func(d float64) ggui.Point {
		d = clampFloat(d, 0, total)
		for i := 1; i <= steps; i++ {
			if cum[i] >= d {
				seg := cum[i] - cum[i-1]
				if seg <= 1e-6 {
					return pts[i]
				}
				f := (d - cum[i-1]) / seg
				return ggui.LerpPoint(pts[i-1], pts[i], f)
			}
		}
		return p3
	}
	startOffset := math.Mod(offset, period)
	for d := -startOffset; d < total; d += period {
		a := max(d, 0)
		b := min(d+dash, total)
		if b <= a {
			continue
		}
		dst.StrokeLine(posAtDist(a), posAtDist(b), width, col)
	}
}

func (g *pluginGraphCanvas) paintNodes(dst *ggui.Canvas, r ggui.Rect, now time.Time, hoverHop map[string]bool) {
	selID := g.activeSelectedID()
	order := make([]*protocol.UiGraphNode, 0, len(g.nodes))
	var top []*protocol.UiGraphNode
	for _, nd := range g.nodes {
		if nd.GetId() == selID || nd.GetId() == g.state.hoveredID {
			top = append(top, nd)
		} else {
			order = append(order, nd)
		}
	}
	order = append(order, top...)

	for _, nd := range order {
		wp, ok := g.state.renderedPos(nd.GetId())
		if !ok {
			continue
		}
		sp := g.worldToScreen(r, wp)
		col := g.nodeColor(nd)
		isSelected := nd.GetId() == selID
		isHovered := nd.GetId() == g.state.hoveredID
		dimAlpha := 1.0
		if hoverHop != nil && !hoverHop[nd.GetId()] {
			dimAlpha = 0.25
		}

		if g.nodeShape(nd) == "card" {
			g.paintCardNode(dst, sp, nd, col, isSelected, isHovered, dimAlpha, now)
		} else {
			g.paintCircleNode(dst, sp, nd, col, isSelected, isHovered, dimAlpha, now)
		}
	}
}

func (g *pluginGraphCanvas) paintLiveRingRect(dst *ggui.Canvas, rect ggui.Rect, radius float64, status string, dimAlpha float64, now time.Time) {
	switch status {
	case "working":
		ringCol := g.colRunning
		if g.reduced {
			dst.StrokeRoundRect(expandRect(rect, 3), radius+3, 1.5, graphFade(ringCol, 0.45*dimAlpha))
			return
		}
		phase := math.Mod(float64(now.UnixMilli()), 1400.0) / 1400.0
		expand := 2.0 + phase*9.0
		alpha := (1.0 - phase) * 0.65 * dimAlpha
		dst.StrokeRoundRect(expandRect(rect, expand), radius+expand*0.7, 2.0, graphFade(ringCol, alpha))
	case "waiting":
		ringCol := g.colWaiting
		if g.reduced {
			dst.StrokeRoundRect(expandRect(rect, 3), radius+3, 1.8, graphFade(ringCol, 0.55*dimAlpha))
			return
		}
		phase := math.Mod(float64(now.UnixMilli()), 1600.0) / 1600.0
		pulse := 0.5 + 0.5*math.Sin(phase*2*math.Pi)
		dst.StrokeRoundRect(expandRect(rect, 2+pulse*3), radius+3, 2.0, graphFade(ringCol, (0.35+0.45*pulse)*dimAlpha))
		expand := 2.0 + phase*8.0
		dst.StrokeRoundRect(expandRect(rect, expand), radius+expand*0.7, 1.5, graphFade(ringCol, (1-phase)*0.5*dimAlpha))
	}
}

func (g *pluginGraphCanvas) paintLiveRingCircle(dst *ggui.Canvas, center ggui.Point, rad float64, status string, dimAlpha float64, now time.Time) {
	switch status {
	case "working":
		ringCol := g.colRunning
		if g.reduced {
			var ring ggui.Path
			ring.Arc(center.X, center.Y, rad+4, 0, 2*math.Pi)
			dst.StrokePath(&ring, 1.5, graphFade(ringCol, 0.45*dimAlpha))
			return
		}
		phase := math.Mod(float64(now.UnixMilli()), 1400.0) / 1400.0
		var ring ggui.Path
		ring.Arc(center.X, center.Y, rad+2+phase*10, 0, 2*math.Pi)
		dst.StrokePath(&ring, 2.0, graphFade(ringCol, (1-phase)*0.65*dimAlpha))
	case "waiting":
		ringCol := g.colWaiting
		if g.reduced {
			var ring ggui.Path
			ring.Arc(center.X, center.Y, rad+4, 0, 2*math.Pi)
			dst.StrokePath(&ring, 1.8, graphFade(ringCol, 0.55*dimAlpha))
			return
		}
		phase := math.Mod(float64(now.UnixMilli()), 1600.0) / 1600.0
		pulse := 0.5 + 0.5*math.Sin(phase*2*math.Pi)
		var glow ggui.Path
		glow.Arc(center.X, center.Y, rad+2.5+pulse*3, 0, 2*math.Pi)
		dst.StrokePath(&glow, 2.0, graphFade(ringCol, (0.35+0.45*pulse)*dimAlpha))
		var ping ggui.Path
		ping.Arc(center.X, center.Y, rad+2+phase*9, 0, 2*math.Pi)
		dst.StrokePath(&ping, 1.5, graphFade(ringCol, (1-phase)*0.5*dimAlpha))
	}
}

func (g *pluginGraphCanvas) paintCardNode(dst *ggui.Canvas, sp ggui.Point, nd *protocol.UiGraphNode, col color.Color, isSelected, isHovered bool, dimAlpha float64, now time.Time) {
	_, _, zoom := g.state.renderedCamera()
	s := clampFloat(math.Sqrt(zoom), 0.75, 1.35)
	cw, ch := g.cardSize(nd)
	cardRect := ggui.Rct(ggui.Pt(sp.X-cw/2, sp.Y-ch/2), ggui.Sz(cw, ch))
	const cardRad = 10.0

	if nd.GetStatus() != "" {
		g.paintLiveRingRect(dst, cardRect, cardRad, nd.GetStatus(), dimAlpha, now)
	}
	if isSelected {
		dst.FillRoundRect(expandRect(cardRect, 4), cardRad+3, graphFade(col, 0.16*dimAlpha))
	} else if isHovered {
		dst.FillRoundRect(expandRect(cardRect, 3), cardRad+2, graphFade(col, 0.12*dimAlpha))
	}

	dst.FillRoundRect(cardRect, cardRad, graphFade(g.colSurface0, max(0.92*dimAlpha, 0.25)))
	borderCol := graphFade(g.colBorder, dimAlpha)
	borderW := 1.0
	if isSelected || isHovered {
		borderCol = graphFade(col, dimAlpha)
		borderW = 1.8
	}
	dst.StrokeRoundRect(cardRect, cardRad, borderW, borderCol)

	pad := 10.0 * s
	iconSize := 24.0 * s
	iconBox := ggui.Rct(ggui.Pt(cardRect.Origin.X+pad, cardRect.Origin.Y+pad), ggui.Sz(iconSize, iconSize))
	dst.FillRoundRect(iconBox, 6*s, graphFade(col, 0.14*dimAlpha))
	iconName := cmp.Or(nd.GetIcon(), "bot")
	if svg := graphIconSVG(iconName); svg != nil {
		innerIcon := ggui.Rct(ggui.Pt(iconBox.Origin.X+5*s, iconBox.Origin.Y+5*s), ggui.Sz(iconSize-10*s, iconSize-10*s))
		svg.Draw(dst, innerIcon, graphFade(col, dimAlpha), 0)
	} else {
		dst.FillCircle(ggui.Pt(iconBox.Origin.X+iconSize/2, iconBox.Origin.Y+iconSize/2), 4*s, graphFade(col, dimAlpha))
	}

	textX := iconBox.Origin.X + iconSize + 8*s
	dotRoom := 0.0
	if nd.GetStatus() != "" {
		dotRoom = 14.0 * s
		dotCenter := ggui.Pt(cardRect.Origin.X+cw-pad-4*s, cardRect.Origin.Y+pad+8*s)
		dst.FillCircle(dotCenter, 3.8*s, graphFade(g.statusColor(nd.GetStatus()), dimAlpha))
	}
	availTextW := max(cw-(textX-cardRect.Origin.X)-pad-dotRoom, 20)
	titleFontSize := 11.5 * s
	title := dst.FitText(nd.GetLabel(), g.font, titleFontSize, availTextW)
	dst.DrawText(title, g.font, titleFontSize, ggui.Pt(textX, cardRect.Origin.Y+pad+1*s), graphFade(g.colFg, dimAlpha))

	if sub := nd.GetSubtitle(); sub != "" {
		subSize := 10.0 * s
		fittedSub := dst.FitText(sub, g.font, subSize, availTextW+dotRoom*0.5)
		dst.DrawText(fittedSub, g.font, subSize, ggui.Pt(textX, cardRect.Origin.Y+pad+16*s), graphFade(g.colMuted, dimAlpha))
	}

	if badges := nd.GetBadges(); len(badges) > 0 {
		badgeY := cardRect.Origin.Y + ch - pad - 15*s
		g.paintCanvasBadges(dst, badges, ggui.Pt(cardRect.Origin.X+pad, badgeY), cw-pad*2, s, dimAlpha)
	}

	dst.Leaf(cardRect, ggui.Node{
		Role:     ggui.RoleButton,
		Name:     nd.GetLabel(),
		Selected: isSelected,
		Disabled: g.IsInert() || g.onSelect == nil,
	})
}

func (g *pluginGraphCanvas) paintCanvasBadges(dst *ggui.Canvas, badges []*protocol.UiBadge, origin ggui.Point, maxW, s, dimAlpha float64) {
	x := origin.X
	limit := min(len(badges), 3)
	fontSize := 9.0 * s
	h := 14.5 * s
	for i := range limit {
		b := badges[i]
		txt := b.GetText()
		if txt == "" {
			continue
		}
		fitted := dst.FitText(txt, g.font, fontSize, maxW*0.45)
		tw := dst.TextWidth(fitted, g.font, fontSize)
		w := tw + 10*s
		if i > 0 && x+w > origin.X+maxW {
			break
		}
		bCol := g.colMuted
		if b.GetTone() != "" {
			bCol = resolveOr(toneColor(b.GetTone()), g.env, g.colMuted)
		}
		pill := ggui.Rct(ggui.Pt(x, origin.Y), ggui.Sz(w, h))
		dst.FillRoundRect(pill, 4*s, graphFade(bCol, 0.16*dimAlpha))
		dst.StrokeRoundRect(pill, 4*s, 0.8, graphFade(bCol, 0.35*dimAlpha))
		dst.DrawText(fitted, g.font, fontSize, ggui.Pt(x+5*s, origin.Y+1.5*s), graphFade(bCol, dimAlpha))
		x += w + 4*s
	}
	if len(badges) > limit && x+22*s <= origin.X+maxW {
		more := fmt.Sprintf("+%d", len(badges)-limit)
		tw := dst.TextWidth(more, g.font, fontSize)
		pill := ggui.Rct(ggui.Pt(x, origin.Y), ggui.Sz(tw+8*s, h))
		dst.FillRoundRect(pill, 4*s, graphFade(g.colSurface2, dimAlpha))
		dst.DrawText(more, g.font, fontSize, ggui.Pt(x+4*s, origin.Y+1.5*s), graphFade(g.colMuted, dimAlpha))
	}
}

func (g *pluginGraphCanvas) paintCircleNode(dst *ggui.Canvas, sp ggui.Point, nd *protocol.UiGraphNode, col color.Color, isSelected, isHovered bool, dimAlpha float64, now time.Time) {
	rad := g.nodeRadius(nd)

	if nd.GetStatus() != "" {
		g.paintLiveRingCircle(dst, sp, rad, nd.GetStatus(), dimAlpha, now)
	}
	if isSelected {
		dst.FillCircle(sp, rad+7, graphFade(col, 0.24*dimAlpha))
		var ring ggui.Path
		ring.Arc(sp.X, sp.Y, rad+3.5, 0, 2*math.Pi)
		dst.StrokePath(&ring, 2, graphFade(col, dimAlpha))
	} else if isHovered {
		dst.FillCircle(sp, rad+4.5, graphFade(col, 0.22*dimAlpha))
	}

	dst.FillCircle(sp, rad, graphFade(col, dimAlpha))
	var rim ggui.Path
	rim.Arc(sp.X, sp.Y, rad, 0, 2*math.Pi)
	dst.StrokePath(&rim, 1.5, graphFade(g.colSurface0, dimAlpha))

	if iconName := nd.GetIcon(); iconName != "" && rad >= 10 {
		if svg := graphIconSVG(iconName); svg != nil {
			sz := rad * 1.1
			svg.Draw(dst, ggui.Rct(ggui.Pt(sp.X-sz/2, sp.Y-sz/2), ggui.Sz(sz, sz)), graphFade(g.colSurface0, dimAlpha), 0)
		}
	}
	if nd.GetStatus() != "" {
		dotPos := ggui.Pt(sp.X+rad*0.72, sp.Y-rad*0.72)
		dst.FillCircle(dotPos, 4.2, graphFade(g.colSurface0, dimAlpha))
		dst.FillCircle(dotPos, 3.0, graphFade(g.statusColor(nd.GetStatus()), dimAlpha))
	}

	label := nd.GetLabel()
	if label != "" {
		const fontSize = 11.0
		fitted := dst.FitText(label, g.font, fontSize, 130)
		tw := dst.TextWidth(fitted, g.font, fontSize)
		padX, padY := 6.0, 2.5
		pillW := tw + padX*2
		pillH := 15.0 + padY*2
		pillRect := ggui.Rct(ggui.Pt(sp.X-pillW/2, sp.Y+rad+5), ggui.Sz(pillW, pillH))
		dst.FillRoundRect(pillRect, 6, graphFade(g.colSurface0, 0.9*dimAlpha))
		borderCol := graphFade(g.colBorder, dimAlpha)
		if isSelected || isHovered {
			borderCol = graphFade(col, dimAlpha)
		}
		dst.StrokeRoundRect(pillRect, 6, 1, borderCol)
		dst.DrawText(fitted, g.font, fontSize, ggui.Pt(pillRect.Origin.X+padX, pillRect.Origin.Y+padY), graphFade(g.colFg, dimAlpha))

		if (isSelected || isHovered) && nd.GetSubtitle() != "" {
			sub := dst.FitText(nd.GetSubtitle(), g.font, 9.5, 140)
			stw := dst.TextWidth(sub, g.font, 9.5)
			subRect := ggui.Rct(ggui.Pt(sp.X-stw/2-5, pillRect.Origin.Y+pillRect.Size.H+2), ggui.Sz(stw+10, 14))
			dst.FillRoundRect(subRect, 4, graphFade(g.colSurface0, 0.88*dimAlpha))
			dst.DrawText(sub, g.font, 9.5, ggui.Pt(subRect.Origin.X+5, subRect.Origin.Y+1), graphFade(g.colMuted, dimAlpha))
		}
	}

	hitR := ggui.Rct(ggui.Pt(sp.X-rad, sp.Y-rad), ggui.Sz(rad*2, rad*2))
	dst.Leaf(hitR, ggui.Node{
		Role:     ggui.RoleButton,
		Name:     nd.GetLabel(),
		Selected: isSelected,
		Disabled: g.IsInert() || g.onSelect == nil,
	})
}

func (g *pluginGraphCanvas) paintMinimap(dst *ggui.Canvas, r ggui.Rect) {
	mmRect, ok := g.minimapRect(r)
	if !ok || len(g.nodes) == 0 {
		return
	}
	dst.FillRoundRect(mmRect, 8, graphFade(g.colSurface0, 0.92))
	dst.StrokeRoundRect(mmRect, 8, 1, g.colBorder)

	pad := 10.0
	innerW := max(mmRect.Size.W-pad*2, 1)
	innerH := max(mmRect.Size.H-pad*2, 1)
	spanW := max(g.state.layout.spanW, 1.0)
	spanH := max(g.state.layout.spanH, 1.0)
	mmScale := min(innerW/spanW, innerH/spanH)
	mmCX := mmRect.Origin.X + mmRect.Size.W/2
	mmCY := mmRect.Origin.Y + mmRect.Size.H/2
	toMM := func(wp ggui.Point) ggui.Point {
		return ggui.Pt(
			mmCX+(wp.X-g.state.layout.centerX)*mmScale,
			mmCY+(wp.Y-g.state.layout.centerY)*mmScale,
		)
	}

	for _, e := range g.edges {
		pA, okA := g.state.renderedPos(e.GetFrom())
		pB, okB := g.state.renderedPos(e.GetTo())
		if !okA || !okB {
			continue
		}
		dst.StrokeLine(toMM(pA), toMM(pB), 0.8, graphFade(g.colMuted, 0.45))
	}
	selID := g.activeSelectedID()
	for _, nd := range g.nodes {
		wp, ok := g.state.renderedPos(nd.GetId())
		if !ok {
			continue
		}
		mp := toMM(wp)
		rad := 2.4
		if nd.GetId() == selID {
			rad = 3.6
		}
		dst.FillCircle(mp, rad, g.nodeColor(nd))
	}

	// Current camera viewport rect in minimap coordinates.
	panX, panY, zoom := g.state.renderedCamera()
	cam := g.state.cameraLayout()
	scale := graphBaseScale(r, cam) * zoom
	if scale > 0 {
		wMinX := cam.centerX + (-r.Size.W/2-panX)/scale
		wMaxX := cam.centerX + (r.Size.W/2-panX)/scale
		wMinY := cam.centerY + (-r.Size.H/2-panY)/scale
		wMaxY := cam.centerY + (r.Size.H/2-panY)/scale
		p0 := toMM(ggui.Pt(wMinX, wMinY))
		p1 := toMM(ggui.Pt(wMaxX, wMaxY))
		vx0 := clampFloat(p0.X, mmRect.Origin.X+2, mmRect.Origin.X+mmRect.Size.W-2)
		vy0 := clampFloat(p0.Y, mmRect.Origin.Y+2, mmRect.Origin.Y+mmRect.Size.H-2)
		vx1 := clampFloat(p1.X, mmRect.Origin.X+2, mmRect.Origin.X+mmRect.Size.W-2)
		vy1 := clampFloat(p1.Y, mmRect.Origin.Y+2, mmRect.Origin.Y+mmRect.Size.H-2)
		if vx1 > vx0+2 && vy1 > vy0+2 {
			vp := ggui.Rct(ggui.Pt(vx0, vy0), ggui.Sz(vx1-vx0, vy1-vy0))
			dst.FillRoundRect(vp, 3, graphFade(g.colAccent, 0.12))
			dst.StrokeRoundRect(vp, 3, 1.1, graphFade(g.colAccent, 0.85))
		}
	}
}

func (g *pluginGraphCanvas) paintHoverDetailCard(dst *ggui.Canvas, r ggui.Rect) {
	if g.state.hoveredID == "" || g.dragMode != graphDragNone {
		return
	}
	var nd *protocol.UiGraphNode
	for _, x := range g.nodes {
		if x.GetId() == g.state.hoveredID {
			nd = x
			break
		}
	}
	if nd == nil {
		return
	}
	nr, ok := g.nodeScreenRect(r, nd)
	if !ok {
		return
	}

	const cardW = 216.0
	cardH := 52.0
	if nd.GetSubtitle() != "" {
		cardH += 16
	}
	if nd.GetGroup() != "" || nd.GetStatus() != "" {
		cardH += 16
	}
	if len(nd.GetBadges()) > 0 {
		cardH += 20
	}

	x := nr.Origin.X + nr.Size.W + 10
	if x+cardW > r.Origin.X+r.Size.W-8 {
		x = nr.Origin.X - cardW - 10
	}
	x = clampFloat(x, r.Origin.X+8, max(r.Origin.X+8, r.Origin.X+r.Size.W-cardW-8))
	y := clampFloat(nr.Origin.Y, r.Origin.Y+8, max(r.Origin.Y+8, r.Origin.Y+r.Size.H-cardH-8))

	card := ggui.Rct(ggui.Pt(x, y), ggui.Sz(cardW, cardH))
	dst.FillRoundRect(card, 8, graphFade(g.colSurface0, 0.96))
	dst.StrokeRoundRect(card, 8, 1, g.colBorder)

	cy := y + 10
	col := g.nodeColor(nd)
	dst.FillCircle(ggui.Pt(x+14, cy+6), 4, col)
	title := dst.FitText(nd.GetLabel(), g.font, 11.5, cardW-32)
	dst.DrawText(title, g.font, 11.5, ggui.Pt(x+24, cy), g.colFg)
	cy += 18

	if nd.GetSubtitle() != "" {
		sub := dst.FitText(nd.GetSubtitle(), g.font, 10, cardW-20)
		dst.DrawText(sub, g.font, 10, ggui.Pt(x+10, cy), g.colMuted)
		cy += 16
	}

	meta := nd.GetGroup()
	if st := nd.GetStatus(); st != "" {
		if meta != "" {
			meta += " · " + st
		} else {
			meta = st
		}
	}
	if meta != "" {
		dst.DrawText(dst.FitText(meta, g.font, 9.5, cardW-20), g.font, 9.5, ggui.Pt(x+10, cy), g.colMuted)
		cy += 16
	}
	if len(nd.GetBadges()) > 0 {
		g.paintCanvasBadges(dst, nd.GetBadges(), ggui.Pt(x+10, cy), cardW-20, 0.9, 1.0)
	}

	dst.Leaf(card, ggui.Node{
		Role: ggui.RoleStatus,
		Name: Tf("Detail: %s", nd.GetLabel()),
	})
}

func (g *pluginGraphCanvas) openSessionTarget(sid string) {
	if sid == "" {
		return
	}
	if g.openSession != nil {
		g.openSession(sid)
		return
	}
	if g.m == nil {
		return
	}
	if g.spaceWindow != nil {
		g.m.spaceWindowSelectSession(g.spaceWindow, sid)
		return
	}
	g.m.clearSidebarPage()
	if g.m.Page != nil && ggui.Peek(g.m.Page) != "workspace" {
		g.m.Page.Set("workspace")
	}
	g.m.selectSession(sid)
}

func (g *pluginGraphCanvas) leavePluginSidebarPage() {
	if g.spaceWindow != nil {
		if g.spaceWindow.sidebarPage != nil && ggui.Peek(g.spaceWindow.sidebarPage) != "" {
			g.spaceWindow.sidebarPage.Set("")
		}
		return
	}
	if g.m == nil {
		return
	}
	g.m.clearSidebarPage()
	if g.m.Page != nil && ggui.Peek(g.m.Page) != "workspace" {
		g.m.Page.Set("workspace")
	}
}

func (g *pluginGraphCanvas) triggerDoubleTap(nodeID string) {
	if nodeID == "" {
		return
	}
	if g.onDoubleTap != nil {
		g.onDoubleTap(nodeID)
		return
	}
	if g.m != nil {
		if sid, ok := g.sessionIDForNode(nodeID); ok {
			g.openSessionTarget(sid)
		}
	}
}

func (g *pluginGraphCanvas) sessionIDForNode(nodeID string) (string, bool) {
	if g.m == nil {
		return "", false
	}
	sid, ok := strings.CutPrefix(nodeID, "sess:")
	if !ok || sid == "" {
		return "", false
	}
	_, exists := g.m.session(sid)
	return sid, exists
}

func (g *pluginGraphCanvas) openNodeContextMenu(nd *protocol.UiGraphNode, at ggui.Point) bool {
	if nd == nil {
		return false
	}
	var nativeItems []ggui.MenuItem
	var widgetItems []ggui.Widget

	if g.pluginMenu != nil {
		nat, wid := g.pluginMenu(nd)
		nativeItems = append(nativeItems, nat...)
		widgetItems = append(widgetItems, wid...)
	}

	if sid, ok := g.sessionIDForNode(nd.GetId()); ok {
		m := g.m
		if len(nativeItems) > 0 {
			nativeItems = append(nativeItems, ggui.MenuSeparator())
		}
		if len(widgetItems) > 0 {
			widgetItems = append(widgetItems, ui.MenuDivider())
		}
		openAct := func() {
			g.openSessionTarget(sid)
		}
		sendAct := func() {
			g.leavePluginSidebarPage()
			m.askAgentIn(g.spaceWindow, sid, "")
		}
		subAct := func() {
			g.leavePluginSidebarPage()
			m.openSubagentIn(g.spaceWindow, sid)
		}
		killAct := func() {
			m.askKill(sid)
		}

		nativeItems = append(nativeItems,
			ggui.MenuAction(T("Open session"), "", openAct),
			ggui.MenuAction(T("Send message"), "", sendAct),
			ggui.MenuAction(T("Run subagent"), "", subAct),
			ggui.MenuSeparator(),
			ggui.MenuAction(T("Kill session"), "", killAct),
		)
		widgetItems = append(widgetItems,
			ui.MenuItem(T("Open session"), openAct).Icon(icon("external-link").Size(13).Color(skin.Muted)),
			ui.MenuItem(T("Send message"), sendAct).Icon(icon("message-square").Size(13).Color(skin.Muted)),
			ui.MenuItem(T("Run subagent"), subAct).Icon(icon("bot").Size(13).Color(skin.Muted)),
			ui.MenuDivider(),
			ui.MenuItem(T("Kill session"), killAct).Destructive().Icon(icon("trash-2").Size(13).Color(skin.DiffRemoved)),
		)
	}

	if len(nativeItems) == 0 && len(widgetItems) == 0 {
		return false
	}

	if h, ok := ggui.LookupHost(); ok {
		if w, ok := h.(*ggui.Window); ok && w != nil && w.ShowContextMenu(at, nativeItems...) {
			return true
		}
	}
	cs := g.state.ctxStateFor(g.spaceWindow)
	popup := g.ctxPopup
	if popup == nil && cs != nil {
		popup = cs.popup
	}
	if popup != nil && cs != nil && cs.items != nil {
		cs.items.Set(widgetItems)
		popup.ShowAt(at)
		ggui.Invalidate(g.env)
		return true
	}
	return false
}

func (g *pluginGraphCanvas) HandleKey(ev ggui.KeyEvent) {
	g.Keyboard(ev, nil)
	if g.IsInert() || ev.Kind != ggui.KeyPress || len(g.nodes) == 0 {
		return
	}
	cur := g.activeSelectedID()
	switch ev.Key {
	case ggui.KeyEnter, ggui.KeyNumpadEnter:
		if cur != "" {
			g.triggerDoubleTap(cur)
		}
	case ggui.KeyArrowLeft, ggui.KeyArrowRight, ggui.KeyArrowUp, ggui.KeyArrowDown:
		next := g.nextNodeInDirection(cur, ev.Key)
		if next != "" {
			g.state.localSelected = next
			g.state.focusNode(next, g.rect)
			if g.onSelect != nil {
				g.onSelect(next)
			}
			ggui.Invalidate(g.env)
		}
	}
}

func (g *pluginGraphCanvas) nextNodeInDirection(cur string, key ggui.KeyboardKey) string {
	if len(g.nodes) == 0 {
		return ""
	}
	if cur == "" {
		return g.nodes[0].GetId()
	}
	curPos, ok := g.state.layout.pos[cur]
	if !ok {
		return g.nodes[0].GetId()
	}

	var dx, dy float64
	switch key {
	case ggui.KeyArrowRight:
		dx = 1
	case ggui.KeyArrowLeft:
		dx = -1
	case ggui.KeyArrowDown:
		dy = 1
	case ggui.KeyArrowUp:
		dy = -1
	}

	bestID := ""
	bestScore := math.Inf(1)
	for _, nd := range g.nodes {
		id := nd.GetId()
		if id == cur {
			continue
		}
		p, ok := g.state.layout.pos[id]
		if !ok {
			continue
		}
		vx, vy := p.X-curPos.X, p.Y-curPos.Y
		proj := vx*dx + vy*dy
		if proj <= 1e-4 {
			continue
		}
		ortho := math.Abs(vx*dy - vy*dx)
		score := proj + ortho*2.2
		if score < bestScore {
			bestScore = score
			bestID = id
		}
	}
	if bestID != "" {
		return bestID
	}

	// Deterministic sequential wrap when no node lies strictly in that half-plane.
	idx := slices.IndexFunc(g.nodes, func(nd *protocol.UiGraphNode) bool { return nd.GetId() == cur })
	if idx < 0 {
		return g.nodes[0].GetId()
	}
	step := 1
	if key == ggui.KeyArrowLeft || key == ggui.KeyArrowUp {
		step = -1
	}
	nextIdx := (idx + step + len(g.nodes)) % len(g.nodes)
	return g.nodes[nextIdx].GetId()
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
		return true
	case ggui.PointerExit:
		if g.state.hoveredID != "" {
			g.state.hoveredID = ""
			ggui.Invalidate(g.env)
		}
		return g.Pointer(ev, nil)
	case ggui.PointerDown:
		if ev.Button == ggui.MouseButtonRight {
			nd := g.hitTestNodeObj(g.rect, ev.Pos)
			if nd == nil {
				return false
			}
			return g.openNodeContextMenu(nd, ev.Pos)
		}
		if ev.Button != ggui.MouseButtonLeft {
			return false
		}
		g.wantFocus = true
		g.moved = false
		g.downPos = ev.Pos
		g.lastPos = ev.Pos
		if mmRect, ok := g.minimapRect(g.rect); ok && mmRect.Contains(ev.Pos) {
			g.dragMode = graphDragMinimap
			g.panToMinimapPoint(g.rect, mmRect, ev.Pos)
			ggui.Invalidate(g.env)
			return true
		}
		if hit := g.hitTestNode(g.rect, ev.Pos); hit != "" {
			g.dragMode = graphDragNode
			g.dragNodeID = hit
			if rp, ok := g.state.renderedPos(hit); ok {
				g.dragStartWorld = rp
			} else {
				g.dragStartWorld = g.state.layout.pos[hit]
			}
			startScreen := g.worldToScreen(g.rect, g.dragStartWorld)
			g.dragGrabOffset = ggui.Pt(ev.Pos.X-startScreen.X, ev.Pos.Y-startScreen.Y)
		} else {
			g.dragMode = graphDragPan
			g.dragNodeID = ""
			curPanX, curPanY, curZoom := g.state.renderedCamera()
			if g.state.hasCamBounds {
				curCam := g.state.cameraLayout()
				curCam.pos = g.state.layout.pos
				g.state.setCamBounds(curCam, false)
			}
			g.state.setCamera(curPanX, curPanY, curZoom, false)
			g.panStartX = curPanX
			g.panStartY = curPanY
		}
		ggui.Invalidate(g.env)
		return g.Pointer(ev, nil)
	case ggui.PointerDrag:
		if g.dragMode == graphDragNone {
			return false
		}
		wasMoved := g.moved
		if !g.moved && math.Hypot(ev.Pos.X-g.downPos.X, ev.Pos.Y-g.downPos.Y) > 4 {
			g.moved = true
		}
		switch g.dragMode {
		case graphDragMinimap:
			if mmRect, ok := g.minimapRect(g.rect); ok {
				g.panToMinimapPoint(g.rect, mmRect, ev.Pos)
				ggui.Invalidate(g.env)
			}
		case graphDragPan:
			if g.moved {
				dx := ev.Pos.X - g.downPos.X
				dy := ev.Pos.Y - g.downPos.Y
				g.state.setCamera(g.panStartX+dx, g.panStartY+dy, g.state.zoom, false)
				ggui.Invalidate(g.env)
			}
		case graphDragNode:
			if g.findNode(g.dragNodeID) == nil || g.state == nil {
				g.dragMode = graphDragNone
				g.dragNodeID = ""
				g.moved = false
				if g.state != nil {
					g.state.draggingID = ""
				}
				ggui.Invalidate(g.env)
				return false
			}
			if _, ok := g.state.layout.pos[g.dragNodeID]; !ok {
				g.dragMode = graphDragNone
				g.dragNodeID = ""
				g.moved = false
				g.state.draggingID = ""
				ggui.Invalidate(g.env)
				return false
			}
			if g.moved && g.dragNodeID != "" {
				if !wasMoved {
					if g.state.hasCamBounds {
						curCam := g.state.cameraLayout()
						curCam.pos = g.state.layout.pos
						g.state.setCamBounds(curCam, false)
					}
					curPanX, curPanY, curZoom := g.state.renderedCamera()
					g.state.setCamera(curPanX, curPanY, curZoom, false)
					if rp, ok := g.state.renderedPos(g.dragNodeID); ok {
						g.dragStartWorld = rp
					} else {
						g.dragStartWorld = g.state.layout.pos[g.dragNodeID]
					}
					curNodeScreen := g.worldToScreen(g.rect, g.dragStartWorld)
					g.downPos = ggui.Pt(curNodeScreen.X+g.dragGrabOffset.X, curNodeScreen.Y+g.dragGrabOffset.Y)
				}
				g.state.draggingID = g.dragNodeID
				cam := g.state.cameraLayout()
				_, _, curZoom := g.state.renderedCamera()
				scale := graphBaseScale(g.rect, cam) * curZoom
				if scale <= 0 {
					scale = 1
				}
				dx := (ev.Pos.X - g.downPos.X) / scale
				dy := (ev.Pos.Y - g.downPos.Y) / scale
				newPos := ggui.Pt(
					clampGraphCoord(g.dragStartWorld.X+dx),
					clampGraphCoord(g.dragStartWorld.Y+dy),
				)
				if g.state.pinnedPos == nil {
					g.state.pinnedPos = map[string]ggui.Point{}
				}
				g.state.pinnedPos[g.dragNodeID] = newPos
				g.state.layout.pos[g.dragNodeID] = newPos
				ggui.Invalidate(g.env)
			}
		}
		g.lastPos = ev.Pos
		return true
	case ggui.PointerUp:
		mode, wasMoved, dragID := g.dragMode, g.moved, g.dragNodeID
		g.dragMode = graphDragNone
		g.dragNodeID = ""
		g.moved = false
		if g.state != nil {
			g.state.draggingID = ""
		}
		if mode == graphDragNode {
			if g.findNode(dragID) == nil || g.state == nil {
				ggui.Invalidate(g.env)
				return g.Pointer(ev, nil)
			}
			if _, ok := g.state.layout.pos[dragID]; !ok {
				ggui.Invalidate(g.env)
				return g.Pointer(ev, nil)
			}
		}
		if mode == graphDragNode && wasMoved && dragID != "" {
			g.state.layout = refreshGraphBounds(g.state.layout)
			if pos, ok := g.state.layout.pos[dragID]; ok {
				if g.state.layoutTarget != nil {
					posCopy := maps.Clone(g.state.layout.pos)
					g.state.layoutTarget.Set(posCopy)
					if g.state.layoutTween != nil {
						cur := maps.Clone(g.state.layoutTween.Get())
						if cur != nil {
							cur[dragID] = pos
							if maps.Equal(cur, posCopy) {
								g.state.layoutTween.Jump(posCopy)
							} else {
								g.state.layoutTween.Jump(cur)
								g.state.layoutTween.Set(posCopy)
							}
						} else {
							g.state.layoutTween.Jump(posCopy)
						}
					}
				}
				if g.onDrop != nil {
					g.onDrop(dragID, pos)
				}
			}
			ggui.Invalidate(g.env)
			return g.Pointer(ev, nil)
		}
		if mode != graphDragNone && mode != graphDragMinimap && !wasMoved && ev.Button == ggui.MouseButtonLeft {
			hit := g.hitTestNode(g.rect, ev.Pos)
			now := ggui.ClockTime()
			isDoubleTap := g.state.lastTapID != "" &&
				now.Sub(g.state.lastTapAt) <= graphDoubleTapWindow &&
				math.Hypot(ev.Pos.X-g.state.lastTapPos.X, ev.Pos.Y-g.state.lastTapPos.Y) <= graphDoubleTapDist &&
				g.findNode(g.state.lastTapID) != nil
			if isDoubleTap {
				tapID := g.state.lastTapID
				g.state.lastTapID = ""
				g.triggerDoubleTap(tapID)
				ggui.Invalidate(g.env)
			} else if hit != "" {
				g.state.lastTapID = hit
				g.state.lastTapAt = now
				g.state.lastTapPos = ev.Pos
				g.state.localSelected = hit
				g.state.focusNode(hit, g.rect)
				if g.onSelect != nil {
					g.onSelect(hit)
				}
				ggui.Invalidate(g.env)
			} else {
				g.state.lastTapID = ""
			}
		}
		return g.Pointer(ev, nil)
	case ggui.PointerScroll:
		// Let plain mouse-wheel / trackpad scroll bubble up to ancestor scroll
		// containers; only Ctrl/Cmd/Meta/Shift + wheel zooms the graph canvas.
		if !(ev.Mods.Ctrl || ev.Mods.Meta || ev.Mods.Shift) {
			return false
		}
		if ev.Scroll.Y != 0 {
			factor := 1.0 - ev.Scroll.Y*0.08
			if factor < 0.75 {
				factor = 0.75
			}
			if factor > 1.35 {
				factor = 1.35
			}
			curPanX, curPanY, curZoom := g.state.renderedCamera()
			if g.state.hasCamBounds {
				curCam := g.state.cameraLayout()
				curCam.pos = g.state.layout.pos
				g.state.setCamBounds(curCam, false)
			}
			nextZoom := clampFloat(curZoom*factor, 0.35, 3.0)
			newPanX, newPanY := curPanX, curPanY
			if g.rect.Size.W > 0 && g.rect.Size.H > 0 && curZoom > 0 {
				rcx := g.rect.Origin.X + g.rect.Size.W/2
				rcy := g.rect.Origin.Y + g.rect.Size.H/2
				ratio := nextZoom / curZoom
				newPanX = ev.Pos.X - rcx - (ev.Pos.X-rcx-curPanX)*ratio
				newPanY = ev.Pos.Y - rcy - (ev.Pos.Y-rcy-curPanY)*ratio
			}
			g.state.setCamera(newPanX, newPanY, nextZoom, false)
			ggui.Invalidate(g.env)
			return true
		}
		return false
	}
	return g.Pointer(ev, nil)
}

func expandRect(r ggui.Rect, px float64) ggui.Rect {
	return ggui.Rct(ggui.Pt(r.Origin.X-px, r.Origin.Y-px), ggui.Sz(r.Size.W+px*2, r.Size.H+px*2))
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

func clampGraphCoord(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	return clampFloat(v, -graphCoordLimit, graphCoordLimit)
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
