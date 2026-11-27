package gui

import (
	"encoding/json"
	"fmt"
	"image/color"
	"math"
	"strconv"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui/chart"
	uitheme "github.com/ironpark/ggui/ui/theme"

	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// A chart node of a block's tree, drawn with ggui's charts (line, area, bar,
// stacked-bar, sparkline, donut, pie) or a 2D heatmap grid canvas. A series's
// colour is its tone's token, or the theme's chart colours in order for none;
// the daemon checked the numbers finite and few.

// chartAnimation is short: a block is drawn again on every update.
const chartAnimation = 250 * time.Millisecond

func (v *treeView) chartNode(n *protocol.UiNode) ggui.Widget {
	id := n.GetId()
	name := cmpTone(n.GetText(), T("Chart"))
	interactive := n.GetChange() && !n.GetDisabled()
	fill := n.GetFill() || n.GetGrow() > 0
	maxHeight := n.GetMaxHeight()
	caption := n.GetText()
	captionTone := cmpTone(n.GetTone(), "muted")

	if n.GetChart() == "heatmap" {
		var onHeatSelect func(string)
		if interactive {
			onHeatSelect = func(payload string) {
				v.send(id, protocol.UiChange, payload)
			}
		}
		hm := &pluginHeatmapCanvas{
			series:      n.GetSeries(),
			labels:      n.GetLabels(),
			name:        name,
			interactive: interactive,
			onSelect:    onHeatSelect,
			selRow:      -1,
			selCol:      -1,
		}
		hm.Role = ggui.RoleGroup
		hm.SetName(name)
		hm.SetKey("heatmap:" + v.block + ":" + v.scope + ":" + id)
		hm.SetInert(n.GetDisabled())
		return &pluginChartBox{
			inner:       hm,
			defaultH:    160,
			maxHeight:   maxHeight,
			fill:        fill,
			caption:     caption,
			captionTone: captionTone,
			captionSize: textSize(n.GetSize()),
		}
	}

	config := make(chart.Config, len(n.GetSeries()))
	points := 0
	for j, s := range n.GetSeries() {
		config[j] = chart.Series{Key: strconv.Itoa(j), Label: s.GetName()}
		if s.GetTone() != "" {
			config[j].Color = toneColor(s.GetTone())
		}
		points = max(points, len(s.GetValues()))
	}
	data := make([]chart.Datum, points)
	for i := range data {
		label := strconv.Itoa(i + 1)
		if i < len(n.GetLabels()) {
			label = n.GetLabels()[i]
		}
		data[i] = chart.Datum{Label: label, Values: map[string]float64{}}
		for j, s := range n.GetSeries() {
			if i < len(s.GetValues()) {
				data[i].Values[strconv.Itoa(j)] = s.GetValues()[i]
			}
		}
	}

	var c *chart.Widget
	defaultH := 160.0
	switch n.GetChart() {
	case "bar":
		c = chart.Bar(data, config).Height(defaultH).Legend(len(config) > 1)
	case "stacked-bar":
		c = chart.Bar(data, config).Stack(chart.StackStacked).Height(defaultH).Legend(len(config) > 1)
	case "area":
		c = chart.Area(data, config).Height(defaultH).Legend(len(config) > 1).Curve(chart.CurveMonotone).FillOpacity(0.28)
	case "sparkline":
		defaultH = 36
		c = chart.Line(data, config).Height(defaultH).Axes(false, false).Grid(false).Legend(false).Dots(false).
			Curve(chart.CurveLinear).TooltipCursor(false).StrokeWidth(1.5)
	case "donut":
		defaultH = 190
		c = chart.Pie(data, config).Height(defaultH).InnerRadius(0.55).OuterRadius(0.85).Legend(true).AnimationDelay(0)
	case "pie":
		defaultH = 190
		c = chart.Pie(data, config).Height(defaultH).InnerRadius(0).OuterRadius(0.85).Legend(true).AnimationDelay(0)
	default:
		c = chart.Line(data, config).Height(defaultH).Legend(len(config) > 1).Dots(false).Curve(chart.CurveMonotone)
	}
	c = c.Animation(chartAnimation).Name(name)
	if interactive {
		c = c.OnSelect(func(idx int, d chart.Datum) {
			if idx < 0 {
				return
			}
			payload, err := json.Marshal(struct {
				Index int    `json:"index"`
				Label string `json:"label,omitempty"`
			}{
				Index: idx,
				Label: d.Label,
			})
			if err != nil {
				return
			}
			v.send(id, protocol.UiChange, string(payload))
		})
	}
	return &pluginChartBox{
		inner:       c,
		chartWidget: c,
		defaultH:    defaultH,
		maxHeight:   maxHeight,
		fill:        fill,
		caption:     caption,
		captionTone: captionTone,
		captionSize: textSize(n.GetSize()),
	}
}

// pluginChartBox sizes a chart or heatmap dynamically for maxHeight/fill/grow
// and paints an optional visual caption without emitting a preceding RoleText
// semantic node ahead of the interactive chart plot.
type pluginChartBox struct {
	inner       ggui.Widget
	chartWidget *chart.Widget
	defaultH    float64
	maxHeight   float64
	fill        bool
	caption     string
	captionTone string
	captionSize float64

	font     *ggui.Font
	capColor color.Color
	capH     float64
}

func (b *pluginChartBox) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	th := uitheme.Of(env)
	b.font = env.ResolveText(ggui.TextStyle{Size: b.captionSize}).Font
	b.capColor = resolveOr(toneColor(b.captionTone), env, th.MutedFg)
	b.capH = 0
	if b.caption != "" {
		b.capH = 22
	}

	targetH := b.defaultH + b.capH
	if b.maxHeight > 0 && targetH > b.maxHeight {
		targetH = b.maxHeight
	}
	if b.fill && !math.IsInf(c.MaxH, 1) && c.MaxH > 0 {
		targetH = c.MaxH
		if b.maxHeight > 0 && targetH > b.maxHeight {
			targetH = b.maxHeight
		}
	} else if !math.IsInf(c.MaxH, 1) && c.MaxH > 0 && targetH > c.MaxH {
		targetH = c.MaxH
	}

	plotH := max(targetH-b.capH, 40)
	if b.chartWidget != nil {
		b.chartWidget.Height(plotH)
	}
	innerC := ggui.Constraints{
		MinW: c.MinW,
		MaxW: c.MaxW,
		MinH: plotH,
		MaxH: plotH,
	}
	sz := b.inner.Layout(innerC, env)
	return c.Constrain(ggui.Sz(sz.W, sz.H+b.capH))
}

func (b *pluginChartBox) Paint(dst *ggui.Canvas, r ggui.Rect) {
	if b.caption != "" && b.font != nil {
		fitted := dst.FitText(b.caption, b.font, b.captionSize, max(r.Size.W, 24))
		dst.DrawText(fitted, b.font, b.captionSize, r.Origin, b.capColor)
	}
	plotRect := ggui.Rct(ggui.Pt(r.Origin.X, r.Origin.Y+b.capH), ggui.Sz(r.Size.W, max(r.Size.H-b.capH, 0)))
	dst.Paint(b.inner, plotRect)
}

type heatmapCellHit struct {
	row, col int
	val      float64
	series   string
	label    string
	rect     ggui.Rect
}

type pluginHeatmapCanvas struct {
	ggui.Interactive
	series      []*protocol.UiSeries
	labels      []string
	name        string
	interactive bool
	onSelect    func(string)
	selRow      int
	selCol      int

	env         ggui.Env
	theme       *uitheme.Theme
	font        *ggui.Font
	rect        ggui.Rect
	cells       []heatmapCellHit
	colBg       color.Color
	colSurface2 color.Color
	colBorder   color.Color
	colFg       color.Color
	colMuted    color.Color
	colAccent   color.Color
}

func (h *pluginHeatmapCanvas) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	h.Sync()
	h.env = env
	h.theme = uitheme.Of(env)
	h.font = env.ResolveText(ggui.TextStyle{Size: 11}).Font
	h.colBg = resolveOr(skin.Surface0, env, h.theme.Card)
	h.colSurface2 = resolveOr(skin.Surface2, env, h.theme.Muted)
	h.colBorder = resolveOr(skin.Border, env, h.theme.Border)
	h.colFg = resolveOr(skin.Fg, env, h.theme.Fg)
	h.colMuted = resolveOr(skin.Muted, env, h.theme.MutedFg)
	h.colAccent = resolveOr(skin.AccentBright, env, h.theme.Primary)

	w := c.MaxW
	if math.IsInf(w, 1) || w <= 0 {
		w = 400
	}
	ht := c.MaxH
	if math.IsInf(ht, 1) || ht <= 0 {
		ht = 160
	}
	return c.Constrain(ggui.Sz(w, ht))
}

func (h *pluginHeatmapCanvas) Describe() ggui.Node {
	return ggui.Node{
		Role:     ggui.RoleGroup,
		Name:     h.name,
		Disabled: h.IsInert(),
		Actions:  ggui.ActionFocus,
	}
}

func (h *pluginHeatmapCanvas) HandleKey(ggui.KeyEvent) {}

func blendHeatmapColor(base, target color.Color, t float64) color.Color {
	t = clampFloat(t, 0, 1)
	b := color.NRGBAModel.Convert(base).(color.NRGBA)
	g := color.NRGBAModel.Convert(target).(color.NRGBA)
	lerp := func(a, z uint8) uint8 {
		return uint8(clampFloat(float64(a)+(float64(z)-float64(a))*t, 0, 255))
	}
	return color.NRGBA{
		R: lerp(b.R, g.R),
		G: lerp(b.G, g.G),
		B: lerp(b.B, g.B),
		A: lerp(b.A, g.A),
	}
}

func (h *pluginHeatmapCanvas) Paint(dst *ggui.Canvas, r ggui.Rect) {
	h.rect = r
	h.cells = h.cells[:0]

	dst.DescribeNode(r, h, func(dst *ggui.Canvas) {
		cursor := ggui.CursorShapeDefault
		if h.interactive {
			cursor = ggui.CursorShapePointer
		}
		h.Hit(dst, r, h, cursor)
		dst.FillRoundRect(r, 8, h.colBg)

		rows := len(h.series)
		cols := len(h.labels)
		for _, s := range h.series {
			cols = max(cols, len(s.GetValues()))
		}
		if rows == 0 || cols == 0 {
			if h.font != nil {
				msg := T("No data.")
				tw := dst.TextWidth(msg, h.font, 12)
				lx := r.Origin.X + (r.Size.W-tw)/2
				ly := r.Origin.Y + r.Size.H/2 - 7
				dst.DrawText(msg, h.font, 12, ggui.Pt(lx, ly), h.colMuted)
				dst.Leaf(ggui.Rct(ggui.Pt(lx, ly), ggui.Sz(tw, 16)), ggui.Node{Role: ggui.RoleText, Name: msg})
			}
			dst.StrokeRoundRect(r, 8, 1, h.colBorder)
			return
		}

		minVal, maxVal := math.Inf(1), math.Inf(-1)
		for _, s := range h.series {
			for _, v := range s.GetValues() {
				if math.IsNaN(v) || math.IsInf(v, 0) {
					continue
				}
				minVal = min(minVal, v)
				maxVal = max(maxVal, v)
			}
		}
		if math.IsInf(minVal, 1) || math.IsInf(maxVal, -1) {
			minVal, maxVal = 0, 1
		}
		span := maxVal - minVal
		if span <= 0 {
			span = 1
		}

		hasRowLabels := false
		for _, s := range h.series {
			if s.GetName() != "" {
				hasRowLabels = true
				break
			}
		}
		hasColLabels := len(h.labels) > 0

		rowLabelW := 0.0
		if hasRowLabels {
			rowLabelW = min(76.0, r.Size.W*0.24)
		}
		colLabelH := 0.0
		if hasColLabels {
			colLabelH = 20.0
		}
		const pad = 8.0
		gridX := r.Origin.X + pad + rowLabelW
		gridY := r.Origin.Y + pad + colLabelH
		gridW := max(r.Size.W-pad*2-rowLabelW, 24)
		gridH := max(r.Size.H-pad*2-colLabelH, 24)
		cellGap := 3.0
		cellW := max((gridW-cellGap*float64(cols-1))/float64(cols), 6)
		cellH := max((gridH-cellGap*float64(rows-1))/float64(rows), 6)

		if hasColLabels && h.font != nil {
			for c := range cols {
				lbl := strconv.Itoa(c + 1)
				if c < len(h.labels) {
					lbl = h.labels[c]
				}
				cx := gridX + float64(c)*(cellW+cellGap)
				fitted := dst.FitText(lbl, h.font, 10, cellW)
				tw := dst.TextWidth(fitted, h.font, 10)
				dst.DrawText(fitted, h.font, 10, ggui.Pt(cx+(cellW-tw)/2, r.Origin.Y+pad), h.colMuted)
			}
		}

		for rowIdx, s := range h.series {
			ry := gridY + float64(rowIdx)*(cellH+cellGap)
			if hasRowLabels && s.GetName() != "" && h.font != nil {
				fitted := dst.FitText(s.GetName(), h.font, 10.5, max(rowLabelW-6, 12))
				dst.DrawText(fitted, h.font, 10.5, ggui.Pt(r.Origin.X+pad, ry+(cellH-12)/2), h.colMuted)
			}
			rowTone := h.colAccent
			if s.GetTone() != "" {
				rowTone = resolveOr(toneColor(s.GetTone()), h.env, h.colAccent)
			}
			vals := s.GetValues()
			for colIdx := range cols {
				cx := gridX + float64(colIdx)*(cellW+cellGap)
				cellRect := ggui.Rct(ggui.Pt(cx, ry), ggui.Sz(cellW, cellH))
				cLabel := strconv.Itoa(colIdx + 1)
				if colIdx < len(h.labels) {
					cLabel = h.labels[colIdx]
				}
				if colIdx >= len(vals) || math.IsNaN(vals[colIdx]) || math.IsInf(vals[colIdx], 0) {
					dst.FillRoundRect(cellRect, 4, graphFade(h.colSurface2, 0.35))
					continue
				}
				val := vals[colIdx]
				norm := 0.18 + 0.82*((val-minVal)/span)
				col := blendHeatmapColor(h.colSurface2, rowTone, norm)
				dst.FillRoundRect(cellRect, 4, col)
				if rowIdx == h.selRow && colIdx == h.selCol {
					dst.StrokeRoundRect(cellRect, 4, 1.5, h.colFg)
				}
				if cellW >= 28 && cellH >= 18 && h.font != nil {
					txt := fmt.Sprintf("%.0f", val)
					if math.Abs(val-math.Round(val)) > 0.05 {
						txt = fmt.Sprintf("%.1f", val)
					}
					tw := dst.TextWidth(txt, h.font, 10)
					dst.DrawText(txt, h.font, 10, ggui.Pt(cx+(cellW-tw)/2, ry+(cellH-12)/2), h.colFg)
				}
				h.cells = append(h.cells, heatmapCellHit{
					row:    rowIdx,
					col:    colIdx,
					val:    val,
					series: s.GetName(),
					label:  cLabel,
					rect:   cellRect,
				})
				dst.Leaf(cellRect, ggui.Node{
					Role:     ggui.RoleCell,
					Name:     fmt.Sprintf("%s %s: %g", s.GetName(), cLabel, val),
					Selected: rowIdx == h.selRow && colIdx == h.selCol,
					Disabled: h.IsInert() || !h.interactive,
				})
			}
		}
		dst.StrokeRoundRect(r, 8, 1, h.colBorder)
	})
}

func (h *pluginHeatmapCanvas) HandlePointer(ev ggui.PointerEvent) bool {
	if h.IsInert() || !h.interactive {
		return false
	}
	if ev.Kind == ggui.PointerUp && ev.Button == ggui.MouseButtonLeft {
		for _, cell := range h.cells {
			if cell.rect.Contains(ev.Pos) {
				h.selRow = cell.row
				h.selCol = cell.col
				if h.onSelect != nil {
					payload, err := json.Marshal(struct {
						Row    int     `json:"row"`
						Col    int     `json:"col"`
						Series string  `json:"series,omitempty"`
						Label  string  `json:"label,omitempty"`
						Value  float64 `json:"value"`
					}{
						Row:    cell.row,
						Col:    cell.col,
						Series: cell.series,
						Label:  cell.label,
						Value:  cell.val,
					})
					if err == nil {
						h.onSelect(string(payload))
					}
				}
				ggui.Invalidate(h.env)
				return true
			}
		}
	}
	return h.Pointer(ev, nil)
}
