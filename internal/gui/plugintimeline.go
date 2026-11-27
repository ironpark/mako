package gui

import (
	"fmt"
	"image/color"
	"math"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	uitheme "github.com/ironpark/ggui/ui/theme"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

const (
	timelineMinZoom = 1.0
	timelineMaxZoom = 24.0
	timelineRulerH  = 28.0
	timelineLaneH   = 42.0
)

type timelineViewState struct {
	zoom           float64
	scrollX        float64
	scrollY        float64
	selectedSpanID string
	hoveredSpanID  string
}

func formatTimelineMs(ms float64) string {
	if math.IsNaN(ms) || math.IsInf(ms, 0) {
		return "0s"
	}
	sign := ""
	if ms < 0 {
		sign = "-"
		ms = -ms
	}
	switch {
	case ms < 1000:
		return fmt.Sprintf("%s%.0fms", sign, ms)
	case ms < 60_000:
		return fmt.Sprintf("%s%.1fs", sign, ms/1000)
	case ms < 3_600_000:
		totalSec := int64(ms / 1000)
		m := totalSec / 60
		s := totalSec % 60
		return fmt.Sprintf("%s%d:%02d", sign, m, s)
	case ms < 86_400_000:
		totalSec := int64(ms / 1000)
		h := totalSec / 3600
		m := (totalSec % 3600) / 60
		s := totalSec % 60
		return fmt.Sprintf("%s%d:%02d:%02d", sign, h, m, s)
	default:
		hours := ms / 3_600_000
		return fmt.Sprintf("%s%.1fh:00", sign, hours)
	}
}

func timelineBounds(lanes []*protocol.UiTimelineLane) (minMs, maxMs, totalMs float64, hasSpans bool) {
	first := true
	for _, lane := range lanes {
		for _, sp := range lane.GetSpans() {
			s := sp.GetStartMs()
			e := sp.GetEndMs()
			if math.IsNaN(s) || math.IsInf(s, 0) || math.IsNaN(e) || math.IsInf(e, 0) {
				continue
			}
			lo, hi := min(s, e), max(s, e)
			if first {
				minMs, maxMs = lo, hi
				first = false
			} else {
				minMs = min(minMs, lo)
				maxMs = max(maxMs, hi)
			}
		}
	}
	if first {
		return 0, 1000, 1000, false
	}
	totalMs = maxMs - minMs
	if totalMs <= 0 || math.IsNaN(totalMs) || math.IsInf(totalMs, 0) {
		totalMs = 1
		maxMs = minMs + 1
	}
	return minMs, maxMs, totalMs, true
}

func (v *treeView) timelineNode(n *protocol.UiNode) ggui.Widget {
	id := n.GetId()
	st := v.form.timelineState(id)
	if st.zoom < timelineMinZoom {
		st.zoom = timelineMinZoom
	}

	tick := ggui.State(0)
	bump := func() { tick.Update(func(x int) int { return x + 1 }) }

	interactive := n.GetChange() && !n.GetDisabled()
	var onSelect func(string)
	if interactive {
		onSelect = func(spanID string) {
			v.send(id, protocol.UiChange, spanID)
		}
	}

	fill := n.GetFill() || n.GetGrow() > 0
	tl := &pluginTimelineWidget{
		state:       st,
		lanes:       n.GetTimelineLanes(),
		maxHeight:   n.GetMaxHeight(),
		fill:        fill,
		grow:        n.GetGrow(),
		interactive: interactive,
		onSelect:    onSelect,
		tick:        tick,
		name:        cmpTone(n.GetText(), T("Timeline")),
	}
	tl.Role = ggui.RoleGroup
	tl.SetName(tl.name)
	tl.SetKey("timeline:" + v.block + ":" + v.scope + ":" + id)
	tl.SetInert(n.GetDisabled())

	controls := widget.Align(
		widget.Padding(
			widget.Box(widget.Row(
				ui.Tooltip(
					ui.ButtonOf(widget.Text("+").Size(13).Weight(ggui.WeightMedium).Color(skin.Fg).NoWrap(), func() {
						st.zoom = clampFloat(st.zoom*1.5, timelineMinZoom, timelineMaxZoom)
						bump()
					}).Ghost().Pad(2, 6).Radius(5).Disabled(n.GetDisabled()).Name(T("Zoom in")),
					T("Zoom in"),
				),
				ui.Tooltip(
					ui.ButtonOf(widget.Text("−").Size(13).Weight(ggui.WeightMedium).Color(skin.Fg).NoWrap(), func() {
						st.zoom = clampFloat(st.zoom/1.5, timelineMinZoom, timelineMaxZoom)
						bump()
					}).Ghost().Pad(2, 6).Radius(5).Disabled(n.GetDisabled()).Name(T("Zoom out")),
					T("Zoom out"),
				),
				ui.Tooltip(
					ui.ButtonOf(icon("refresh-cw").Size(12).Color(skin.Muted), func() {
						st.zoom = timelineMinZoom
						st.scrollX = 0
						st.scrollY = 0
						bump()
					}).Ghost().Pad(4).Radius(5).Disabled(n.GetDisabled()).Name(T("Reset")),
					T("Reset"),
				),
			).Gap(2).Align(widget.AlignCenter)).Fill(skin.Surface0).Border(1, skin.Border).Radius(7).Pad(2),
			6,
		),
	).At(1, 0)

	layered := ggui.Widget(overlayStack{base: tl, overlay: controls})
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

type timelineSpanHit struct {
	id   string
	rect ggui.Rect
}

type pluginTimelineWidget struct {
	ggui.Interactive
	state       *timelineViewState
	lanes       []*protocol.UiTimelineLane
	maxHeight   float64
	fill        bool
	grow        float64
	interactive bool
	onSelect    func(string)
	tick        *ggui.StateValue[int]
	name        string

	env   ggui.Env
	theme *uitheme.Theme
	font  *ggui.Font
	rect  ggui.Rect
	hits  []timelineSpanHit

	colBg       color.Color
	colSurface1 color.Color
	colSurface2 color.Color
	colBorder   color.Color
	colFg       color.Color
	colMuted    color.Color

	dragging bool
	moved    bool
	downPos  ggui.Point
	lastPos  ggui.Point
}

func (t *pluginTimelineWidget) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	t.Sync()
	if t.tick != nil {
		_ = t.tick.Get()
	}
	t.env = env
	t.theme = uitheme.Of(env)
	t.font = env.ResolveText(ggui.TextStyle{Size: 11}).Font
	t.colBg = resolveOr(skin.Surface0, env, t.theme.Card)
	t.colSurface1 = resolveOr(skin.Surface1, env, t.theme.Card)
	t.colSurface2 = resolveOr(skin.Surface2, env, t.theme.Muted)
	t.colBorder = resolveOr(skin.Border, env, t.theme.Border)
	t.colFg = resolveOr(skin.Fg, env, t.theme.Fg)
	t.colMuted = resolveOr(skin.Muted, env, t.theme.MutedFg)

	w := c.MaxW
	if math.IsInf(w, 1) || w <= 0 {
		w = 560
	}
	laneCount := max(len(t.lanes), 1)
	h := max(timelineRulerH+float64(laneCount)*timelineLaneH+12, 110)
	if t.maxHeight > 0 && h > t.maxHeight {
		h = t.maxHeight
	}
	if (t.fill || t.grow > 0) && !math.IsInf(c.MaxH, 1) && c.MaxH > 0 {
		h = c.MaxH
		if t.maxHeight > 0 && h > t.maxHeight {
			h = t.maxHeight
		}
	} else if !math.IsInf(c.MaxH, 1) && c.MaxH > 0 && h > c.MaxH {
		h = c.MaxH
	}
	return c.Constrain(ggui.Sz(w, h))
}

func (t *pluginTimelineWidget) Describe() ggui.Node {
	return ggui.Node{
		Role:     ggui.RoleGroup,
		Name:     t.name,
		Disabled: t.IsInert(),
		Actions:  ggui.ActionFocus,
	}
}

func (t *pluginTimelineWidget) CaptureDrag() bool      { return !t.IsInert() }
func (t *pluginTimelineWidget) CaptureTouchDrag() bool { return !t.IsInert() }
func (t *pluginTimelineWidget) HandleKey(ev ggui.KeyEvent) {
	t.Keyboard(ev, nil)
}

func (t *pluginTimelineWidget) Adopt(prev any) {
	t.Interactive.Adopt(prev)
	if q, ok := prev.(*pluginTimelineWidget); ok {
		t.rect = q.rect
		t.hits = q.hits
		t.dragging = q.dragging
		t.moved = q.moved
		t.downPos = q.downPos
		t.lastPos = q.lastPos
	}
}

func (t *pluginTimelineWidget) trackMetrics(r ggui.Rect) (gutterW, trackX, trackW, contentW, maxScrollX, maxScrollY float64) {
	gutterW = min(116.0, r.Size.W*0.32)
	trackX = r.Origin.X + gutterW
	trackW = max(r.Size.W-gutterW-10, 40)
	contentW = trackW * t.state.zoom
	maxScrollX = max(contentW-trackW, 0)
	viewH := max(r.Size.H-timelineRulerH, 20)
	totalH := float64(len(t.lanes)) * timelineLaneH
	maxScrollY = max(totalH-viewH, 0)
	return
}

func (t *pluginTimelineWidget) Paint(dst *ggui.Canvas, r ggui.Rect) {
	t.rect = r
	t.hits = t.hits[:0]

	gutterW, trackX, trackW, contentW, maxScrollX, maxScrollY := t.trackMetrics(r)
	t.state.scrollX = clampFloat(t.state.scrollX, 0, maxScrollX)
	t.state.scrollY = clampFloat(t.state.scrollY, 0, maxScrollY)

	minMs, _, totalMs, hasSpans := timelineBounds(t.lanes)

	dst.DescribeNode(r, t, func(dst *ggui.Canvas) {
		cursor := ggui.CursorShapeDefault
		if t.state.hoveredSpanID != "" {
			cursor = ggui.CursorShapePointer
		}
		t.Hit(dst, r, t, cursor)
		dst.FillRoundRect(r, 8, t.colBg)
		dst.ClipRoundRect(r, 8, func(dst *ggui.Canvas) {
			// Ruler header background & separator.
			rulerRect := ggui.Rct(r.Origin, ggui.Sz(r.Size.W, timelineRulerH))
			dst.FillRect(rulerRect, t.colSurface1)
			dst.StrokeLine(
				ggui.Pt(r.Origin.X, r.Origin.Y+timelineRulerH),
				ggui.Pt(r.Origin.X+r.Size.W, r.Origin.Y+timelineRulerH),
				1,
				t.colBorder,
			)
			dst.StrokeLine(
				ggui.Pt(trackX, r.Origin.Y),
				ggui.Pt(trackX, r.Origin.Y+r.Size.H),
				1,
				t.colBorder,
			)

			// Bounded time-axis ruler ticks (safe for 0-duration and 1e13ms ranges).
			trackRulerRect := ggui.Rct(ggui.Pt(trackX, r.Origin.Y), ggui.Sz(trackW, timelineRulerH))
			rulerDst := dst.Clip(trackRulerRect)
			const tickCount = 5
			visStartMs := minMs + (t.state.scrollX/contentW)*totalMs
			visEndMs := minMs + ((t.state.scrollX+trackW)/contentW)*totalMs
			for i := 0; i <= tickCount; i++ {
				frac := float64(i) / float64(tickCount)
				tickMs := visStartMs + frac*(visEndMs-visStartMs)
				tx := trackX + frac*trackW
				lbl := formatTimelineMs(tickMs - minMs)
				if t.font != nil {
					tw := rulerDst.TextWidth(lbl, t.font, 10)
					lx := clampFloat(tx-tw*0.5, trackX+2, trackX+trackW-tw-2)
					ly := r.Origin.Y + 8
					rulerDst.DrawText(lbl, t.font, 10, ggui.Pt(lx, ly), t.colMuted)
					rulerDst.Leaf(ggui.Rct(ggui.Pt(lx, ly), ggui.Sz(max(tw, 12), 14)), ggui.Node{
						Role: ggui.RoleText,
						Name: lbl,
					})
				}
			}

			if len(t.lanes) == 0 {
				if t.font != nil {
					msg := T("No timeline data.")
					tw := dst.TextWidth(msg, t.font, 12)
					lx := r.Origin.X + (r.Size.W-tw)/2
					ly := r.Origin.Y + timelineRulerH + (r.Size.H-timelineRulerH)/2 - 7
					dst.DrawText(msg, t.font, 12, ggui.Pt(lx, ly), t.colMuted)
					dst.Leaf(ggui.Rct(ggui.Pt(lx, ly), ggui.Sz(tw, 16)), ggui.Node{
						Role: ggui.RoleText,
						Name: msg,
					})
				}
				return
			}

			bodyRect := ggui.Rct(ggui.Pt(r.Origin.X, r.Origin.Y+timelineRulerH), ggui.Sz(r.Size.W, max(r.Size.H-timelineRulerH, 0)))
			bodyDst := dst.Clip(bodyRect)

			// Draw lane rows and gutter labels.
			for li, lane := range t.lanes {
				ly := r.Origin.Y + timelineRulerH + float64(li)*timelineLaneH - t.state.scrollY
				if ly+timelineLaneH < bodyRect.Origin.Y || ly > bodyRect.Origin.Y+bodyRect.Size.H {
					continue
				}
				if li%2 == 1 {
					bodyDst.FillRect(ggui.Rct(ggui.Pt(r.Origin.X, ly), ggui.Sz(r.Size.W, timelineLaneH)), graphFade(t.colSurface2, 0.35))
				}
				bodyDst.StrokeLine(
					ggui.Pt(r.Origin.X, ly+timelineLaneH),
					ggui.Pt(r.Origin.X+r.Size.W, ly+timelineLaneH),
					1,
					graphFade(t.colBorder, 0.55),
				)
				if label := lane.GetLabel(); label != "" && t.font != nil {
					fitted := bodyDst.FitText(label, t.font, 11.5, max(gutterW-16, 16))
					lRect := ggui.Rct(ggui.Pt(r.Origin.X+8, ly+(timelineLaneH-14)/2), ggui.Sz(max(gutterW-16, 16), 16))
					bodyDst.DrawText(fitted, t.font, 11.5, lRect.Origin, t.colFg)
					bodyDst.Leaf(lRect, ggui.Node{
						Role: ggui.RoleText,
						Name: label,
					})
				}
			}

			if !hasSpans {
				return
			}

			// Clip spans to track body area.
			trackBodyRect := ggui.Rct(ggui.Pt(trackX, bodyRect.Origin.Y), ggui.Sz(trackW, bodyRect.Size.H))
			trackDst := bodyDst.Clip(trackBodyRect)

			for li, lane := range t.lanes {
				ly := r.Origin.Y + timelineRulerH + float64(li)*timelineLaneH - t.state.scrollY
				if ly+timelineLaneH < bodyRect.Origin.Y || ly > bodyRect.Origin.Y+bodyRect.Size.H {
					continue
				}
				barY := ly + 6
				barH := timelineLaneH - 12
				for _, sp := range lane.GetSpans() {
					s := sp.GetStartMs()
					e := sp.GetEndMs()
					if math.IsNaN(s) || math.IsInf(s, 0) || math.IsNaN(e) || math.IsInf(e, 0) {
						continue
					}
					lo, hi := min(s, e), max(s, e)
					u0 := (lo - minMs) / totalMs
					u1 := (hi - minMs) / totalMs
					trackStart := trackX - t.state.scrollX
					trackEnd := trackStart + contentW
					x0 := trackStart + u0*contentW
					x1 := trackStart + u1*contentW
					spanW := max(x1-x0, 8.0)
					if x1-x0 < 8.0 {
						mid := (x0 + x1) * 0.5
						x0 = clampFloat(mid-spanW*0.5, trackStart, max(trackStart, trackEnd-spanW))
					}
					if x0+spanW < trackX || x0 > trackX+trackW {
						continue
					}
					spRect := ggui.Rct(ggui.Pt(x0, barY), ggui.Sz(spanW, barH))
					clipped := spRect.Intersect(trackBodyRect)
					if clipped.Empty() {
						continue
					}
					tCol := resolveOr(toneColor(cmpTone(sp.GetTone(), "accent")), t.env, t.theme.Primary)
					isSel := sp.GetId() == t.state.selectedSpanID
					isHover := sp.GetId() == t.state.hoveredSpanID
					fillAlpha := 0.22
					borderAlpha := 0.65
					strokeW := 1.0
					if isSel || isHover {
						fillAlpha = 0.34
						borderAlpha = 0.95
						strokeW = 1.5
					}
					trackDst.FillRoundRect(spRect, 5, graphFade(tCol, fillAlpha))
					trackDst.StrokeRoundRect(spRect, 5, strokeW, graphFade(tCol, borderAlpha))
					accentBar := ggui.Rct(ggui.Pt(spRect.Origin.X, spRect.Origin.Y+1), ggui.Sz(min(3.5, spanW), max(barH-2, 2)))
					trackDst.FillRoundRect(accentBar, 2, tCol)

					if spanW > 20 && t.font != nil {
						title := cmpTone(sp.GetLabel(), sp.GetId())
						fitted := trackDst.FitText(title, t.font, 11, max(spanW-14, 6))
						if sp.GetSubtitle() != "" && barH >= 28 {
							trackDst.DrawText(fitted, t.font, 11, ggui.Pt(spRect.Origin.X+7, spRect.Origin.Y+3), t.colFg)
							sub := trackDst.FitText(sp.GetSubtitle(), t.font, 9.5, max(spanW-14, 6))
							trackDst.DrawText(sub, t.font, 9.5, ggui.Pt(spRect.Origin.X+7, spRect.Origin.Y+16), t.colMuted)
						} else {
							trackDst.DrawText(fitted, t.font, 11, ggui.Pt(spRect.Origin.X+7, spRect.Origin.Y+(barH-13)/2), t.colFg)
						}
					}

					t.hits = append(t.hits, timelineSpanHit{id: sp.GetId(), rect: clipped})
					trackDst.Leaf(clipped, ggui.Node{
						Role:     ggui.RoleButton,
						Name:     cmpTone(sp.GetLabel(), sp.GetId()),
						Selected: isSel,
						Disabled: t.IsInert() || !t.interactive,
					})
				}
			}
		})
		dst.StrokeRoundRect(r, 8, 1, t.colBorder)
	})
}

func (t *pluginTimelineWidget) hitTestSpan(pt ggui.Point) string {
	if !t.rect.Contains(pt) {
		return ""
	}
	for i := len(t.hits) - 1; i >= 0; i-- {
		if t.hits[i].rect.Contains(pt) {
			return t.hits[i].id
		}
	}
	return ""
}

func (t *pluginTimelineWidget) HandlePointer(ev ggui.PointerEvent) bool {
	if t.IsInert() {
		return false
	}
	_, _, _, _, maxScrollX, maxScrollY := t.trackMetrics(t.rect)
	switch ev.Kind {
	case ggui.PointerMove:
		hit := t.hitTestSpan(ev.Pos)
		if hit != t.state.hoveredSpanID {
			t.state.hoveredSpanID = hit
			ggui.Invalidate(t.env)
		}
		return t.Pointer(ev, nil)
	case ggui.PointerExit:
		if t.state.hoveredSpanID != "" {
			t.state.hoveredSpanID = ""
			ggui.Invalidate(t.env)
		}
		return t.Pointer(ev, nil)
	case ggui.PointerDown:
		if ev.Button != ggui.MouseButtonLeft {
			return false
		}
		t.dragging = true
		t.moved = false
		t.downPos = ev.Pos
		t.lastPos = ev.Pos
		return t.Pointer(ev, nil)
	case ggui.PointerDrag:
		if !t.dragging {
			return false
		}
		if !t.moved && math.Hypot(ev.Pos.X-t.downPos.X, ev.Pos.Y-t.downPos.Y) > 4 {
			t.moved = true
		}
		if t.moved {
			t.state.scrollX = clampFloat(t.state.scrollX-(ev.Pos.X-t.lastPos.X), 0, maxScrollX)
			t.state.scrollY = clampFloat(t.state.scrollY-(ev.Pos.Y-t.lastPos.Y), 0, maxScrollY)
			ggui.Invalidate(t.env)
		}
		t.lastPos = ev.Pos
		return true
	case ggui.PointerUp:
		wasDrag, wasMoved := t.dragging, t.moved
		t.dragging = false
		t.moved = false
		if wasDrag && !wasMoved && ev.Button == ggui.MouseButtonLeft {
			if hit := t.hitTestSpan(ev.Pos); hit != "" {
				t.state.selectedSpanID = hit
				if t.onSelect != nil {
					t.onSelect(hit)
				}
				ggui.Invalidate(t.env)
			}
		}
		return t.Pointer(ev, nil)
	case ggui.PointerScroll:
		if ev.Mods.Ctrl || ev.Mods.Meta || ev.Mods.Shift {
			if ev.Scroll.Y != 0 {
				factor := clampFloat(1.0-ev.Scroll.Y*0.1, 0.7, 1.45)
				t.state.zoom = clampFloat(t.state.zoom*factor, timelineMinZoom, timelineMaxZoom)
				_, _, _, _, newMaxX, _ := t.trackMetrics(t.rect)
				t.state.scrollX = clampFloat(t.state.scrollX, 0, newMaxX)
				ggui.Invalidate(t.env)
				return true
			}
			return false
		}
		if math.Abs(ev.Scroll.X) > 0 && maxScrollX > 0 {
			next := clampFloat(t.state.scrollX-ev.Scroll.X*1.5, 0, maxScrollX)
			if next != t.state.scrollX {
				t.state.scrollX = next
				ggui.Invalidate(t.env)
				return true
			}
		}
		if math.Abs(ev.Scroll.Y) > 0 && maxScrollY > 0 {
			next := clampFloat(t.state.scrollY+ev.Scroll.Y*16, 0, maxScrollY)
			if next != t.state.scrollY {
				t.state.scrollY = next
				ggui.Invalidate(t.env)
				return true
			}
		}
		return false
	}
	return t.Pointer(ev, nil)
}
