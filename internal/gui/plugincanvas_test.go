package gui

import (
	"strings"
	"testing"

	"github.com/ironpark/ggfx"
	"github.com/ironpark/ggui"
	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/gui/i18n"
	"github.com/ironpark/mako/internal/protocol"
)

func TestPluginCanvasOpsSemanticsHitTestPanAndZoom(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	node := &protocol.UiNode{
		Id:           "/vec",
		Kind:         "canvas",
		Text:         "Architecture Canvas",
		CanvasWidth:  480,
		CanvasHeight: 240,
		CanvasPan:    true,
		CanvasZoom:   true,
		Change:       true,
		CanvasCommands: []*protocol.UiCanvasCommand{
			{Op: "roundRect", X: 24, Y: 24, W: 120, H: 60, R: 8, FillTone: "surfaceAlt", StrokeTone: "accent", StrokeWidth: 2, Alpha: proto.Float64(1), HitId: "box-1"},
			{Op: "circle", X: 240, Y: 60, R: 24, FillTone: "success", StrokeTone: "border", Alpha: proto.Float64(0.9), HitId: "circ-1"},
			{Op: "line", X1: 144, Y1: 54, X2: 216, Y2: 60, StrokeTone: "muted", StrokeWidth: 2, Alpha: proto.Float64(1)},
			{Op: "bezier", X1: 84, Y1: 84, Cx1: 120, Cy1: 140, Cx2: 200, Cy2: 140, X2: 240, Y2: 84, StrokeTone: "warning", StrokeWidth: 2, Alpha: proto.Float64(1)},
			{Op: "text", X: 40, Y: 48, Text: "Worker", Size: 13, Tone: "ink", Alpha: proto.Float64(1)},
			{Op: "icon", X: 110, Y: 38, Icon: "cpu", Size: 16, Tone: "danger", Alpha: proto.Float64(1)},
		},
	}

	type ev struct{ id, kind, value string }
	var evs []ev
	p := NewUiTreeProbe(node, func(id, kind, value string) {
		evs = append(evs, ev{id, kind, value})
	}, ggui.Sz(540, 320))
	defer p.Close()

	img := ggfx.NewImage(540, 320)
	p.Draw(img)

	sem := p.Semantics()
	semStr := sem.String()
	for _, want := range []string{"Architecture Canvas", "box-1", "circ-1", "Worker", "Zoom in", "Zoom out", "Reset"} {
		if !strings.Contains(semStr, want) {
			t.Fatalf("canvas semantics missing %q:\n%s", want, semStr)
		}
	}

	// Click box-1 -> emits UiChange("box-1").
	boxSem, ok := sem.Find(ggui.RoleButton, "box-1")
	if !ok {
		t.Fatalf("missing box-1 button leaf:\n%s", semStr)
	}
	beforeBox := boxSem.Rect
	center := func(r ggui.Rect) ggui.Point {
		return ggui.Pt(r.Origin.X+r.Size.W/2, r.Origin.Y+r.Size.H/2)
	}
	p.Click(center(beforeBox))
	p.Frame()
	if len(evs) != 1 || evs[0].kind != protocol.UiChange || evs[0].value != "box-1" {
		t.Fatalf("expected box-1 UiChange, got %+v", evs)
	}

	// Click empty region -> emits no event.
	p.Click(ggui.Pt(400, 220))
	p.Frame()
	if len(evs) != 1 {
		t.Fatalf("empty click emitted unexpected event: %+v", evs)
	}

	// Background pan drag (+80, +40) emits no event and translates box-1 semantics.
	p.Press(ggui.Pt(380, 220))
	p.Frame()
	p.Move(ggui.Pt(460, 260))
	p.Frame()
	p.Release(ggui.Pt(460, 260))
	p.Frame()
	if len(evs) != 1 {
		t.Fatalf("pan drag emitted unexpected event: %+v", evs)
	}
	pannedSem, ok := p.Semantics().Find(ggui.RoleButton, "box-1")
	if !ok {
		t.Fatal("missing box-1 after pan")
	}
	if pannedSem.Rect.Origin.X <= beforeBox.Origin.X+60 || pannedSem.Rect.Origin.Y <= beforeBox.Origin.Y+20 {
		t.Fatalf("expected panned box-1 origin to shift (+80,+40): before=%+v after=%+v", beforeBox, pannedSem.Rect)
	}

	// Zoom in button + Ctrl/Meta scroll + cmd+= shortcut grow bounds; Reset restores.
	zoomBtn, ok := p.Semantics().Find(ggui.RoleButton, "Zoom in")
	if !ok {
		t.Fatal("missing Zoom in button")
	}
	p.Click(center(zoomBtn.Rect))
	p.Frame()
	p.MoveWith(ggui.Pt(240, 150), ggui.Mods{Ctrl: true, Meta: true})
	p.Scroll(ggui.Pt(240, 150), ggui.Pt(0, -4))
	p.Key("cmd+=")
	p.Frame()
	p.Draw(img)

	zoomedSem, ok := p.Semantics().Find(ggui.RoleButton, "box-1")
	if !ok {
		t.Fatal("missing box-1 after zoom")
	}
	if zoomedSem.Rect.Size.W <= beforeBox.Size.W || zoomedSem.Rect.Size.H <= beforeBox.Size.H {
		t.Fatalf("expected zoomed box-1 size to grow: before=%+v after=%+v", beforeBox, zoomedSem.Rect)
	}

	resetBtn, ok := p.Semantics().Find(ggui.RoleButton, "Reset")
	if !ok {
		t.Fatal("missing Reset button")
	}
	p.Click(center(resetBtn.Rect))
	p.Frame()
	resetSem, ok := p.Semantics().Find(ggui.RoleButton, "box-1")
	if !ok {
		t.Fatal("missing box-1 after reset")
	}
	if resetSem.Rect != beforeBox {
		t.Fatalf("expected Reset to restore box-1 rect %+v, got %+v", beforeBox, resetSem.Rect)
	}
}

func TestPluginCanvasSizingFillGrowAndMaxHeight(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	growTree := &protocol.UiNode{
		Kind: "column",
		Fill: true,
		Children: []*protocol.UiNode{
			{Id: "/hdr", Kind: "text", Text: "Header"},
			{Id: "/cv", Kind: "canvas", Grow: 1, CanvasPan: true, CanvasZoom: true},
		},
	}
	pGrow := NewUiTreeProbe(growTree, nil, ggui.Sz(560, 480))
	defer pGrow.Close()
	szGrow := pGrow.Draw(ggfx.NewImage(560, 480))
	if szGrow.H < 400 {
		t.Fatalf("expected fill+grow canvas tree height >= 400, got %v", szGrow.H)
	}

	capTree := &protocol.UiNode{
		Kind: "column",
		Children: []*protocol.UiNode{
			{Id: "/cv", Kind: "canvas", MaxHeight: 140, CanvasPan: true, CanvasZoom: true},
		},
	}
	pCap := NewUiTreeProbe(capTree, nil, ggui.Sz(560, 480))
	defer pCap.Close()
	szCap := pCap.Draw(ggfx.NewImage(560, 480))
	if szCap.H > 180 {
		t.Fatalf("expected maxHeight=140 canvas tree height <= 180, got %v", szCap.H)
	}
}

func TestPluginCanvasExplicitAlphaZero(t *testing.T) {
	if got := canvasCmdAlpha(nil); got != 1.0 {
		t.Fatalf("canvasCmdAlpha(nil) = %v, want 1.0", got)
	}
	if got := canvasCmdAlpha(&protocol.UiCanvasCommand{Op: "roundRect"}); got != 1.0 {
		t.Fatalf("canvasCmdAlpha(omitted) = %v, want 1.0", got)
	}
	if got := canvasCmdAlpha(&protocol.UiCanvasCommand{Op: "roundRect", Alpha: proto.Float64(0)}); got != 0.0 {
		t.Fatalf("canvasCmdAlpha(0) = %v, want 0.0", got)
	}
	if got := canvasCmdAlpha(&protocol.UiCanvasCommand{Op: "roundRect", Alpha: proto.Float64(0.35)}); got != 0.35 {
		t.Fatalf("canvasCmdAlpha(0.35) = %v, want 0.35", got)
	}
	if got := canvasCmdAlpha(&protocol.UiCanvasCommand{Op: "roundRect", Alpha: proto.Float64(1.5)}); got != 1.0 {
		t.Fatalf("canvasCmdAlpha(1.5) = %v, want 1.0", got)
	}
	if got := canvasCmdAlpha(&protocol.UiCanvasCommand{Op: "roundRect", Alpha: proto.Float64(-0.2)}); got != 0.0 {
		t.Fatalf("canvasCmdAlpha(-0.2) = %v, want 0.0", got)
	}

	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")
	node := &protocol.UiNode{
		Id:           "/alpha0",
		Kind:         "canvas",
		CanvasWidth:  200,
		CanvasHeight: 120,
		CanvasCommands: []*protocol.UiCanvasCommand{
			{Op: "roundRect", X: 10, Y: 10, W: 80, H: 40, FillTone: "accent", Alpha: proto.Float64(0)},
		},
	}
	p := NewUiTreeProbe(node, nil, ggui.Sz(240, 160))
	defer p.Close()
	p.Draw(ggfx.NewImage(240, 160))
}

func TestPluginCanvasHitTestAndSemanticsClippedToViewport(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	i18n.Set("en")

	node := &protocol.UiNode{
		Kind: "row",
		Children: []*protocol.UiNode{
			{
				Id:           "/cv",
				Kind:         "canvas",
				CanvasWidth:  300,
				CanvasHeight: 180,
				CanvasPan:    true,
				CanvasZoom:   true,
				Change:       true,
				CanvasCommands: []*protocol.UiCanvasCommand{
					{Op: "roundRect", X: 250, Y: 20, W: 200, H: 100, FillTone: "accent", HitId: "clipped-right"},
					{Op: "roundRect", X: 500, Y: 20, W: 80, H: 60, FillTone: "success", HitId: "offscreen"},
				},
			},
			{Id: "/aside", Kind: "text", Text: "Aside"},
		},
	}

	var hits []string
	p := NewUiTreeProbe(node, func(_, kind, value string) {
		if kind == protocol.UiChange {
			hits = append(hits, value)
		}
	}, ggui.Sz(500, 220))
	defer p.Close()
	p.Draw(ggfx.NewImage(500, 220))

	sem := p.Semantics()
	if _, ok := sem.Find(ggui.RoleButton, "offscreen"); ok {
		t.Fatalf("expected completely offscreen hitId to be omitted from semantics:\n%s", sem.String())
	}
	clippedSem, ok := sem.Find(ggui.RoleButton, "clipped-right")
	if !ok {
		t.Fatalf("expected partially visible clipped-right button in semantics:\n%s", sem.String())
	}
	if clippedSem.Rect.Origin.X+clippedSem.Rect.Size.W > 300.5 {
		t.Fatalf("expected semantic rect right edge <= 300, got %+v", clippedSem.Rect)
	}

	// Press at (299, 60) inside canvas and release 2px outside at (301, 60) -> must emit 0 events.
	p.Press(ggui.Pt(299, 60))
	p.Frame()
	p.Move(ggui.Pt(301, 60))
	p.Frame()
	p.Release(ggui.Pt(301, 60))
	p.Frame()
	if len(hits) != 0 {
		t.Fatalf("expected 0 events when releasing outside canvas rect, got %v", hits)
	}

	// Direct click outside canvas rect at (305, 60) -> 0 events.
	p.Click(ggui.Pt(305, 60))
	p.Frame()
	if len(hits) != 0 {
		t.Fatalf("expected 0 events when clicking outside canvas rect, got %v", hits)
	}

	// Click visible portion inside canvas rect -> emits clipped-right.
	p.Click(ggui.Pt(280, 60))
	p.Frame()
	if len(hits) != 1 || hits[0] != "clipped-right" {
		t.Fatalf("expected visible portion click to emit clipped-right, got %v", hits)
	}
}
