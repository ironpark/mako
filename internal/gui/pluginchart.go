package gui

import (
	"strconv"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui/chart"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/protocol"
)

// A chart node of a block's tree, drawn with ggui's charts: a line or bar
// chart of its series over its labels, or a sparkline, a line alone with
// no axes, as tall as a line of text. A series's colour is its tone's
// token, or the theme's chart colours in order for none; the daemon
// checked the numbers finite and few.

// chartAnimation is short: a block is drawn again on every update.
const chartAnimation = 250 * time.Millisecond

func chartNode(n *protocol.UiNode) ggui.Widget {
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
	name := cmpTone(n.GetText(), T("Chart"))
	var c *chart.Widget
	switch n.GetChart() {
	case "bar":
		c = chart.Bar(data, config).Height(160).Legend(len(config) > 1)
	case "sparkline":
		c = chart.Line(data, config).Height(36).Axes(false, false).Grid(false).Legend(false).Dots(false).
			Curve(chart.CurveLinear).TooltipCursor(false).StrokeWidth(1.5)
	case "donut":
		c = chart.Pie(data, config).Height(190).InnerRadius(0.55).OuterRadius(0.85).Legend(true).AnimationDelay(0)
	case "pie":
		c = chart.Pie(data, config).Height(190).InnerRadius(0).OuterRadius(0.85).Legend(true).AnimationDelay(0)
	default:
		c = chart.Line(data, config).Height(160).Legend(len(config) > 1).Dots(false).Curve(chart.CurveMonotone)
	}
	c = c.Animation(chartAnimation).Name(name)
	if n.GetText() == "" {
		return c
	}
	return widget.Column(
		widget.Text(n.GetText()).Size(textSize(n.GetSize())).Weight(textWeight(n.GetWeight())).Color(toneColor(cmpTone(n.GetTone(), "muted"))),
		c,
	).Gap(6).Stretch()
}
