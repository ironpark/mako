package gui

import (
	"image/color"
	"math"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/ui/icons"
	uitheme "github.com/ironpark/ggui/ui/theme"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

const (
	canvasMinZoom = 0.25
	canvasMaxZoom = 4.0
)

type canvasViewState struct {
	panX, panY    float64
	zoom          float64
	selectedHitID string
	hoveredHitID  string
	lastRect      ggui.Rect
}

func canvasIconSVG(name string) *icons.SVG {
	if svg := graphIconSVG(name); svg != nil {
		return svg
	}
	return graphIconSVG("square")
}

func canvasToneColor(tone string, env ggui.Env) color.Color {
	th := uitheme.Of(env)
	switch tone {
	case "muted":
		return resolveOr(skin.Muted, env, th.MutedFg)
	case "faint":
		return resolveOr(skin.ExtraMuted, env, th.MutedFg)
	case "accent":
		return resolveOr(skin.AccentBright, env, th.Primary)
	case "success":
		return resolveOr(skin.DiffAdded, env, th.Primary)
	case "warning":
		return resolveOr(skin.DotWaiting, env, th.Primary)
	case "danger":
		return resolveOr(skin.DiffRemoved, env, th.Primary)
	case "surface":
		return resolveOr(skin.Surface1, env, th.Card)
	case "surfaceAlt":
		return resolveOr(skin.Surface2, env, th.Muted)
	case "border":
		return resolveOr(skin.Border, env, th.Border)
	case "", "text", "ink":
		return resolveOr(skin.Fg, env, th.Fg)
	}
	return resolveOr(skin.Fg, env, th.Fg)
}

func canvasCmdAlpha(cmd *protocol.UiCanvasCommand) float64 {
	if cmd == nil || cmd.Alpha == nil {
		return 1.0
	}
	return clampFloat(cmd.GetAlpha(), 0, 1)
}

func (v *treeView) canvasNode(n *protocol.UiNode) ggui.Widget {
	id := n.GetId()
	st := v.form.canvasState(id)
	if st.zoom <= 0 {
		st.zoom = 1.0
	}

	tick := ggui.State(0)
	bump := func() { tick.Update(func(x int) int { return x + 1 }) }

	interactive := n.GetChange() && !n.GetDisabled()
	var onSelect func(string)
	if interactive {
		onSelect = func(hitID string) {
			v.send(id, protocol.UiChange, hitID)
		}
	}

	panEnabled := n.GetCanvasPan()
	zoomEnabled := n.GetCanvasZoom()
	fill := n.GetFill() || n.GetGrow() > 0

	canvas := &pluginCanvasWidget{
		state:       st,
		commands:    n.GetCanvasCommands(),
		prefW:       n.GetCanvasWidth(),
		prefH:       n.GetCanvasHeight(),
		maxHeight:   n.GetMaxHeight(),
		fill:        fill,
		grow:        n.GetGrow(),
		panEnabled:  panEnabled,
		zoomEnabled: zoomEnabled,
		interactive: interactive,
		onSelect:    onSelect,
		tick:        tick,
		bump:        bump,
		name:        cmpTone(n.GetText(), T("Canvas")),
	}
	canvas.Role = ggui.RoleGroup
	canvas.SetName(canvas.name)
	canvas.SetKey("canvas:" + v.block + ":" + v.scope + ":" + id)
	canvas.SetInert(n.GetDisabled())

	var out ggui.Widget = canvas
	if panEnabled || zoomEnabled {
		var btns []ggui.Widget
		if zoomEnabled {
			btns = append(btns,
				ui.Tooltip(
					ui.ButtonOf(widget.Text("+").Size(13).Weight(ggui.WeightMedium).Color(skin.Fg).NoWrap(), func() {
						st.zoom = clampFloat(st.zoom*1.25, canvasMinZoom, canvasMaxZoom)
						bump()
					}).Ghost().Pad(2, 6).Radius(5).Name(T("Zoom in")),
					T("Zoom in"),
				),
				ui.Tooltip(
					ui.ButtonOf(widget.Text("−").Size(13).Weight(ggui.WeightMedium).Color(skin.Fg).NoWrap(), func() {
						st.zoom = clampFloat(st.zoom/1.25, canvasMinZoom, canvasMaxZoom)
						bump()
					}).Ghost().Pad(2, 6).Radius(5).Name(T("Zoom out")),
					T("Zoom out"),
				),
			)
		}
		btns = append(btns,
			ui.Tooltip(
				ui.ButtonOf(icon("refresh-cw").Size(12).Color(skin.Muted), func() {
					st.panX = 0
					st.panY = 0
					st.zoom = 1.0
					bump()
				}).Ghost().Pad(4).Radius(5).Name(T("Reset")),
				T("Reset"),
			),
		)
		controls := widget.Align(
			widget.Padding(
				widget.Box(widget.Row(btns...).Gap(2).Align(widget.AlignCenter)).
					Fill(skin.Surface0).Border(1, skin.Border).Radius(7).Pad(2),
				8,
			),
		).At(1, 0)
		out = overlayStack{base: canvas, overlay: controls}
	}

	if n.GetText() == "" {
		return out
	}
	if fill {
		out = widget.Expanded(out)
	}
	return widget.Column(
		widget.Text(n.GetText()).Size(textSize(n.GetSize())).Weight(textWeight(n.GetWeight())).Color(toneColor(cmpTone(n.GetTone(), "muted"))),
		out,
	).Gap(6).Stretch()
}

// overlayStack lays out base to determine its natural or constrained size and
// then lays out overlay tightly within base's size so aligned overlays never
// inflate a non-fill base widget to the viewport's MaxH.
type overlayStack struct {
	base, overlay ggui.Widget
}

func (o overlayStack) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	sz := o.base.Layout(c, env)
	o.overlay.Layout(ggui.Tight(sz), env)
	return sz
}

func (o overlayStack) Paint(dst *ggui.Canvas, r ggui.Rect) {
	dst.Paint(o.base, r)
	dst.Paint(o.overlay, r)
}

type pluginCanvasWidget struct {
	ggui.Interactive
	state       *canvasViewState
	commands    []*protocol.UiCanvasCommand
	prefW       float64
	prefH       float64
	maxHeight   float64
	fill        bool
	grow        float64
	panEnabled  bool
	zoomEnabled bool
	interactive bool
	onSelect    func(string)
	tick        *ggui.StateValue[int]
	bump        func()
	name        string

	env   ggui.Env
	theme *uitheme.Theme
	font  *ggui.Font
	rect  ggui.Rect

	colBg     color.Color
	colBorder color.Color

	dragging bool
	moved    bool
	downPos  ggui.Point
	lastPos  ggui.Point
}

func (p *pluginCanvasWidget) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	p.Sync()
	if p.tick != nil {
		_ = p.tick.Get()
	}
	p.env = env
	p.theme = uitheme.Of(env)
	p.font = env.ResolveText(ggui.TextStyle{Size: 12}).Font
	p.colBg = resolveOr(skin.Surface0, env, p.theme.Card)
	p.colBorder = resolveOr(skin.Border, env, p.theme.Border)

	w := p.prefW
	if w <= 0 {
		if !math.IsInf(c.MaxW, 1) && c.MaxW > 0 {
			w = c.MaxW
		} else {
			w = 420
		}
	} else if !math.IsInf(c.MaxW, 1) && c.MaxW > 0 && w > c.MaxW {
		w = c.MaxW
	}

	h := p.prefH
	if h <= 0 {
		h = 220
	}
	if p.maxHeight > 0 && h > p.maxHeight {
		h = p.maxHeight
	}
	if (p.fill || p.grow > 0) && !math.IsInf(c.MaxH, 1) && c.MaxH > 0 {
		h = c.MaxH
		if p.maxHeight > 0 && h > p.maxHeight {
			h = p.maxHeight
		}
	} else if !math.IsInf(c.MaxH, 1) && c.MaxH > 0 && h > c.MaxH {
		h = c.MaxH
	}
	return c.Constrain(ggui.Sz(w, h))
}

func (p *pluginCanvasWidget) Describe() ggui.Node {
	return ggui.Node{
		Role:     ggui.RoleGroup,
		Name:     p.name,
		Disabled: p.IsInert(),
		Actions:  ggui.ActionFocus,
	}
}

func (p *pluginCanvasWidget) CaptureDrag() bool      { return !p.IsInert() && p.panEnabled }
func (p *pluginCanvasWidget) CaptureTouchDrag() bool { return !p.IsInert() && p.panEnabled }
func (p *pluginCanvasWidget) HandleKey(ggui.KeyEvent) {}

func (p *pluginCanvasWidget) worldToScreen(x, y float64, r ggui.Rect) ggui.Point {
	return ggui.Pt(r.Origin.X+p.state.panX+x*p.state.zoom, r.Origin.Y+p.state.panY+y*p.state.zoom)
}

func (p *pluginCanvasWidget) cmdScreenBounds(dst *ggui.Canvas, cmd *protocol.UiCanvasCommand, r ggui.Rect) ggui.Rect {
	z := p.state.zoom
	switch cmd.GetOp() {
	case "roundRect":
		pt := p.worldToScreen(cmd.GetX(), cmd.GetY(), r)
		return ggui.Rct(pt, ggui.Sz(max(cmd.GetW()*z, 1), max(cmd.GetH()*z, 1)))
	case "circle":
		c := p.worldToScreen(cmd.GetX(), cmd.GetY(), r)
		rad := max(cmd.GetR()*z, 4)
		return ggui.Rct(ggui.Pt(c.X-rad, c.Y-rad), ggui.Sz(rad*2, rad*2))
	case "line":
		a := p.worldToScreen(cmd.GetX1(), cmd.GetY1(), r)
		b := p.worldToScreen(cmd.GetX2(), cmd.GetY2(), r)
		minX, maxX := min(a.X, b.X), max(a.X, b.X)
		minY, maxY := min(a.Y, b.Y), max(a.Y, b.Y)
		return ggui.Rct(ggui.Pt(minX-4, minY-4), ggui.Sz(max(maxX-minX, 1)+8, max(maxY-minY, 1)+8))
	case "bezier":
		p0 := p.worldToScreen(cmd.GetX1(), cmd.GetY1(), r)
		c1 := p.worldToScreen(cmd.GetCx1(), cmd.GetCy1(), r)
		c2 := p.worldToScreen(cmd.GetCx2(), cmd.GetCy2(), r)
		p1 := p.worldToScreen(cmd.GetX2(), cmd.GetY2(), r)
		minX := min(min(p0.X, p1.X), min(c1.X, c2.X))
		maxX := max(max(p0.X, p1.X), max(c1.X, c2.X))
		minY := min(min(p0.Y, p1.Y), min(c1.Y, c2.Y))
		maxY := max(max(p0.Y, p1.Y), max(c1.Y, c2.Y))
		return ggui.Rct(ggui.Pt(minX-4, minY-4), ggui.Sz(max(maxX-minX, 1)+8, max(maxY-minY, 1)+8))
	case "text":
		pt := p.worldToScreen(cmd.GetX(), cmd.GetY(), r)
		fs := max(cmd.GetSize(), 12) * z
		tw := 24.0
		if dst != nil && p.font != nil && cmd.GetText() != "" {
			tw = max(dst.TextWidth(cmd.GetText(), p.font, fs), 8)
		} else if cmd.GetText() != "" {
			tw = float64(len(cmd.GetText())) * fs * 0.55
		}
		return ggui.Rct(pt, ggui.Sz(tw, fs*1.3))
	case "icon":
		pt := p.worldToScreen(cmd.GetX(), cmd.GetY(), r)
		sz := max(cmd.GetSize(), 16) * z
		return ggui.Rct(pt, ggui.Sz(sz, sz))
	}
	return ggui.Rect{}
}

func (p *pluginCanvasWidget) Paint(dst *ggui.Canvas, r ggui.Rect) {
	p.rect = r
	p.state.lastRect = r

	dst.DescribeNode(r, p, func(dst *ggui.Canvas) {
		cursor := ggui.CursorShapeDefault
		if p.state.hoveredHitID != "" {
			cursor = ggui.CursorShapePointer
		} else if p.panEnabled {
			cursor = ggui.CursorShapeMove
		}
		p.Hit(dst, r, p, cursor)
		if p.zoomEnabled {
			dst.Shortcut(ggui.MustChord("cmd+="), func() {
				p.state.zoom = clampFloat(p.state.zoom*1.25, canvasMinZoom, canvasMaxZoom)
				if p.bump != nil {
					p.bump()
				}
				ggui.Invalidate(p.env)
			})
			dst.Shortcut(ggui.MustChord("cmd+-"), func() {
				p.state.zoom = clampFloat(p.state.zoom/1.25, canvasMinZoom, canvasMaxZoom)
				if p.bump != nil {
					p.bump()
				}
				ggui.Invalidate(p.env)
			})
			dst.Shortcut(ggui.MustChord("cmd+0"), func() {
				p.state.panX = 0
				p.state.panY = 0
				p.state.zoom = 1.0
				if p.bump != nil {
					p.bump()
				}
				ggui.Invalidate(p.env)
			})
		}

		dst.FillRoundRect(r, 8, p.colBg)
		dst.ClipRoundRect(r, 8, func(dst *ggui.Canvas) {
			z := p.state.zoom
			for _, cmd := range p.commands {
				alpha := canvasCmdAlpha(cmd)
				switch cmd.GetOp() {
				case "roundRect":
					pt := p.worldToScreen(cmd.GetX(), cmd.GetY(), r)
					rect := ggui.Rct(pt, ggui.Sz(cmd.GetW()*z, cmd.GetH()*z))
					rad := cmd.GetR() * z
					fillTone := cmd.GetFillTone()
					if fillTone == "" && cmd.GetStrokeTone() == "" && cmd.GetTone() != "" {
						fillTone = cmd.GetTone()
					}
					if fillTone != "" {
						col := graphFade(canvasToneColor(fillTone, p.env), alpha)
						dst.FillRoundRect(rect, rad, col)
					}
					if cmd.GetStrokeTone() != "" {
						sw := cmd.GetStrokeWidth()
						if sw <= 0 {
							sw = 1.5
						}
						col := graphFade(canvasToneColor(cmd.GetStrokeTone(), p.env), alpha)
						dst.StrokeRoundRect(rect, rad, sw*z, col)
					}
				case "circle":
					center := p.worldToScreen(cmd.GetX(), cmd.GetY(), r)
					rad := cmd.GetR() * z
					fillTone := cmd.GetFillTone()
					if fillTone == "" && cmd.GetStrokeTone() == "" && cmd.GetTone() != "" {
						fillTone = cmd.GetTone()
					}
					if fillTone != "" {
						col := graphFade(canvasToneColor(fillTone, p.env), alpha)
						dst.FillCircle(center, rad, col)
					}
					if cmd.GetStrokeTone() != "" {
						sw := cmd.GetStrokeWidth()
						if sw <= 0 {
							sw = 1.5
						}
						col := graphFade(canvasToneColor(cmd.GetStrokeTone(), p.env), alpha)
						var path ggui.Path
						path.Arc(center.X, center.Y, rad, 0, 2*math.Pi)
						path.Close()
						dst.StrokePath(&path, sw*z, col)
					}
				case "line":
					a := p.worldToScreen(cmd.GetX1(), cmd.GetY1(), r)
					b := p.worldToScreen(cmd.GetX2(), cmd.GetY2(), r)
					sw := cmd.GetStrokeWidth()
					if sw <= 0 {
						sw = 1.5
					}
					tone := cmpTone(cmd.GetStrokeTone(), cmd.GetTone())
					col := graphFade(canvasToneColor(tone, p.env), alpha)
					dst.StrokeLine(a, b, sw*z, col)
				case "bezier":
					p0 := p.worldToScreen(cmd.GetX1(), cmd.GetY1(), r)
					c1 := p.worldToScreen(cmd.GetCx1(), cmd.GetCy1(), r)
					c2 := p.worldToScreen(cmd.GetCx2(), cmd.GetCy2(), r)
					p1 := p.worldToScreen(cmd.GetX2(), cmd.GetY2(), r)
					sw := cmd.GetStrokeWidth()
					if sw <= 0 {
						sw = 1.5
					}
					tone := cmpTone(cmd.GetStrokeTone(), cmd.GetTone())
					col := graphFade(canvasToneColor(tone, p.env), alpha)
					var path ggui.Path
					path.MoveTo(p0.X, p0.Y)
					path.CubicTo(c1.X, c1.Y, c2.X, c2.Y, p1.X, p1.Y)
					dst.StrokePath(&path, sw*z, col)
				case "text":
					if cmd.GetText() != "" && p.font != nil {
						pt := p.worldToScreen(cmd.GetX(), cmd.GetY(), r)
						fs := max(cmd.GetSize(), 12) * z
						col := graphFade(canvasToneColor(cmd.GetTone(), p.env), alpha)
						dst.DrawText(cmd.GetText(), p.font, fs, pt, col)
					}
				case "icon":
					if svg := canvasIconSVG(cmd.GetIcon()); svg != nil {
						pt := p.worldToScreen(cmd.GetX(), cmd.GetY(), r)
						sz := max(cmd.GetSize(), 16) * z
						col := graphFade(canvasToneColor(cmd.GetTone(), p.env), alpha)
						svg.Draw(dst, ggui.Rct(pt, ggui.Sz(sz, sz)), col, 0)
					}
				}

				if cmd.GetHitId() != "" {
					bounds := p.cmdScreenBounds(dst, cmd, r).Intersect(r)
					if !bounds.Empty() {
						dst.Leaf(bounds, ggui.Node{
							Role:     ggui.RoleButton,
							Name:     cmpTone(cmd.GetHitId(), cmd.GetText()),
							Selected: cmd.GetHitId() == p.state.selectedHitID,
							Disabled: p.IsInert() || !p.interactive,
						})
					}
				} else if cmd.GetOp() == "text" && cmd.GetText() != "" {
					bounds := p.cmdScreenBounds(dst, cmd, r).Intersect(r)
					if !bounds.Empty() {
						dst.Leaf(bounds, ggui.Node{
							Role: ggui.RoleText,
							Name: cmd.GetText(),
						})
					}
				}
			}
		})
		dst.StrokeRoundRect(r, 8, 1, p.colBorder)
	})
}

func (p *pluginCanvasWidget) hitTest(pt ggui.Point) string {
	if !p.rect.Contains(pt) {
		return ""
	}
	z := p.state.zoom
	for i := len(p.commands) - 1; i >= 0; i-- {
		cmd := p.commands[i]
		hitID := cmd.GetHitId()
		if hitID == "" {
			continue
		}
		switch cmd.GetOp() {
		case "roundRect":
			bounds := p.cmdScreenBounds(nil, cmd, p.rect)
			if bounds.Contains(pt) {
				return hitID
			}
		case "circle":
			c := p.worldToScreen(cmd.GetX(), cmd.GetY(), p.rect)
			rad := max(cmd.GetR()*z, 4)
			if math.Hypot(pt.X-c.X, pt.Y-c.Y) <= rad {
				return hitID
			}
		case "line":
			a := p.worldToScreen(cmd.GetX1(), cmd.GetY1(), p.rect)
			b := p.worldToScreen(cmd.GetX2(), cmd.GetY2(), p.rect)
			tol := max(cmd.GetStrokeWidth()*z*0.5+4, 6)
			if pointSegDist(pt, a, b) <= tol {
				return hitID
			}
		case "bezier":
			p0 := p.worldToScreen(cmd.GetX1(), cmd.GetY1(), p.rect)
			c1 := p.worldToScreen(cmd.GetCx1(), cmd.GetCy1(), p.rect)
			c2 := p.worldToScreen(cmd.GetCx2(), cmd.GetCy2(), p.rect)
			p1 := p.worldToScreen(cmd.GetX2(), cmd.GetY2(), p.rect)
			tol := max(cmd.GetStrokeWidth()*z*0.5+4, 6)
			if pointCubicDist(pt, p0, c1, c2, p1) <= tol {
				return hitID
			}
		case "text", "icon":
			bounds := p.cmdScreenBounds(nil, cmd, p.rect)
			if bounds.Contains(pt) {
				return hitID
			}
		}
	}
	return ""
}

func pointSegDist(p, a, b ggui.Point) float64 {
	dx, dy := b.X-a.X, b.Y-a.Y
	lenSq := dx*dx + dy*dy
	if lenSq <= 1e-9 {
		return math.Hypot(p.X-a.X, p.Y-a.Y)
	}
	t := clampFloat(((p.X-a.X)*dx+(p.Y-a.Y)*dy)/lenSq, 0, 1)
	proj := ggui.Pt(a.X+t*dx, a.Y+t*dy)
	return math.Hypot(p.X-proj.X, p.Y-proj.Y)
}

func pointCubicDist(pt, p0, c1, c2, p1 ggui.Point) float64 {
	const steps = 16
	best := math.Inf(1)
	prev := p0
	for i := 1; i <= steps; i++ {
		t := float64(i) / float64(steps)
		u := 1 - t
		x := u*u*u*p0.X + 3*u*u*t*c1.X + 3*u*t*t*c2.X + t*t*t*p1.X
		y := u*u*u*p0.Y + 3*u*u*t*c1.Y + 3*u*t*t*c2.Y + t*t*t*p1.Y
		cur := ggui.Pt(x, y)
		if d := pointSegDist(pt, prev, cur); d < best {
			best = d
		}
		prev = cur
	}
	return best
}

func (p *pluginCanvasWidget) HandlePointer(ev ggui.PointerEvent) bool {
	if p.IsInert() {
		return false
	}
	switch ev.Kind {
	case ggui.PointerMove:
		hit := p.hitTest(ev.Pos)
		if hit != p.state.hoveredHitID {
			p.state.hoveredHitID = hit
			ggui.Invalidate(p.env)
		}
		return p.Pointer(ev, nil)
	case ggui.PointerExit:
		if p.state.hoveredHitID != "" {
			p.state.hoveredHitID = ""
			ggui.Invalidate(p.env)
		}
		return p.Pointer(ev, nil)
	case ggui.PointerDown:
		if ev.Button != ggui.MouseButtonLeft {
			return false
		}
		p.dragging = true
		p.moved = false
		p.downPos = ev.Pos
		p.lastPos = ev.Pos
		return p.Pointer(ev, nil)
	case ggui.PointerDrag:
		if !p.dragging {
			return false
		}
		if !p.moved && math.Hypot(ev.Pos.X-p.downPos.X, ev.Pos.Y-p.downPos.Y) > 4 {
			p.moved = true
		}
		if p.moved && p.panEnabled {
			p.state.panX += ev.Pos.X - p.lastPos.X
			p.state.panY += ev.Pos.Y - p.lastPos.Y
			ggui.Invalidate(p.env)
		}
		p.lastPos = ev.Pos
		return true
	case ggui.PointerUp:
		wasDrag, wasMoved := p.dragging, p.moved
		p.dragging = false
		p.moved = false
		if wasDrag && !wasMoved && ev.Button == ggui.MouseButtonLeft {
			if hit := p.hitTest(ev.Pos); hit != "" {
				p.state.selectedHitID = hit
				if p.onSelect != nil {
					p.onSelect(hit)
				}
				ggui.Invalidate(p.env)
			}
		}
		return p.Pointer(ev, nil)
	case ggui.PointerScroll:
		if !(p.zoomEnabled && (ev.Mods.Ctrl || ev.Mods.Meta || ev.Mods.Shift)) {
			return false
		}
		if ev.Scroll.Y != 0 {
			factor := clampFloat(1.0-ev.Scroll.Y*0.08, 0.75, 1.35)
			p.state.zoom = clampFloat(p.state.zoom*factor, canvasMinZoom, canvasMaxZoom)
			ggui.Invalidate(p.env)
			return true
		}
		return false
	}
	return p.Pointer(ev, nil)
}
