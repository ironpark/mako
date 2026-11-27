package gui

import (
	"strings"
	"testing"

	"github.com/ironpark/ggfx"
	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/gui/i18n"
	"github.com/ironpark/mako/internal/protocol"
)

func TestPluginTimelineRenderClickZoomAndPan(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	var spans []*protocol.UiTimelineSpan
	for i := range 10 {
		spans = append(spans, &protocol.UiTimelineSpan{
			Id:       "sp-" + string(rune('0'+i)),
			StartMs:  float64(i * 40000),
			EndMs:    float64(i*40000 + 30000),
			Label:    "Step " + string(rune('0'+i)),
			Subtitle: "sub",
			Tone:     "accent",
			Kind:     "turn",
		})
	}
	node := &protocol.UiNode{
		Id:        "/tl",
		Kind:      "timeline",
		Text:      "Pipeline Timeline",
		MaxHeight: 240,
		Change:    true,
		TimelineLanes: []*protocol.UiTimelineLane{
			{Id: "l1", Label: "Agent Turns", Spans: spans},
			{Id: "l2", Label: "CI", Spans: []*protocol.UiTimelineSpan{{Id: "ci-1", StartMs: 5000, EndMs: 45000, Label: "CI Build", Tone: "success"}}},
		},
	}

	var clicked []string
	p := NewUiTreeProbe(node, func(_, kind, value string) {
		if kind == protocol.UiChange {
			clicked = append(clicked, value)
		}
	}, ggui.Sz(680, 280))
	defer p.Close()

	img := ggfx.NewImage(680, 280)
	p.Draw(img)
	sem := p.Semantics()
	semStr := sem.String()
	for _, want := range []string{"Pipeline Timeline", "Agent Turns", "CI", "Step 0", "CI Build"} {
		if !strings.Contains(semStr, want) {
			t.Fatalf("timeline semantics missing %q:\n%s", want, semStr)
		}
	}
	if !strings.Contains(semStr, "s") && !strings.Contains(semStr, "m") && !strings.Contains(semStr, ":") {
		t.Fatalf("expected formatted time ruler labels in semantics:\n%s", semStr)
	}

	ciSem, ok := sem.Find(ggui.RoleButton, "CI Build")
	if !ok {
		t.Fatalf("CI Build semantic button not found:\n%s", semStr)
	}
	center := func(r ggui.Rect) ggui.Point {
		return ggui.Pt(r.Origin.X+r.Size.W/2, r.Origin.Y+r.Size.H/2)
	}
	p.Click(center(ciSem.Rect))
	p.Frame()
	if len(clicked) != 1 || clicked[0] != "ci-1" {
		t.Fatalf("expected span click ci-1, got %v", clicked)
	}

	// Zoom in + horizontal scroll + drag pan should shift span rects without emitting click.
	step3Before, ok := p.Semantics().Find(ggui.RoleButton, "Step 3")
	if !ok {
		t.Fatal("Step 3 not found")
	}
	zoomBtn, ok := p.Semantics().Find(ggui.RoleButton, "Zoom in")
	if !ok {
		t.Fatal("Zoom in button not found")
	}
	p.Click(center(zoomBtn.Rect))
	p.Frame()
	p.Scroll(ggui.Pt(360, 120), ggui.Pt(-40, 0))
	p.Frame()
	p.Press(ggui.Pt(420, 120))
	p.Frame()
	p.Move(ggui.Pt(340, 120))
	p.Frame()
	p.Release(ggui.Pt(340, 120))
	p.Frame()

	if len(clicked) != 1 {
		t.Fatalf("zoom/scroll/pan unexpectedly emitted click: %v", clicked)
	}
	step3After, ok := p.Semantics().Find(ggui.RoleButton, "Step 3")
	if !ok || step3After.Rect == step3Before.Rect {
		t.Fatalf("expected Step 3 rect to move after zoom/pan: before=%+v after=%+v ok=%v", step3Before.Rect, step3After.Rect, ok)
	}
}

func TestPluginTimelineBoundarySafety(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	cases := map[string]*protocol.UiNode{
		"empty": {Id: "/empty", Kind: "timeline", MaxHeight: 180},
		"zero-and-swapped": {
			Id:        "/zero",
			Kind:      "timeline",
			MaxHeight: 200,
			Change:    true,
			TimelineLanes: []*protocol.UiTimelineLane{
				{
					Id:    "l1",
					Label: "Lane",
					Spans: []*protocol.UiTimelineSpan{
						{Id: "z", StartMs: 5000, EndMs: 5000, Label: "Zero", Tone: "accent"},
						{Id: "rev", StartMs: 9000, EndMs: 2000, Label: "Swapped", Tone: "warning"},
					},
				},
			},
		},
		"extreme-range": {
			Id:        "/ext",
			Kind:      "timeline",
			MaxHeight: 200,
			TimelineLanes: []*protocol.UiTimelineLane{
				{
					Id:    "wide",
					Label: "Epoch",
					Spans: []*protocol.UiTimelineSpan{
						{Id: "e1", StartMs: 0, EndMs: 1e13, Label: "Full", Tone: "accent"},
						{Id: "e2", StartMs: 1e12, EndMs: 1e12 + 1, Label: "1ms", Tone: "success"},
					},
				},
			},
		},
	}

	for name, tr := range cases {
		t.Run(name, func(t *testing.T) {
			p := NewUiTreeProbe(tr, nil, ggui.Sz(560, 240))
			defer p.Close()
			p.Draw(ggfx.NewImage(560, 240))
			p.Click(ggui.Pt(280, 100))
			p.Frame()
		})
	}
}

func TestPluginTimelineClippedGutterAndRulerDoNotHitOrLeakSemantics(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	center := func(r ggui.Rect) ggui.Point {
		return ggui.Pt(r.Origin.X+r.Size.W/2, r.Origin.Y+r.Size.H/2)
	}

	t.Run("horizontal_scroll_gutter_no_hit_and_partial_hit", func(t *testing.T) {
		node := &protocol.UiNode{
			Id:        "/tl-horiz",
			Kind:      "timeline",
			MaxHeight: 160,
			Change:    true,
			TimelineLanes: []*protocol.UiTimelineLane{
				{
					Id:    "l0",
					Label: "Lane 0",
					Spans: []*protocol.UiTimelineSpan{
						{Id: "sp-0", StartMs: 0, EndMs: 50000, Label: "Step 0", Tone: "accent"},
						{Id: "sp-1", StartMs: 50000, EndMs: 100000, Label: "Step 1", Tone: "success"},
					},
				},
			},
		}
		var hits []string
		p := NewUiTreeProbe(node, func(_, kind, value string) {
			if kind == protocol.UiChange {
				hits = append(hits, value)
			}
		}, ggui.Sz(600, 200))
		defer p.Close()
		img := ggfx.NewImage(600, 200)
		p.Draw(img)

		zoomBtn, ok := p.Semantics().Find(ggui.RoleButton, "Zoom in")
		if !ok {
			t.Fatal("Zoom in button not found")
		}
		p.Click(center(zoomBtn.Rect))
		p.Frame()
		p.Click(center(zoomBtn.Rect))
		p.Frame()
		p.Scroll(ggui.Pt(300, 60), ggui.Pt(-80, 0))
		p.Frame()
		p.Draw(img)

		sp0Sem, ok := p.Semantics().Find(ggui.RoleButton, "Step 0")
		if !ok {
			t.Fatalf("expected partially visible Step 0 in semantics:\n%s", p.Semantics().String())
		}
		if sp0Sem.Rect.Origin.X < 116 {
			t.Fatalf("expected clipped Step 0 semantic rect X >= 116 (trackX), got %+v", sp0Sem.Rect)
		}

		// Click inside left lane-label gutter where unclipped sp-0 extends -> 0 events.
		p.Click(ggui.Pt(40, 50))
		p.Frame()
		if len(hits) != 0 {
			t.Fatalf("clicking lane label gutter emitted unexpected span hit: %v", hits)
		}

		// Clicking visible portion of partially clipped Step 0 still emits sp-0.
		p.Click(center(sp0Sem.Rect))
		p.Frame()
		if len(hits) != 1 || hits[0] != "sp-0" {
			t.Fatalf("expected visible Step 0 click to emit sp-0, got %v", hits)
		}
	})

	t.Run("vertical_scroll_ruler_no_hit_and_partial_hit", func(t *testing.T) {
		var lanes []*protocol.UiTimelineLane
		for i := range 8 {
			lanes = append(lanes, &protocol.UiTimelineLane{
				Id:    "l-" + string(rune('0'+i)),
				Label: "Lane " + string(rune('0'+i)),
				Spans: []*protocol.UiTimelineSpan{
					{Id: "sp-" + string(rune('0'+i)), StartMs: 0, EndMs: 10000, Label: "Span " + string(rune('0'+i)), Tone: "accent"},
				},
			})
		}
		node := &protocol.UiNode{
			Id:            "/tl-vert",
			Kind:          "timeline",
			MaxHeight:     160,
			Change:        true,
			TimelineLanes: lanes,
		}
		var hits []string
		p := NewUiTreeProbe(node, func(_, kind, value string) {
			if kind == protocol.UiChange {
				hits = append(hits, value)
			}
		}, ggui.Sz(600, 200))
		defer p.Close()
		img := ggfx.NewImage(600, 200)
		p.Draw(img)

		// Scroll vertically by 16px so Lane 0's span bar straddles the 28px ruler boundary (Y in [18..48]).
		p.Scroll(ggui.Pt(250, 80), ggui.Pt(0, 1))
		p.Frame()
		p.Draw(img)

		sp0Sem, ok := p.Semantics().Find(ggui.RoleButton, "Span 0")
		if !ok {
			t.Fatalf("expected partially visible Span 0 in semantics:\n%s", p.Semantics().String())
		}
		if sp0Sem.Rect.Origin.Y < timelineRulerH {
			t.Fatalf("expected clipped Span 0 semantic rect Y >= %v, got %+v", timelineRulerH, sp0Sem.Rect)
		}

		// Click inside the 28px time ruler header -> 0 events.
		p.Click(ggui.Pt(250, 22))
		p.Frame()
		if len(hits) != 0 {
			t.Fatalf("clicking time ruler header emitted unexpected span hit: %v", hits)
		}

		// Clicking visible portion of partially clipped Span 0 below ruler still emits sp-0.
		p.Click(center(sp0Sem.Rect))
		p.Frame()
		if len(hits) != 1 || hits[0] != "sp-0" {
			t.Fatalf("expected visible Span 0 click to emit sp-0, got %v", hits)
		}
	})
}
