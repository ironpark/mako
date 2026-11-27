package gui

import (
	"strings"
	"testing"

	"github.com/ironpark/ggfx"
	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/gui/i18n"
	"github.com/ironpark/mako/internal/protocol"
)

func TestPluginChartNewTypesSizingAndOnChange(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	tree := &protocol.UiNode{
		Kind: "column",
		Gap:  8,
		Children: []*protocol.UiNode{
			{
				Id:        "/area",
				Kind:      "chart",
				Chart:     "area",
				Text:      "Latency Area",
				MaxHeight: 150,
				Change:    true,
				Labels:    []string{"T1", "T2", "T3", "T4"},
				Series:    []*protocol.UiSeries{{Name: "Latency", Tone: "accent", Values: []float64{12, 34, 22, 48}}},
			},
			{
				Id:        "/stack",
				Kind:      "chart",
				Chart:     "stacked-bar",
				Text:      "Build Stack",
				MaxHeight: 150,
				Labels:    []string{"W1", "W2"},
				Series: []*protocol.UiSeries{
					{Name: "Pass", Tone: "success", Values: []float64{10, 14}},
					{Name: "Fail", Tone: "danger", Values: []float64{2, 1}},
				},
			},
			{
				Id:        "/heat",
				Kind:      "chart",
				Chart:     "heatmap",
				Text:      "Heat Grid",
				MaxHeight: 150,
				Change:    true,
				Labels:    []string{"C1", "C2", "C3"},
				Series: []*protocol.UiSeries{
					{Name: "R1", Tone: "accent", Values: []float64{1, 5, 9}},
					{Name: "R2", Tone: "success", Values: []float64{4, 8, 2}},
				},
			},
		},
	}

	type ev struct{ id, kind, value string }
	var evs []ev
	p := NewUiTreeProbe(tree, func(id, kind, value string) {
		evs = append(evs, ev{id, kind, value})
	}, ggui.Sz(560, 520))
	defer p.Close()

	p.Draw(ggfx.NewImage(560, 520))
	sem := p.Semantics()
	semStr := sem.String()
	for _, want := range []string{"Latency Area", "Build Stack", "Heat Grid"} {
		if !strings.Contains(semStr, want) {
			t.Fatalf("chart semantics missing %q:\n%s", want, semStr)
		}
	}

	center := func(r ggui.Rect) ggui.Point {
		return ggui.Pt(r.Origin.X+r.Size.W/2, r.Origin.Y+r.Size.H/2)
	}
	areaSem, ok := sem.Find(ggui.RoleGroup, "Latency Area")
	if !ok {
		t.Fatalf("Latency Area RoleGroup not found:\n%s", semStr)
	}
	p.Click(center(areaSem.Rect))
	p.Frame()

	heatSem, ok := sem.Find(ggui.RoleGroup, "Heat Grid")
	if !ok {
		t.Fatalf("Heat Grid RoleGroup not found:\n%s", semStr)
	}
	p.Click(center(heatSem.Rect))
	p.Frame()

	var gotArea, gotHeat bool
	for _, e := range evs {
		if e.id == "/area" && e.kind == protocol.UiChange && e.value != "" {
			gotArea = true
		}
		if e.id == "/heat" && e.kind == protocol.UiChange && e.value != "" {
			gotHeat = true
		}
	}
	if !gotArea || !gotHeat {
		t.Fatalf("expected area and heatmap onChange events, got %+v", evs)
	}
}

func TestPluginChartSizingAndBoundarySafety(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	capTree := &protocol.UiNode{
		Kind: "column",
		Children: []*protocol.UiNode{
			{
				Id:        "/cap",
				Kind:      "chart",
				Chart:     "area",
				Text:      "Capped",
				MaxHeight: 110,
				Labels:    []string{"A", "B", "C"},
				Series:    []*protocol.UiSeries{{Name: "S", Values: []float64{1, 2, 3}}},
			},
		},
	}
	pCap := NewUiTreeProbe(capTree, nil, ggui.Sz(560, 480))
	defer pCap.Close()
	szCap := pCap.Draw(ggfx.NewImage(560, 480))
	if szCap.H > 160 {
		t.Fatalf("expected maxHeight=110 chart height <= 160, got %v", szCap.H)
	}

	expTree := &protocol.UiNode{
		Kind: "column",
		Fill: true,
		Children: []*protocol.UiNode{
			{
				Id:     "/exp",
				Kind:   "chart",
				Chart:  "area",
				Text:   "Expanded",
				Grow:   1,
				Labels: []string{"A", "B", "C"},
				Series: []*protocol.UiSeries{{Name: "S", Values: []float64{1, 2, 3}}},
			},
		},
	}
	pExp := NewUiTreeProbe(expTree, nil, ggui.Sz(560, 480))
	defer pExp.Close()
	szExp := pExp.Draw(ggfx.NewImage(560, 480))
	if szExp.H <= szCap.H+100 {
		t.Fatalf("expected fill+grow chart (%v) taller than capped chart (%v)", szExp.H, szCap.H)
	}

	// Empty series & uneven series + disabled onChange click safety.
	for _, node := range []*protocol.UiNode{
		{Id: "/empty", Kind: "chart", Chart: "heatmap", Text: "Empty", Change: true},
		{
			Id:     "/uneven",
			Kind:   "chart",
			Chart:  "heatmap",
			Text:   "Uneven",
			Change: false,
			Labels: []string{"C1", "C2", "C3"},
			Series: []*protocol.UiSeries{
				{Name: "A", Values: []float64{1, 2, 3}},
				{Name: "B", Values: []float64{4}},
			},
		},
	} {
		var fired int
		p := NewUiTreeProbe(node, func(_, _, _ string) { fired++ }, ggui.Sz(480, 240))
		p.Draw(ggfx.NewImage(480, 240))
		p.Click(ggui.Pt(240, 120))
		p.Frame()
		p.Close()
		if fired != 0 {
			t.Fatalf("expected 0 events for %s, got %d", node.GetId(), fired)
		}
	}
}
