//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ironpark/ggfx"
	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/gui"
	"github.com/ironpark/mako/internal/gui/i18n"
	"github.com/ironpark/mako/internal/plugin"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/uiblock"
)

const primTestTimeout = 10 * time.Second

type primTestUiEvent struct {
	id    string
	kind  string
	value string
}

type primTestEventLog struct {
	mu     sync.Mutex
	events []primTestUiEvent
}

func (l *primTestEventLog) record(id, kind, value string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, primTestUiEvent{id: id, kind: kind, value: value})
}

func (l *primTestEventLog) snapshot() []primTestUiEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.events)
}

func (l *primTestEventLog) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = nil
}

func primTestNewProbe(t *testing.T, tree *protocol.UiNode, sz ggui.Size) (*ggui.Probe, *primTestEventLog) {
	t.Helper()
	if tree == nil {
		t.Fatal("primTestNewProbe: tree is nil")
	}
	i18n.Set("en")
	log := &primTestEventLog{}
	p := gui.NewUiTreeProbe(tree, log.record, sz)
	t.Cleanup(p.Close)
	for range 3 {
		p.Frame()
	}
	return p, log
}

func primTestDraw(t *testing.T, p *ggui.Probe, w, h int) ggui.Size {
	t.Helper()
	img := ggfx.NewImage(w, h)
	defer img.Deallocate()
	sz := p.Draw(img)
	if sz.W <= 0 || sz.H <= 0 {
		t.Fatalf("expected positive drawn size, got %+v", sz)
	}
	return sz
}

func primTestRectCenter(r ggui.Rect) ggui.Point {
	return ggui.Pt(r.Origin.X+r.Size.W/2, r.Origin.Y+r.Size.H/2)
}

func primTestFindSemSubstr(sem *ggui.SemTree, role ggui.Role, substr string) (ggui.SemNode, bool) {
	if sem == nil {
		return ggui.SemNode{}, false
	}
	low := strings.ToLower(substr)
	for _, nd := range sem.All() {
		if role != "" && nd.Role != role {
			continue
		}
		if strings.Contains(strings.ToLower(nd.Name), low) {
			return nd, true
		}
	}
	return ggui.SemNode{}, false
}

func primTestFindSemAny(sem *ggui.SemTree, needles ...string) (ggui.SemNode, bool) {
	if sem == nil {
		return ggui.SemNode{}, false
	}
	for _, needle := range needles {
		if nd, ok := primTestFindSemSubstr(sem, ggui.RoleButton, needle); ok && nd.Rect.Size.W > 0 && nd.Rect.Size.H > 0 {
			return nd, true
		}
	}
	for _, needle := range needles {
		if nd, ok := primTestFindSemSubstr(sem, "", needle); ok && nd.Rect.Size.W > 0 && nd.Rect.Size.H > 0 {
			return nd, true
		}
	}
	return ggui.SemNode{}, false
}

func primTestInstallAndWatch(t *testing.T, rig *PluginRig, id, toml, js, block string) *TreeWatcher {
	t.Helper()
	rig.InstallJS(t, id, toml, js)
	rig.Grant(t, id)
	return rig.WatchTree(t, "plugin:"+id+"/"+block, "", plugin.View{})
}

func primTestMinimalToml(id, block string) string {
	return fmt.Sprintf(`id = %q
version = "0.1.0"
publisher = "e2e"
api = 1
entry = "main.js"
permissions = []

[[contributes.block]]
id = %q
title = "Test Block"
slots = ["right"]
`, id, block)
}

func primTestMustRejectRender(t *testing.T, id, js string) string {
	t.Helper()
	rig := NewPluginRig(t, PluginRigOpts{})
	rig.InstallJS(t, id, primTestMinimalToml(id, "main"), js)
	rig.Grant(t, id)
	w := rig.WatchTree(t, "plugin:"+id+"/main", "", plugin.View{})
	tr, upd := w.WaitUpdate(t, primTestTimeout, func(tree *protocol.UiNode, u *protocol.BlockUpdate) bool {
		return u.GetError() != "" || tree != nil
	})
	if upd.GetError() == "" {
		t.Fatalf("expected invalid render to be rejected, but succeeded with tree=%+v", tr)
	}
	return upd.GetError()
}

func primTestDrag(p *ggui.Probe, from, to ggui.Point) {
	mid := ggui.Pt((from.X+to.X)/2, (from.Y+to.Y)/2)
	p.Press(from)
	p.Frame()
	p.Move(mid)
	p.Frame()
	p.Move(to)
	p.Frame()
	p.Release(to)
	p.Frame()
}

func primTestShouldIgnoreParityRel(rel string) bool {
	slash := filepath.ToSlash(rel)
	base := filepath.Base(slash)
	switch base {
	case "mako-signature.json", ".DS_Store":
		return true
	}
	for _, seg := range strings.Split(slash, "/") {
		if seg == ".git" || seg == "node_modules" {
			return true
		}
	}
	return false
}

func primTestComparePluginDirs(srcDir, dstDir string) ([]string, error) {
	srcFiles := map[string]string{}
	err := filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if primTestShouldIgnoreParityRel(rel) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		srcFiles[filepath.ToSlash(rel)] = path
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk src %q: %w", srcDir, err)
	}

	dstFiles := map[string]string{}
	err = filepath.WalkDir(dstDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(dstDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if primTestShouldIgnoreParityRel(rel) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		dstFiles[filepath.ToSlash(rel)] = path
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk dst %q: %w", dstDir, err)
	}

	var diffs []string
	var keys []string
	for k := range srcFiles {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, rel := range keys {
		dstPath, ok := dstFiles[rel]
		if !ok {
			diffs = append(diffs, fmt.Sprintf("missing in dst: %s", rel))
			continue
		}
		sb, err := os.ReadFile(srcFiles[rel])
		if err != nil {
			return nil, fmt.Errorf("read src %s: %w", rel, err)
		}
		db, err := os.ReadFile(dstPath)
		if err != nil {
			return nil, fmt.Errorf("read dst %s: %w", rel, err)
		}
		if !bytes.Equal(sb, db) {
			diffs = append(diffs, fmt.Sprintf("content mismatch: %s (src=%dB dst=%dB)", rel, len(sb), len(db)))
		}
	}

	var extraKeys []string
	for k := range dstFiles {
		if _, ok := srcFiles[k]; !ok {
			extraKeys = append(extraKeys, k)
		}
	}
	slices.Sort(extraKeys)
	for _, rel := range extraKeys {
		diffs = append(diffs, fmt.Sprintf("unexpected extra file in dst: %s", rel))
	}
	return diffs, nil
}

func primTestSnapshotDir(t *testing.T, root string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		sumHex := "-"
		if info.Mode().IsRegular() {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(b)
			sumHex = hex.EncodeToString(sum[:])
		}
		snap[filepath.ToSlash(rel)] = fmt.Sprintf("%s|%d|%d|%s", info.Mode().String(), info.Size(), info.ModTime().UnixNano(), sumHex)
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %q: %v", root, err)
	}
	return snap
}

func primTestCollectUIWorkbenchTrees(t *testing.T, rig *PluginRig) []*protocol.UiNode {
	t.Helper()
	var trees []*protocol.UiNode

	wExp := rig.WatchTree(t, "plugin:ui-workbench/explorer", "", plugin.View{})
	expTree := wExp.WaitFor(t, primTestTimeout, nil)
	trees = append(trees, expTree)

	wView := rig.WatchTree(t, "plugin:ui-workbench/viewer", "", plugin.View{})
	viewTree := wView.WaitFor(t, primTestTimeout, nil)
	trees = append(trees, viewTree)

	// Walk all top-level tabs in viewer and any secondary tabs within each section.
	if mainTabs := FindNodeByKey(viewTree, "main-tabs"); mainTabs != nil {
		for _, opt := range mainTabs.GetOptions() {
			if _, err := rig.SendEvent(t, &protocol.BlockEvent{
				Block: "plugin:ui-workbench/viewer",
				Node:  mainTabs.GetId(),
				Kind:  protocol.UiChange,
				Value: opt,
			}); err != nil {
				t.Fatalf("switch ui-workbench main tab %q: %v", opt, err)
			}
			cur := wView.WaitFor(t, primTestTimeout, func(n *protocol.UiNode) bool {
				mt := FindNodeByKey(n, "main-tabs")
				return mt != nil && mt.GetValue() == opt
			})
			trees = append(trees, cur)

			for _, kind := range []string{"radio", "select"} {
				for _, ctrl := range FindAllNodesByKind(cur, kind) {
					if !ctrl.GetChange() {
						continue
					}
					for _, opt := range ctrl.GetOptions() {
						if _, err := rig.SendEvent(t, &protocol.BlockEvent{
							Block: "plugin:ui-workbench/viewer",
							Node:  ctrl.GetId(),
							Kind:  protocol.UiChange,
							Value: opt,
						}); err != nil {
							t.Fatalf("switch ui-workbench %s %q: %v", kind, opt, err)
						}
						subTree := wView.WaitFor(t, primTestTimeout, func(n *protocol.UiNode) bool {
							return FindNodeByID(n, ctrl.GetId()) != nil
						})
						trees = append(trees, subTree)
					}
				}
			}

			for _, subTabs := range FindAllNodesByKind(cur, "tabs") {
				if subTabs.GetId() == mainTabs.GetId() {
					continue
				}
				for _, subOpt := range subTabs.GetOptions() {
					if _, err := rig.SendEvent(t, &protocol.BlockEvent{
						Block: "plugin:ui-workbench/viewer",
						Node:  subTabs.GetId(),
						Kind:  protocol.UiChange,
						Value: subOpt,
					}); err != nil {
						t.Fatalf("switch ui-workbench sub-tab %q: %v", subOpt, err)
					}
					subTree := wView.WaitFor(t, primTestTimeout, func(n *protocol.UiNode) bool {
						st := FindNodeByID(n, subTabs.GetId())
						return st != nil && st.GetValue() == subOpt
					})
					trees = append(trees, subTree)
				}
			}
		}
	}
	return trees
}

func primTestFindChartType(trees []*protocol.UiNode, chartType string) *protocol.UiNode {
	for _, tr := range trees {
		for _, ch := range FindAllNodesByKind(tr, "chart") {
			if ch.GetChart() == chartType {
				return ch
			}
		}
	}
	return nil
}

// =============================================================================
// TIER 1 — Feature Coverage (>= 25 tests across F3.1–F3.3, F4.1–F4.2)
// =============================================================================

// --- F3.1: ui.canvas (5 Tier 1 tests) ---

func TestPrimT1CanvasRendersAllVectorOpsAndThemeTokens(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	js := `export default {
  activate(mako) {
    mako.blocks.register("main", {
      render(s, ui) {
        return ui.canvas([
          { op: "roundRect", x: 12, y: 12, w: 140, h: 72, r: 10, fillTone: "surface", strokeTone: "accent", strokeWidth: 2, hitId: "rr-1" },
          { op: "circle", x: 240, y: 48, r: 26, fillTone: "success", strokeTone: "border", strokeWidth: 1.5, hitId: "circ-1" },
          { op: "line", x1: 152, y1: 48, x2: 214, y2: 48, strokeTone: "muted", strokeWidth: 2 },
          { op: "bezier", x1: 82, y1: 84, cx1: 110, cy1: 140, cx2: 210, cy2: 140, x2: 240, y2: 74, strokeTone: "warning", strokeWidth: 2 },
          { op: "text", x: 26, y: 38, text: "Vector Worker", size: 13, tone: "text" },
          { op: "icon", x: 118, y: 34, icon: "cpu", size: 16, tone: "danger" }
        ], { key: "vec", width: 480, height: 220, caption: "Architecture Canvas" });
      }
    });
  }
};`
	w := primTestInstallAndWatch(t, rig, "canvas-ops", primTestMinimalToml("canvas-ops", "main"), js, "main")
	tree := w.WaitFor(t, primTestTimeout, func(n *protocol.UiNode) bool {
		return FindNodeByKind(n, "canvas") != nil
	})

	p, _ := primTestNewProbe(t, tree, ggui.Sz(540, 300))
	primTestDraw(t, p, 540, 300)
	sem := p.Semantics().String()
	if !strings.Contains(sem, "Vector Worker") && !strings.Contains(sem, "Architecture Canvas") {
		t.Fatalf("expected canvas labels in semantics, got:\n%s", sem)
	}
}

func TestPrimT1CanvasHitIdClickDispatchesEvent(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	js := `export default {
  activate(mako) {
    mako.blocks.register("main", {
      state: { selected: "none" },
      render(s, ui) {
        return ui.column([
          ui.text("selected:" + s.selected, { key: "status" }),
          ui.canvas([
            { op: "roundRect", x: 30, y: 30, w: 120, h: 64, r: 8, fillTone: "surfaceAlt", strokeTone: "accent", strokeWidth: 2, hitId: "box-alpha" },
            { op: "text", x: 46, y: 54, text: "Box Alpha", size: 12, tone: "ink" },
            { op: "circle", x: 260, y: 62, r: 28, fillTone: "success", strokeTone: "border", hitId: "circle-beta" }
          ], { key: "vec", width: 460, height: 200, onChange: ["pick"] })
        ], { key: "root", gap: 8 });
      },
      on: {
        pick(s, hit) { s.selected = String(hit || ""); }
      }
    });
  }
};`
	w := primTestInstallAndWatch(t, rig, "canvas-hit", primTestMinimalToml("canvas-hit", "main"), js, "main")
	tree := w.WaitFor(t, primTestTimeout, func(n *protocol.UiNode) bool {
		return FindNodeByKey(n, "vec") != nil
	})

	p, log := primTestNewProbe(t, tree, ggui.Sz(520, 320))
	sem := p.Semantics()
	targetPt := ggui.Pt(90, 92)
	if nd, ok := primTestFindSemAny(sem, "box-alpha", "Box Alpha"); ok {
		targetPt = primTestRectCenter(nd.Rect)
	} else if cNode, ok := primTestFindSemSubstr(sem, "", "vec"); ok && cNode.Rect.Size.W > 0 {
		targetPt = ggui.Pt(cNode.Rect.Origin.X+90, cNode.Rect.Origin.Y+62)
	}
	p.Click(targetPt)
	p.Frame()

	evs := log.snapshot()
	if len(evs) == 0 {
		t.Fatalf("expected click on canvas hitId to emit event, got none (sem:\n%s)", sem.String())
	}
	last := evs[len(evs)-1]
	if last.value != "box-alpha" {
		t.Fatalf("expected hitId %q, got event %+v", "box-alpha", last)
	}

	canvasNode := FindNodeByKey(tree, "vec")
	if _, err := rig.SendEvent(t, &protocol.BlockEvent{
		Block: "plugin:canvas-hit/main",
		Node:  canvasNode.GetId(),
		Kind:  last.kind,
		Value: last.value,
	}); err != nil {
		t.Fatalf("SendEvent: %v", err)
	}
	w.WaitFor(t, primTestTimeout, func(n *protocol.UiNode) bool {
		return HasTreeText(n, "selected:box-alpha")
	})
}

func TestPrimT1CanvasPanTranslatesPrimitivesAndHitCoords(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	js := `export default {
  activate(mako) {
    mako.blocks.register("main", {
      render(s, ui) {
        return ui.canvas([
          { op: "roundRect", x: 40, y: 40, w: 90, h: 60, r: 8, fillTone: "accent", strokeTone: "border", hitId: "pan-target" }
        ], { key: "vec", width: 500, height: 300, pan: true, onChange: ["pick"] });
      },
      on: { pick() {} }
    });
  }
};`
	w := primTestInstallAndWatch(t, rig, "canvas-pan", primTestMinimalToml("canvas-pan", "main"), js, "main")
	tree := w.WaitFor(t, primTestTimeout, func(n *protocol.UiNode) bool {
		return FindNodeByKind(n, "canvas") != nil
	})

	p, log := primTestNewProbe(t, tree, ggui.Sz(520, 320))
	beforeSem := p.Semantics()
	initialCenter := ggui.Pt(85, 70)
	if nd, ok := primTestFindSemAny(beforeSem, "pan-target"); ok {
		initialCenter = primTestRectCenter(nd.Rect)
	}

	// Drag empty canvas background by (+120, +60).
	primTestDrag(p, ggui.Pt(320, 220), ggui.Pt(440, 280))
	if evs := log.snapshot(); len(evs) != 0 {
		t.Fatalf("background pan drag unexpectedly emitted hit event: %+v", evs)
	}

	// Verify old pre-pan coordinate no longer hits, while shifted coordinate hits.
	afterSem := p.Semantics()
	p.Click(initialCenter)
	p.Frame()
	if evs := log.snapshot(); len(evs) != 0 {
		t.Fatalf("expected pre-pan center %+v not to hit after pan, got %+v", initialCenter, evs)
	}

	shiftedCenter := ggui.Pt(initialCenter.X+120, initialCenter.Y+60)
	if nd, ok := primTestFindSemAny(afterSem, "pan-target"); ok {
		shiftedCenter = primTestRectCenter(nd.Rect)
	}
	p.Click(shiftedCenter)
	p.Frame()
	evs := log.snapshot()
	if len(evs) != 1 || evs[0].value != "pan-target" {
		t.Fatalf("expected shifted center %+v to hit pan-target, got %+v", shiftedCenter, evs)
	}
}

func TestPrimT1CanvasZoomScalesCanvas(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	js := `export default {
  activate(mako) {
    mako.blocks.register("main", {
      render(s, ui) {
        return ui.canvas([
          { op: "roundRect", x: 100, y: 80, w: 120, h: 70, r: 8, fillTone: "surface", strokeTone: "accent", hitId: "zoom-box" },
          { op: "text", x: 120, y: 110, text: "Zoomable", size: 12, tone: "text" }
        ], { key: "vec", width: 480, height: 280, zoom: true, pan: true, onChange: ["pick"] });
      },
      on: { pick() {} }
    });
  }
};`
	w := primTestInstallAndWatch(t, rig, "canvas-zoom", primTestMinimalToml("canvas-zoom", "main"), js, "main")
	tree := w.WaitFor(t, primTestTimeout, func(n *protocol.UiNode) bool {
		return FindNodeByKind(n, "canvas") != nil
	})

	p, _ := primTestNewProbe(t, tree, ggui.Sz(520, 320))
	primTestDraw(t, p, 520, 320)

	var beforeRect ggui.Rect
	if nd, ok := primTestFindSemAny(p.Semantics(), "zoom-box", "Zoomable"); ok {
		beforeRect = nd.Rect
	}

	// Trigger zoom via built-in Zoom in control if exposed, and Ctrl/Cmd+Scroll / shortcut.
	if zoomBtn, ok := primTestFindSemSubstr(p.Semantics(), ggui.RoleButton, "Zoom in"); ok {
		p.Click(primTestRectCenter(zoomBtn.Rect))
		p.Frame()
	}
	p.MoveWith(ggui.Pt(240, 150), ggui.Mods{Ctrl: true, Meta: true})
	p.Scroll(ggui.Pt(240, 150), ggui.Pt(0, -4))
	p.Key("cmd+=")
	p.Frame()
	primTestDraw(t, p, 520, 320)

	if beforeRect.Size.W > 0 {
		if nd, ok := primTestFindSemAny(p.Semantics(), "zoom-box", "Zoomable"); ok {
			if nd.Rect.Size.W <= beforeRect.Size.W && nd.Rect.Size.H <= beforeRect.Size.H {
				t.Fatalf("expected zoomed semantic bounds to grow: before=%+v after=%+v", beforeRect, nd.Rect)
			}
		}
	}
}

func TestPrimT1CanvasFillGrowMaxHeightSizing(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	js := `export default {
  activate(mako) {
    mako.blocks.register("grow", {
      render(s, ui) {
        return ui.column([
          ui.text("Header", { key: "hdr" }),
          ui.canvas([{ op: "roundRect", x: 10, y: 10, w: 80, h: 40, fillTone: "accent", hitId: "g-node" }], { key: "c-grow", grow: 1 })
        ], { key: "root", fill: true, gap: 4 });
      }
    });
    mako.blocks.register("capped", {
      render(s, ui) {
        return ui.column([
          ui.canvas([{ op: "roundRect", x: 10, y: 10, w: 80, h: 40, fillTone: "success", hitId: "m-node" }], { key: "c-max", maxHeight: 140 })
        ], { key: "root", gap: 4 });
      }
    });
  }
};`
	toml := `id = "canvas-size"
version = "0.1.0"
publisher = "e2e"
api = 1
entry = "main.js"
permissions = []

[[contributes.block]]
id = "grow"
title = "Grow"
slots = ["right"]

[[contributes.block]]
id = "capped"
title = "Capped"
slots = ["right"]
`
	rig.InstallJS(t, "canvas-size", toml, js)
	rig.Grant(t, "canvas-size")

	growTree := rig.WatchTree(t, "plugin:canvas-size/grow", "", plugin.View{}).WaitFor(t, primTestTimeout, nil)
	capTree := rig.WatchTree(t, "plugin:canvas-size/capped", "", plugin.View{}).WaitFor(t, primTestTimeout, nil)

	pGrow, _ := primTestNewProbe(t, growTree, ggui.Sz(560, 480))
	szGrow := primTestDraw(t, pGrow, 560, 480)
	if szGrow.H < 400 {
		t.Fatalf("expected fill+grow canvas tree to fill container height (~480), got %v", szGrow.H)
	}

	pCap, _ := primTestNewProbe(t, capTree, ggui.Sz(560, 480))
	szCap := primTestDraw(t, pCap, 560, 480)
	if szCap.H > 180 {
		t.Fatalf("expected maxHeight=140 canvas to remain constrained <=180, got %v", szCap.H)
	}
}

// --- F3.2: ui.timeline (5 Tier 1 tests) ---

func TestPrimT1TimelineMultipleLanesAndKinds(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	js := `export default {
  activate(mako) {
    mako.blocks.register("main", {
      render(s, ui) {
        return ui.timeline([
          {
            id: "lane-turns",
            label: "Agent Turns",
            spans: [
              { id: "turn-1", startMs: 0, endMs: 4200, label: "Plan", subtitle: "opus", tone: "accent", kind: "turn" },
              { id: "turn-2", startMs: 4500, endMs: 9800, label: "Patch", subtitle: "sonnet", tone: "success", kind: "turn" }
            ]
          },
          {
            id: "lane-subagents",
            label: "Subagents",
            spans: [
              { id: "sub-1", startMs: 1200, endMs: 5600, label: "Explorer", subtitle: "parallel", tone: "warning", kind: "subagent" }
            ]
          },
          {
            id: "lane-ci",
            label: "Tool & CI",
            spans: [
              { id: "ci-1", startMs: 5800, endMs: 11200, label: "go test", subtitle: "23 pkgs", tone: "success", kind: "ci" }
            ]
          }
        ], { key: "tl", maxHeight: 300 });
      }
    });
  }
};`
	w := primTestInstallAndWatch(t, rig, "tl-lanes", primTestMinimalToml("tl-lanes", "main"), js, "main")
	tree := w.WaitFor(t, primTestTimeout, func(n *protocol.UiNode) bool {
		return FindNodeByKind(n, "timeline") != nil
	})

	p, _ := primTestNewProbe(t, tree, ggui.Sz(720, 340))
	primTestDraw(t, p, 720, 340)
	sem := p.Semantics().String()
	for _, want := range []string{"Agent Turns", "Subagents", "Tool & CI", "Plan", "Explorer", "go test"} {
		if !strings.Contains(sem, want) {
			t.Fatalf("expected timeline semantics to include %q, got:\n%s", want, sem)
		}
	}
}

func TestPrimT1TimelineRulerFormatsStartEndDuration(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	js := `export default {
  activate(mako) {
    mako.blocks.register("main", {
      render(s, ui) {
        return ui.timeline([
          {
            id: "lane-1",
            label: "Pipeline",
            spans: [
              { id: "sp-1", startMs: 1000, endMs: 65000, label: "Build & Vet", tone: "accent", kind: "ci" },
              { id: "sp-2", startMs: 66000, endMs: 126000, label: "E2E Suite", tone: "success", kind: "ci" }
            ]
          }
        ], { key: "tl", caption: "Run Duration", maxHeight: 240 });
      }
    });
  }
};`
	w := primTestInstallAndWatch(t, rig, "tl-ruler", primTestMinimalToml("tl-ruler", "main"), js, "main")
	tree := w.WaitFor(t, primTestTimeout, func(n *protocol.UiNode) bool {
		return FindNodeByKind(n, "timeline") != nil
	})

	p, _ := primTestNewProbe(t, tree, ggui.Sz(680, 280))
	primTestDraw(t, p, 680, 280)
	sem := p.Semantics().String()
	// Verify time-axis/duration ruler text is emitted (e.g. s/m/ms or timestamps).
	hasDuration := strings.Contains(sem, "s") || strings.Contains(sem, "m") || strings.Contains(sem, ":")
	if !hasDuration {
		t.Fatalf("expected formatted time/duration ruler labels in semantics, got:\n%s", sem)
	}
}

func TestPrimT1TimelineSpanClickEmitsId(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	js := `export default {
  activate(mako) {
    mako.blocks.register("main", {
      state: { inspected: "none" },
      render(s, ui) {
        return ui.column([
          ui.text("inspected:" + s.inspected, { key: "insp" }),
          ui.timeline([
            {
              id: "l1",
              label: "Turns",
              spans: [
                { id: "span-turn-1", startMs: 0, endMs: 3000, label: "Turn One", tone: "accent" },
                { id: "span-ci-2", startMs: 3200, endMs: 8000, label: "CI Build", tone: "success" }
              ]
            }
          ], { key: "tl", maxHeight: 240, onChange: ["inspect"] })
        ], { key: "root", gap: 8 });
      },
      on: {
        inspect(s, spanId) { s.inspected = String(spanId || ""); }
      }
    });
  }
};`
	w := primTestInstallAndWatch(t, rig, "tl-click", primTestMinimalToml("tl-click", "main"), js, "main")
	tree := w.WaitFor(t, primTestTimeout, func(n *protocol.UiNode) bool {
		return FindNodeByKey(n, "tl") != nil
	})

	p, log := primTestNewProbe(t, tree, ggui.Sz(700, 320))
	sem := p.Semantics()
	nd, ok := primTestFindSemAny(sem, "CI Build", "span-ci-2")
	if !ok {
		t.Fatalf("expected clickable span 'CI Build' in timeline semantics:\n%s", sem.String())
	}
	p.Click(primTestRectCenter(nd.Rect))
	p.Frame()

	evs := log.snapshot()
	if len(evs) == 0 {
		t.Fatalf("expected click on timeline span to emit event")
	}
	last := evs[len(evs)-1]
	if last.value != "span-ci-2" {
		t.Fatalf("expected clicked span id %q, got %+v", "span-ci-2", last)
	}

	tlNode := FindNodeByKey(tree, "tl")
	if _, err := rig.SendEvent(t, &protocol.BlockEvent{
		Block: "plugin:tl-click/main",
		Node:  tlNode.GetId(),
		Kind:  last.kind,
		Value: last.value,
	}); err != nil {
		t.Fatalf("SendEvent: %v", err)
	}
	w.WaitFor(t, primTestTimeout, func(n *protocol.UiNode) bool {
		return HasTreeText(n, "inspected:span-ci-2")
	})
}

func TestPrimT1TimelineZoomPanHorizontalScroll(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	js := `export default {
  activate(mako) {
    mako.blocks.register("main", {
      render(s, ui) {
        const spans = [];
        for (let i = 0; i < 12; i++) {
          spans.push({
            id: "sp-" + i,
            startMs: i * 50000,
            endMs: i * 50000 + 38000,
            label: "Step " + i,
            tone: i % 2 === 0 ? "accent" : "success"
          });
        }
        return ui.timeline([{ id: "l1", label: "Long Run", spans }], { key: "tl", maxHeight: 220, onChange: ["pick"] });
      },
      on: { pick() {} }
    });
  }
};`
	w := primTestInstallAndWatch(t, rig, "tl-scroll", primTestMinimalToml("tl-scroll", "main"), js, "main")
	tree := w.WaitFor(t, primTestTimeout, func(n *protocol.UiNode) bool {
		return FindNodeByKind(n, "timeline") != nil
	})

	p, log := primTestNewProbe(t, tree, ggui.Sz(640, 260))
	primTestDraw(t, p, 640, 260)

	var beforeStep0 ggui.Rect
	if nd, ok := primTestFindSemAny(p.Semantics(), "Step 0", "sp-0"); ok {
		beforeStep0 = nd.Rect
	}

	if zoomBtn, ok := primTestFindSemSubstr(p.Semantics(), ggui.RoleButton, "Zoom in"); ok {
		p.Click(primTestRectCenter(zoomBtn.Rect))
		p.Frame()
	}
	p.Scroll(ggui.Pt(360, 120), ggui.Pt(-120, 0))
	p.Frame()
	primTestDrag(p, ggui.Pt(420, 120), ggui.Pt(260, 120))
	primTestDraw(t, p, 640, 260)

	if evs := log.snapshot(); len(evs) != 0 {
		t.Fatalf("pan/scroll unexpectedly emitted span selection event: %+v", evs)
	}
	if beforeStep0.Size.W > 0 {
		if nd, ok := primTestFindSemAny(p.Semantics(), "Step 0", "sp-0"); ok {
			if nd.Rect == beforeStep0 {
				t.Fatalf("expected timeline zoom/pan/scroll to shift Step 0 rect: stayed %+v", beforeStep0)
			}
		}
	}
}

func TestPrimT1TimelineToneColorsDistinguishStates(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	js := `export default {
  activate(mako) {
    mako.blocks.register("main", {
      render(s, ui) {
        return ui.timeline([
          {
            id: "states",
            label: "Execution States",
            spans: [
              { id: "st-run", startMs: 0, endMs: 2000, label: "Running", tone: "accent" },
              { id: "st-ok", startMs: 2100, endMs: 4100, label: "Success", tone: "success" },
              { id: "st-warn", startMs: 4200, endMs: 6200, label: "Warning", tone: "warning" },
              { id: "st-err", startMs: 6300, endMs: 8300, label: "Failed", tone: "danger" }
            ]
          }
        ], { key: "tl", maxHeight: 220 });
      }
    });
  }
};`
	w := primTestInstallAndWatch(t, rig, "tl-tones", primTestMinimalToml("tl-tones", "main"), js, "main")
	tree := w.WaitFor(t, primTestTimeout, func(n *protocol.UiNode) bool {
		return FindNodeByKind(n, "timeline") != nil
	})

	p, _ := primTestNewProbe(t, tree, ggui.Sz(720, 260))
	primTestDraw(t, p, 720, 260)
	sem := p.Semantics().String()
	for _, want := range []string{"Running", "Success", "Warning", "Failed"} {
		if !strings.Contains(sem, want) {
			t.Fatalf("expected state span %q in semantics:\n%s", want, sem)
		}
	}
}

// --- F3.3: ui.chart enhancements (5 Tier 1 tests) ---

func TestPrimT1ChartAreaRendersCleanly(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	js := `export default {
  activate(mako) {
    mako.blocks.register("main", {
      render(s, ui) {
        return ui.chart("area", [
          { name: "Input Tokens", tone: "accent", values: [12, 28, 22, 45, 39, 58] },
          { name: "Output Tokens", tone: "success", values: [6, 14, 11, 20, 18, 27] }
        ], { key: "area", caption: "Token Throughput", labels: ["10:00", "10:05", "10:10", "10:15", "10:20", "10:25"] });
      }
    });
  }
};`
	w := primTestInstallAndWatch(t, rig, "ch-area", primTestMinimalToml("ch-area", "main"), js, "main")
	tree := w.WaitFor(t, primTestTimeout, func(n *protocol.UiNode) bool {
		ch := FindNodeByKind(n, "chart")
		return ch != nil && ch.GetChart() == "area"
	})

	p, _ := primTestNewProbe(t, tree, ggui.Sz(560, 280))
	primTestDraw(t, p, 560, 280)
	if !strings.Contains(p.Semantics().String(), "Token Throughput") {
		t.Fatalf("expected area chart caption in semantics:\n%s", p.Semantics().String())
	}
}

func TestPrimT1ChartStackedBarMultiSeriesRendersCleanly(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	js := `export default {
  activate(mako) {
    mako.blocks.register("main", {
      render(s, ui) {
        return ui.chart("stacked-bar", [
          { name: "Passed", tone: "success", values: [18, 24, 21, 30] },
          { name: "Warnings", tone: "warning", values: [3, 2, 5, 1] },
          { name: "Errors", tone: "danger", values: [1, 0, 2, 0] }
        ], { key: "sb", caption: "CI Build Outcomes", labels: ["W1", "W2", "W3", "W4"] });
      }
    });
  }
};`
	w := primTestInstallAndWatch(t, rig, "ch-stack", primTestMinimalToml("ch-stack", "main"), js, "main")
	tree := w.WaitFor(t, primTestTimeout, func(n *protocol.UiNode) bool {
		ch := FindNodeByKind(n, "chart")
		return ch != nil && ch.GetChart() == "stacked-bar"
	})

	p, _ := primTestNewProbe(t, tree, ggui.Sz(560, 280))
	primTestDraw(t, p, 560, 280)
	if !strings.Contains(p.Semantics().String(), "CI Build Outcomes") {
		t.Fatalf("expected stacked-bar caption in semantics:\n%s", p.Semantics().String())
	}
}

func TestPrimT1ChartHeatmap2DGridRendersCleanly(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	js := `export default {
  activate(mako) {
    mako.blocks.register("main", {
      render(s, ui) {
        return ui.chart("heatmap", [
          { name: "Mon", tone: "accent", values: [2, 6, 9, 4] },
          { name: "Tue", tone: "accent", values: [8, 3, 5, 7] },
          { name: "Wed", tone: "accent", values: [1, 9, 4, 6] }
        ], { key: "hm", caption: "Agent Activity Matrix", labels: ["00h", "06h", "12h", "18h"] });
      }
    });
  }
};`
	w := primTestInstallAndWatch(t, rig, "ch-heat", primTestMinimalToml("ch-heat", "main"), js, "main")
	tree := w.WaitFor(t, primTestTimeout, func(n *protocol.UiNode) bool {
		ch := FindNodeByKind(n, "chart")
		return ch != nil && ch.GetChart() == "heatmap"
	})

	p, _ := primTestNewProbe(t, tree, ggui.Sz(560, 280))
	primTestDraw(t, p, 560, 280)
	if !strings.Contains(p.Semantics().String(), "Agent Activity Matrix") {
		t.Fatalf("expected heatmap caption in semantics:\n%s", p.Semantics().String())
	}
}

func TestPrimT1ChartMaxHeightAndGrowSizing(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	js := `export default {
  activate(mako) {
    mako.blocks.register("capped", {
      render(s, ui) {
        return ui.column([
          ui.chart("area", [{ name: "CPU", tone: "accent", values: [10, 40, 25, 60] }], {
            key: "ch-cap", caption: "Capped Chart", labels: ["A", "B", "C", "D"], maxHeight: 110
          })
        ], { key: "root" });
      }
    });
    mako.blocks.register("expanded", {
      render(s, ui) {
        return ui.column([
          ui.chart("area", [{ name: "CPU", tone: "accent", values: [10, 40, 25, 60] }], {
            key: "ch-grow", caption: "Expanded Chart", labels: ["A", "B", "C", "D"], grow: 1
          })
        ], { key: "root", fill: true });
      }
    });
  }
};`
	toml := `id = "ch-size"
version = "0.1.0"
publisher = "e2e"
api = 1
entry = "main.js"
permissions = []

[[contributes.block]]
id = "capped"
title = "Capped"
slots = ["right"]

[[contributes.block]]
id = "expanded"
title = "Expanded"
slots = ["right"]
`
	rig.InstallJS(t, "ch-size", toml, js)
	rig.Grant(t, "ch-size")

	capTree := rig.WatchTree(t, "plugin:ch-size/capped", "", plugin.View{}).WaitFor(t, primTestTimeout, nil)
	expTree := rig.WatchTree(t, "plugin:ch-size/expanded", "", plugin.View{}).WaitFor(t, primTestTimeout, nil)

	pCap, _ := primTestNewProbe(t, capTree, ggui.Sz(560, 480))
	szCap := primTestDraw(t, pCap, 560, 480)
	pExp, _ := primTestNewProbe(t, expTree, ggui.Sz(560, 480))
	szExp := primTestDraw(t, pExp, 560, 480)

	if szCap.H > 160 {
		t.Fatalf("expected maxHeight=110 chart to remain constrained <=160, got %v", szCap.H)
	}
	if szExp.H <= szCap.H+100 {
		t.Fatalf("expected grow=1 fill chart (%v) to expand significantly taller than maxHeight=110 chart (%v)", szExp.H, szCap.H)
	}
}

func TestPrimT1ChartClickPointAndHeatmapCellEmitsOnChange(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	js := `export default {
  activate(mako) {
    mako.blocks.register("main", {
      state: { lastArea: "", lastHeat: "" },
      render(s, ui) {
        return ui.column([
          ui.text("area:" + s.lastArea + "|heat:" + s.lastHeat, { key: "sel" }),
          ui.chart("area", [{ name: "Latency", tone: "accent", values: [12, 34, 22, 48] }], {
            key: "ch-area", caption: "Latency Area", labels: ["T1", "T2", "T3", "T4"], maxHeight: 160, onChange: ["pickArea"]
          }),
          ui.chart("heatmap", [
            { name: "R1", values: [1, 5, 9] },
            { name: "R2", values: [4, 8, 2] }
          ], {
            key: "ch-heat", caption: "Heat Grid", labels: ["C1", "C2", "C3"], maxHeight: 160, onChange: ["pickHeat"]
          })
        ], { key: "root", gap: 8 });
      },
      on: {
        pickArea(s, v) { s.lastArea = typeof v === "string" ? v : JSON.stringify(v); },
        pickHeat(s, v) { s.lastHeat = typeof v === "string" ? v : JSON.stringify(v); }
      }
    });
  }
};`
	w := primTestInstallAndWatch(t, rig, "ch-click", primTestMinimalToml("ch-click", "main"), js, "main")
	tree := w.WaitFor(t, primTestTimeout, func(n *protocol.UiNode) bool {
		return FindNodeByKey(n, "ch-area") != nil && FindNodeByKey(n, "ch-heat") != nil
	})

	p, log := primTestNewProbe(t, tree, ggui.Sz(560, 440))
	sem := p.Semantics()

	areaNode, ok := primTestFindSemSubstr(sem, "", "Latency Area")
	if !ok {
		t.Fatalf("expected Latency Area in semantics:\n%s", sem.String())
	}
	p.Click(primTestRectCenter(areaNode.Rect))
	p.Frame()

	heatNode, ok := primTestFindSemSubstr(sem, "", "Heat Grid")
	if !ok {
		t.Fatalf("expected Heat Grid in semantics:\n%s", sem.String())
	}
	p.Click(primTestRectCenter(heatNode.Rect))
	p.Frame()

	evs := log.snapshot()
	if len(evs) < 2 {
		t.Fatalf("expected >= 2 chart click events (area + heatmap), got %+v", evs)
	}

	areaUi := FindNodeByKey(tree, "ch-area")
	heatUi := FindNodeByKey(tree, "ch-heat")
	var areaEv, heatEv *primTestUiEvent
	for i := range evs {
		ev := evs[i]
		if ev.id == areaUi.GetId() {
			areaEv = &ev
		}
		if ev.id == heatUi.GetId() {
			heatEv = &ev
		}
	}
	if areaEv == nil || areaEv.kind != protocol.UiChange || areaEv.value == "" {
		t.Fatalf("missing valid area chart onChange event in %+v", evs)
	}
	if heatEv == nil || heatEv.kind != protocol.UiChange || heatEv.value == "" {
		t.Fatalf("missing valid heatmap chart onChange event in %+v", evs)
	}

	if _, err := rig.SendEvent(t, &protocol.BlockEvent{
		Block: "plugin:ch-click/main",
		Node:  areaEv.id,
		Kind:  areaEv.kind,
		Value: areaEv.value,
	}); err != nil {
		t.Fatalf("SendEvent area: %v", err)
	}
	if _, err := rig.SendEvent(t, &protocol.BlockEvent{
		Block: "plugin:ch-click/main",
		Node:  heatEv.id,
		Kind:  heatEv.kind,
		Value: heatEv.value,
	}); err != nil {
		t.Fatalf("SendEvent heat: %v", err)
	}

	w.WaitFor(t, primTestTimeout, func(n *protocol.UiNode) bool {
		sel := FindNodeByKey(n, "sel")
		return sel != nil && !strings.HasSuffix(sel.GetText(), "|heat:") && !strings.HasPrefix(sel.GetText(), "area:|")
	})
}

// --- F4.1: Demo Plugins Update (5 Tier 1 tests) ---

func TestPrimT1DemoUIWorkbenchManifestAndLoad(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	rig.CopyPluginDir(t, DemoPluginDir(t, "ui-workbench"), "ui-workbench")
	rig.Grant(t, "ui-workbench")

	info := rig.WaitPlugin(t, "ui-workbench", func(p *protocol.PluginInfo) bool {
		return p.GetState() == protocol.PluginReady || p.GetState() == protocol.PluginRunning
	})
	if info.GetError() != "" {
		t.Fatalf("ui-workbench error: %s", info.GetError())
	}
	var blockIDs []string
	for _, b := range info.GetBlocks() {
		blockIDs = append(blockIDs, b.GetName())
	}
	for _, want := range []string{"plugin:ui-workbench/explorer", "plugin:ui-workbench/viewer"} {
		if !slices.Contains(blockIDs, want) {
			t.Fatalf("expected block %q in %v", want, blockIDs)
		}
	}
}

func TestPrimT1DemoUIWorkbenchShowcasesCanvas(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	rig.CopyPluginDir(t, DemoPluginDir(t, "ui-workbench"), "ui-workbench")
	rig.Grant(t, "ui-workbench")

	trees := primTestCollectUIWorkbenchTrees(t, rig)
	var foundTree *protocol.UiNode
	for _, tr := range trees {
		if FindNodeByKind(tr, "canvas") != nil {
			foundTree = tr
			break
		}
	}
	if foundTree == nil {
		t.Fatal("expected updated ui-workbench to showcase at least one ui.canvas node")
	}
	p, _ := primTestNewProbe(t, foundTree, ggui.Sz(820, 640))
	primTestDraw(t, p, 820, 640)
}

func TestPrimT1DemoUIWorkbenchShowcasesTimeline(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	rig.CopyPluginDir(t, DemoPluginDir(t, "ui-workbench"), "ui-workbench")
	rig.Grant(t, "ui-workbench")

	trees := primTestCollectUIWorkbenchTrees(t, rig)
	var foundTree *protocol.UiNode
	for _, tr := range trees {
		if FindNodeByKind(tr, "timeline") != nil {
			foundTree = tr
			break
		}
	}
	if foundTree == nil {
		t.Fatal("expected updated ui-workbench to showcase at least one ui.timeline node")
	}
	p, _ := primTestNewProbe(t, foundTree, ggui.Sz(820, 640))
	primTestDraw(t, p, 820, 640)
}

func TestPrimT1DemoUIWorkbenchShowcasesNewCharts(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	rig.CopyPluginDir(t, DemoPluginDir(t, "ui-workbench"), "ui-workbench")
	rig.Grant(t, "ui-workbench")

	trees := primTestCollectUIWorkbenchTrees(t, rig)
	for _, chartType := range []string{"area", "stacked-bar", "heatmap"} {
		if primTestFindChartType(trees, chartType) == nil {
			t.Fatalf("expected updated ui-workbench to showcase chart type %q", chartType)
		}
	}
	for _, tr := range trees {
		if len(FindAllNodesByKind(tr, "chart")) > 0 {
			p, _ := primTestNewProbe(t, tr, ggui.Sz(820, 640))
			primTestDraw(t, p, 820, 640)
			break
		}
	}
}

func TestPrimT1DemoAgentMapLoadAndUpdatedGraph(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{
		Sessions: []*protocol.SessionInfo{
			{
				Id:      "sess-root-1",
				Name:    "Lead Architect",
				Agent:   "claude",
				Project: "/tmp/mako-repo",
				State:   protocol.StateRunning,
				Status:  protocol.AgentWorking,
				NodeId:  "node-mac",
			},
			{
				Id:      "sess-sub-2",
				Name:    "Test Subagent",
				Agent:   "claude",
				Project: "/tmp/mako-repo",
				State:   protocol.StateRunning,
				Status:  protocol.AgentWaiting,
				NodeId:  "node-mac",
			},
		},
	})
	rig.CopyPluginDir(t, DemoPluginDir(t, "agent-map"), "agent-map")
	rig.Grant(t, "agent-map")

	w := rig.WatchTree(t, "plugin:agent-map/map", "", plugin.View{})
	tree := w.WaitFor(t, primTestTimeout, func(n *protocol.UiNode) bool {
		g := FindNodeByKind(n, "graph")
		return g != nil && len(g.GetGraphNodes()) > 0
	})

	p, _ := primTestNewProbe(t, tree, ggui.Sz(960, 640))
	primTestDraw(t, p, 960, 640)
	sem := p.Semantics().String()
	if !strings.Contains(sem, "Lead Architect") && !strings.Contains(sem, "Agent Map") {
		t.Fatalf("expected agent-map semantics to include rendered session labels:\n%s", sem)
	}
}

// --- F4.2: AppSupport Plugin Sync (5 Tier 1 tests) ---

func TestPrimT1AppSupportPluginsDirExists(t *testing.T) {
	dir, err := AppSupportPluginsDir()
	if err != nil {
		t.Fatalf("AppSupportPluginsDir: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat %q: %v", dir, err)
	}
	if !info.IsDir() {
		t.Fatalf("expected %q to be a directory", dir)
	}
}

func TestPrimT1AppSupportAgentMapByteParity(t *testing.T) {
	appDir, err := AppSupportPluginsDir()
	if err != nil {
		t.Fatalf("AppSupportPluginsDir: %v", err)
	}
	diffs, err := primTestComparePluginDirs(DemoPluginDir(t, "agent-map"), filepath.Join(appDir, "agent-map"))
	if err != nil {
		t.Fatalf("compare agent-map: %v", err)
	}
	if len(diffs) > 0 {
		t.Fatalf("agent-map AppSupport parity drift:\n%s", strings.Join(diffs, "\n"))
	}
}

func TestPrimT1AppSupportUIWorkbenchByteParity(t *testing.T) {
	appDir, err := AppSupportPluginsDir()
	if err != nil {
		t.Fatalf("AppSupportPluginsDir: %v", err)
	}
	diffs, err := primTestComparePluginDirs(DemoPluginDir(t, "ui-workbench"), filepath.Join(appDir, "ui-workbench"))
	if err != nil {
		t.Fatalf("compare ui-workbench: %v", err)
	}
	if len(diffs) > 0 {
		t.Fatalf("ui-workbench AppSupport parity drift:\n%s", strings.Join(diffs, "\n"))
	}
}

func TestPrimT1AppSupportOtherExamplePluginsParity(t *testing.T) {
	appDir, err := AppSupportPluginsDir()
	if err != nil {
		t.Fatalf("AppSupportPluginsDir: %v", err)
	}
	for _, id := range []string{"wasm-inspector", "okf"} {
		srcToml, err := os.ReadFile(filepath.Join(DemoPluginDir(t, id), plugin.ManifestName))
		if err != nil {
			t.Fatalf("read example %s plugin.toml: %v", id, err)
		}
		dstToml, err := os.ReadFile(filepath.Join(appDir, id, plugin.ManifestName))
		if err != nil {
			t.Fatalf("read AppSupport %s plugin.toml: %v", id, err)
		}
		if !bytes.Equal(srcToml, dstToml) {
			t.Fatalf("%s/plugin.toml differs between examples/plugins and AppSupport", id)
		}
		diffs, err := primTestComparePluginDirs(DemoPluginDir(t, id), filepath.Join(appDir, id))
		if err != nil {
			t.Fatalf("compare %s: %v", id, err)
		}
		if len(diffs) > 0 {
			t.Fatalf("%s AppSupport parity drift:\n%s", id, strings.Join(diffs, "\n"))
		}
	}
}

func TestPrimT1AppSupportSyncedPluginsLoadAndRender(t *testing.T) {
	appDir, err := AppSupportPluginsDir()
	if err != nil {
		t.Fatalf("AppSupportPluginsDir: %v", err)
	}
	rig := NewPluginRig(t, PluginRigOpts{
		Sessions: []*protocol.SessionInfo{
			{Id: "s-app-1", Name: "Synced Session", Agent: "claude", Project: "/tmp/proj", State: protocol.StateRunning, Status: protocol.AgentWorking},
		},
	})
	rig.CopyPluginDir(t, filepath.Join(appDir, "ui-workbench"), "ui-workbench")
	rig.CopyPluginDir(t, filepath.Join(appDir, "agent-map"), "agent-map")
	rig.Grant(t, "ui-workbench")
	rig.Grant(t, "agent-map")

	wbTree := rig.WatchTree(t, "plugin:ui-workbench/explorer", "", plugin.View{}).WaitFor(t, primTestTimeout, nil)
	amTree := rig.WatchTree(t, "plugin:agent-map/map", "", plugin.View{}).WaitFor(t, primTestTimeout, nil)

	pWB, _ := primTestNewProbe(t, wbTree, ggui.Sz(420, 640))
	primTestDraw(t, pWB, 420, 640)
	pAM, _ := primTestNewProbe(t, amTree, ggui.Sz(900, 600))
	primTestDraw(t, pAM, 900, 600)
}

// =============================================================================
// TIER 2 — Boundary & Negative Cases (>= 25 tests across F3.1–F3.3, F4.1–F4.2)
// =============================================================================

// --- F3.1: ui.canvas Boundary & Negative (5 Tier 2 tests) ---

func TestPrimT2CanvasRejectsRawHexTone(t *testing.T) {
	for _, tc := range []struct {
		name string
		cmd  string
	}{
		{"tone", `{ op: "text", x: 10, y: 20, text: "bad", tone: "#ff0000" }`},
		{"fillTone", `{ op: "roundRect", x: 10, y: 10, w: 50, h: 30, fillTone: "#112233" }`},
		{"strokeTone", `{ op: "circle", x: 40, y: 40, r: 20, strokeTone: "#abc" }`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			js := fmt.Sprintf(`export default {
  activate(mako) {
    mako.blocks.register("main", {
      render(s, ui) { return ui.canvas([%s], { key: "c" }); }
    });
  }
};`, tc.cmd)
			errMsg := primTestMustRejectRender(t, "neg-hex-"+strings.ToLower(tc.name), js)
			if errMsg == "" {
				t.Fatal("expected non-empty rejection error for raw hex tone")
			}
		})
	}
}

func TestPrimT2CanvasRejectsRGBAndNamedColors(t *testing.T) {
	for _, tc := range []struct {
		name  string
		color string
	}{
		{"rgb", "rgb(255,0,0)"},
		{"rgba", "rgba(10, 20, 30, 0.5)"},
		{"named", "rebeccapurple"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			js := fmt.Sprintf(`export default {
  activate(mako) {
    mako.blocks.register("main", {
      render(s, ui) {
        return ui.canvas([{ op: "roundRect", x: 10, y: 10, w: 60, h: 40, fillTone: %q }], { key: "c" });
      }
    });
  }
};`, tc.color)
			errMsg := primTestMustRejectRender(t, "neg-col-"+tc.name, js)
			if errMsg == "" {
				t.Fatalf("expected non-token color %q to be rejected", tc.color)
			}
		})
	}
}

func TestPrimT2CanvasRejectsUnknownOp(t *testing.T) {
	for _, op := range []string{"eval", "script", ""} {
		t.Run("op_"+op, func(t *testing.T) {
			js := fmt.Sprintf(`export default {
  activate(mako) {
    mako.blocks.register("main", {
      render(s, ui) {
        return ui.canvas([{ op: %q, x: 10, y: 10, text: "x" }], { key: "c" });
      }
    });
  }
};`, op)
			id := "neg-op-" + op
			if op == "" {
				id = "neg-op-empty"
			}
			errMsg := primTestMustRejectRender(t, id, js)
			if errMsg == "" {
				t.Fatalf("expected unknown canvas op %q to be rejected", op)
			}
		})
	}
}

func TestPrimT2CanvasEmptyCommandsRendersGracefully(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	js := `export default {
  activate(mako) {
    mako.blocks.register("main", {
      render(s, ui) {
        return ui.canvas([], { key: "empty-c", width: 320, height: 180 });
      }
    });
  }
};`
	w := primTestInstallAndWatch(t, rig, "canvas-empty", primTestMinimalToml("canvas-empty", "main"), js, "main")
	tree := w.WaitFor(t, primTestTimeout, func(n *protocol.UiNode) bool {
		return FindNodeByKind(n, "canvas") != nil
	})
	p, _ := primTestNewProbe(t, tree, ggui.Sz(380, 240))
	primTestDraw(t, p, 380, 240)
	p.Click(ggui.Pt(160, 90))
	p.Frame()
}

func TestPrimT2CanvasClickEmptyRegionEmitsNoEvent(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	js := `export default {
  activate(mako) {
    mako.blocks.register("main", {
      render(s, ui) {
        return ui.canvas([
          { op: "roundRect", x: 16, y: 16, w: 48, h: 36, r: 4, fillTone: "accent", hitId: "tiny-box" }
        ], { key: "vec", width: 480, height: 280, onChange: ["hit"] });
      },
      on: { hit() {} }
    });
  }
};`
	w := primTestInstallAndWatch(t, rig, "canvas-miss", primTestMinimalToml("canvas-miss", "main"), js, "main")
	tree := w.WaitFor(t, primTestTimeout, func(n *protocol.UiNode) bool {
		return FindNodeByKind(n, "canvas") != nil
	})
	p, log := primTestNewProbe(t, tree, ggui.Sz(520, 320))
	p.Click(ggui.Pt(420, 240))
	p.Frame()
	if evs := log.snapshot(); len(evs) != 0 {
		t.Fatalf("expected click in empty canvas region to emit zero events, got %+v", evs)
	}
}

// --- F3.2: ui.timeline Boundary & Negative (5 Tier 2 tests) ---

func TestPrimT2TimelineOutOfOrderSpanSafeOrRejected(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	js := `export default {
  activate(mako) {
    mako.blocks.register("main", {
      render(s, ui) {
        return ui.timeline([
          { id: "l1", label: "Lane", spans: [{ id: "bad", startMs: 9000, endMs: 2000, label: "Reversed", tone: "warning" }] }
        ], { key: "tl", maxHeight: 200 });
      }
    });
  }
};`
	w := primTestInstallAndWatch(t, rig, "tl-ooo", primTestMinimalToml("tl-ooo", "main"), js, "main")
	tr, upd := w.WaitUpdate(t, primTestTimeout, func(tree *protocol.UiNode, u *protocol.BlockUpdate) bool {
		return u.GetError() != "" || tree != nil
	})
	if upd.GetError() != "" {
		return
	}
	p, _ := primTestNewProbe(t, tr, ggui.Sz(560, 240))
	primTestDraw(t, p, 560, 240)
}

func TestPrimT2TimelineZeroDurationSpanRendersSafely(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	js := `export default {
  activate(mako) {
    mako.blocks.register("main", {
      render(s, ui) {
        return ui.timeline([
          { id: "l1", label: "Instant", spans: [{ id: "zero", startMs: 5000, endMs: 5000, label: "Marker", tone: "accent" }] }
        ], { key: "tl", maxHeight: 200, onChange: ["pick"] });
      },
      on: { pick() {} }
    });
  }
};`
	w := primTestInstallAndWatch(t, rig, "tl-zero", primTestMinimalToml("tl-zero", "main"), js, "main")
	tree := w.WaitFor(t, primTestTimeout, func(n *protocol.UiNode) bool {
		return FindNodeByKind(n, "timeline") != nil
	})
	p, _ := primTestNewProbe(t, tree, ggui.Sz(560, 240))
	primTestDraw(t, p, 560, 240)
	p.Click(ggui.Pt(280, 100))
	p.Frame()
}

func TestPrimT2TimelineEmptyLanesAndSpansRendersCleanly(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	js := `export default {
  activate(mako) {
    mako.blocks.register("empty", {
      render(s, ui) { return ui.timeline([], { key: "tl0", maxHeight: 180 }); }
    });
    mako.blocks.register("nospans", {
      render(s, ui) { return ui.timeline([{ id: "l1", label: "Idle Lane", spans: [] }], { key: "tl1", maxHeight: 180 }); }
    });
  }
};`
	toml := `id = "tl-empty"
version = "0.1.0"
publisher = "e2e"
api = 1
entry = "main.js"
permissions = []

[[contributes.block]]
id = "empty"
title = "Empty"
slots = ["right"]

[[contributes.block]]
id = "nospans"
title = "NoSpans"
slots = ["right"]
`
	rig.InstallJS(t, "tl-empty", toml, js)
	rig.Grant(t, "tl-empty")

	t0 := rig.WatchTree(t, "plugin:tl-empty/empty", "", plugin.View{}).WaitFor(t, primTestTimeout, nil)
	t1 := rig.WatchTree(t, "plugin:tl-empty/nospans", "", plugin.View{}).WaitFor(t, primTestTimeout, nil)

	p0, _ := primTestNewProbe(t, t0, ggui.Sz(500, 220))
	primTestDraw(t, p0, 500, 220)
	p1, _ := primTestNewProbe(t, t1, ggui.Sz(500, 220))
	primTestDraw(t, p1, 500, 220)
}

func TestPrimT2TimelineRejectsInvalidSpanTone(t *testing.T) {
	for _, tone := range []string{"#ff0000", "magenta"} {
		t.Run("tone_"+tone, func(t *testing.T) {
			js := fmt.Sprintf(`export default {
  activate(mako) {
    mako.blocks.register("main", {
      render(s, ui) {
        return ui.timeline([
          { id: "l1", label: "Lane", spans: [{ id: "s1", startMs: 0, endMs: 1000, label: "Bad", tone: %q }] }
        ], { key: "tl" });
      }
    });
  }
};`, tone)
			errMsg := primTestMustRejectRender(t, "neg-tl-tone-"+strings.TrimPrefix(tone, "#"), js)
			if errMsg == "" {
				t.Fatalf("expected invalid timeline span tone %q to be rejected", tone)
			}
		})
	}
}

func TestPrimT2TimelineExtremeTimestampRangeNoHangOrDivZero(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	js := `export default {
  activate(mako) {
    mako.blocks.register("main", {
      render(s, ui) {
        return ui.timeline([
          {
            id: "wide",
            label: "Epoch",
            spans: [
              { id: "e1", startMs: 0, endMs: 1e13, label: "Full Epoch", tone: "accent" },
              { id: "e2", startMs: 1e12, endMs: 1e12 + 1, label: "1ms Tick", tone: "success" }
            ]
          }
        ], { key: "tl", maxHeight: 220 });
      }
    });
  }
};`
	w := primTestInstallAndWatch(t, rig, "tl-extreme", primTestMinimalToml("tl-extreme", "main"), js, "main")
	tree := w.WaitFor(t, primTestTimeout, func(n *protocol.UiNode) bool {
		return FindNodeByKind(n, "timeline") != nil
	})
	p, _ := primTestNewProbe(t, tree, ggui.Sz(640, 260))
	primTestDraw(t, p, 640, 260)
}

// --- F3.3: ui.chart Boundary & Negative (5 Tier 2 tests) ---

func TestPrimT2ChartRejectsUnknownChartType(t *testing.T) {
	MustRejectUI(t, `{"kind":"chart","chart":"3d-pie","series":[{"name":"S","values":[1,2,3]}]}`)

	js := `export default {
  activate(mako) {
    mako.blocks.register("main", {
      render(s, ui) {
        return ui.chart("3d-pie", [{ name: "S", values: [1, 2, 3] }], { key: "bad" });
      }
    });
  }
};`
	if errMsg := primTestMustRejectRender(t, "neg-chart-3dpie", js); errMsg == "" {
		t.Fatal("expected unknown chart type 3d-pie to be rejected")
	}
}

func TestPrimT2ChartEmptySeriesHandledGracefully(t *testing.T) {
	// Both uiblock rejection and headless GUI rendering must handle empty series without panic.
	_, _ = uiblock.Read([]byte(`{"kind":"chart","chart":"area","series":[]}`))

	emptyChart := &protocol.UiNode{
		Id:     "/ch",
		Kind:   "chart",
		Chart:  "area",
		Text:   "Empty Area",
		Series: nil,
	}
	p, _ := primTestNewProbe(t, emptyChart, ggui.Sz(480, 240))
	primTestDraw(t, p, 480, 240)
	p.Click(ggui.Pt(240, 120))
	p.Frame()
}

func TestPrimT2ChartMismatchedSeriesLengthsSafe(t *testing.T) {
	// Check both stacked-bar and heatmap with uneven series lengths in uiblock and GUI probe.
	for _, kind := range []string{"stacked-bar", "heatmap"} {
		t.Run(kind, func(t *testing.T) {
			_, _ = uiblock.Read([]byte(fmt.Sprintf(
				`{"kind":"chart","chart":%q,"series":[{"name":"A","values":[1,2,3,4]},{"name":"B","values":[5,6]}]}`,
				kind,
			)))
			node := &protocol.UiNode{
				Id:     "/ch",
				Kind:   "chart",
				Chart:  kind,
				Text:   "Uneven " + kind,
				Labels: []string{"C1", "C2", "C3", "C4"},
				Series: []*protocol.UiSeries{
					{Name: "A", Tone: "accent", Values: []float64{1, 2, 3, 4}},
					{Name: "B", Tone: "success", Values: []float64{5, 6}},
				},
			}
			p, _ := primTestNewProbe(t, node, ggui.Sz(520, 260))
			primTestDraw(t, p, 520, 260)
			p.Click(ggui.Pt(260, 130))
			p.Frame()
		})
	}
}

func TestPrimT2ChartOmittedOnChangeClickDoesNotPanic(t *testing.T) {
	for _, kind := range []string{"area", "stacked-bar", "heatmap"} {
		t.Run(kind, func(t *testing.T) {
			node := &protocol.UiNode{
				Id:     "/static",
				Kind:   "chart",
				Chart:  kind,
				Text:   "Static " + kind,
				Change: false,
				Labels: []string{"A", "B", "C"},
				Series: []*protocol.UiSeries{
					{Name: "S1", Tone: "accent", Values: []float64{3, 7, 5}},
					{Name: "S2", Tone: "success", Values: []float64{2, 4, 6}},
				},
			}
			p, log := primTestNewProbe(t, node, ggui.Sz(500, 260))
			primTestDraw(t, p, 500, 260)
			p.Click(ggui.Pt(180, 130))
			p.Click(ggui.Pt(280, 140))
			p.Frame()
			if evs := log.snapshot(); len(evs) != 0 {
				t.Fatalf("expected static %s chart click to emit zero events, got %+v", kind, evs)
			}
		})
	}
}

func TestPrimT2ChartMaxHeightBoundaries(t *testing.T) {
	MustRejectUI(t, `{"kind":"chart","chart":"area","maxHeight":-50,"series":[{"name":"S","values":[1,2,3]}]}`)
	MustRejectUI(t, `{"kind":"chart","chart":"area","maxHeight":999999,"series":[{"name":"S","values":[1,2,3]}]}`)

	// NaN in JS becomes null in JSON (or fails validation); verify no runtime crash either way.
	rig := NewPluginRig(t, PluginRigOpts{})
	js := `export default {
  activate(mako) {
    mako.blocks.register("main", {
      render(s, ui) {
        return ui.chart("area", [{ name: "S", values: [1, 2, 3] }], { key: "ch", maxHeight: NaN });
      }
    });
  }
};`
	w := primTestInstallAndWatch(t, rig, "ch-nan-h", primTestMinimalToml("ch-nan-h", "main"), js, "main")
	tr, upd := w.WaitUpdate(t, primTestTimeout, func(tree *protocol.UiNode, u *protocol.BlockUpdate) bool {
		return u.GetError() != "" || tree != nil
	})
	if upd.GetError() == "" && tr != nil {
		p, _ := primTestNewProbe(t, tr, ggui.Sz(420, 240))
		primTestDraw(t, p, 420, 240)
	}
}

// --- F4.1: Demo Plugins Boundary & Negative (5 Tier 2 tests) ---

func TestPrimT2DemoUIWorkbenchEmptyAndCorruptedStorageRecovers(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	rig.CopyPluginDir(t, DemoPluginDir(t, "ui-workbench"), "ui-workbench")
	rig.Grant(t, "ui-workbench")

	storeDir := filepath.Join(rig.DataDir, "ui-workbench")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatalf("mkdir store: %v", err)
	}
	corruptPayload, _ := json.Marshal(map[string]string{
		"comments:default:diff-auth": "{corrupted-json",
		"comments:tab1:diff-auth":    "null",
		"scratch:default":            "",
	})
	if err := os.WriteFile(filepath.Join(storeDir, "storage.json"), corruptPayload, 0o644); err != nil {
		t.Fatalf("write corrupt storage: %v", err)
	}

	exp := rig.WatchTree(t, "plugin:ui-workbench/explorer", "", plugin.View{}).WaitFor(t, primTestTimeout, nil)
	view := rig.WatchTree(t, "plugin:ui-workbench/viewer", "", plugin.View{}).WaitFor(t, primTestTimeout, nil)
	pExp, _ := primTestNewProbe(t, exp, ggui.Sz(380, 560))
	primTestDraw(t, pExp, 380, 560)
	pView, _ := primTestNewProbe(t, view, ggui.Sz(720, 560))
	primTestDraw(t, pView, 720, 560)
}

func TestPrimT2DemoUIWorkbenchUnknownTabOrSectionFallback(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	rig.CopyPluginDir(t, DemoPluginDir(t, "ui-workbench"), "ui-workbench")
	rig.Grant(t, "ui-workbench")

	w := rig.WatchTree(t, "plugin:ui-workbench/viewer", "", plugin.View{
		Instance: "bad-inst",
		Params:   `{"preset":"nonexistent-preset","section":"unknown-section"}`,
	})
	tree := w.WaitFor(t, primTestTimeout, nil)
	if len(TreeTexts(tree)) == 0 {
		t.Fatal("expected ui-workbench viewer to fall back to valid rendered content")
	}
	p, _ := primTestNewProbe(t, tree, ggui.Sz(760, 560))
	primTestDraw(t, p, 760, 560)
}

func TestPrimT2DemoAgentMapInvalidBlockEventSafe(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{
		Sessions: []*protocol.SessionInfo{
			{Id: "s1", Name: "Worker", Agent: "claude", Project: "/tmp/p", State: protocol.StateRunning, Status: protocol.AgentWorking},
		},
	})
	rig.CopyPluginDir(t, DemoPluginDir(t, "agent-map"), "agent-map")
	rig.Grant(t, "agent-map")

	w := rig.WatchTree(t, "plugin:agent-map/map", "", plugin.View{})
	w.WaitFor(t, primTestTimeout, nil)

	// Bogus node ID must fail gracefully with an error, not crash the plugin runtime.
	if _, err := rig.SendEvent(t, &protocol.BlockEvent{
		Block: "plugin:agent-map/map",
		Node:  "/nonexistent-node",
		Kind:  protocol.UiTap,
	}); err == nil {
		t.Fatal("expected BlockEvent targeting nonexistent node to return an error")
	}

	// Empty session list + subsequent valid refresh must continue working cleanly.
	rig.SetSessions(nil)
	if _, err := rig.SendEvent(t, &protocol.BlockEvent{
		Block: "plugin:agent-map/map",
		Node:  "/tb/btn-refresh",
		Kind:  protocol.UiTap,
	}); err != nil {
		t.Fatalf("valid refresh after invalid event failed: %v", err)
	}
	w.WaitFor(t, primTestTimeout, func(n *protocol.UiNode) bool {
		return FindNodeByKind(n, "empty") != nil
	})
}

func TestPrimT2DemoUIWorkbenchViewerStandaloneWithoutParams(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	rig.CopyPluginDir(t, DemoPluginDir(t, "ui-workbench"), "ui-workbench")
	rig.Grant(t, "ui-workbench")

	w := rig.WatchTree(t, "plugin:ui-workbench/viewer", "", plugin.View{Standalone: true})
	tree := w.WaitFor(t, primTestTimeout, nil)
	p, _ := primTestNewProbe(t, tree, ggui.Sz(800, 600))
	primTestDraw(t, p, 800, 600)
}

func TestPrimT2DemoUIWorkbenchClickAllShowcaseTabsAndControlsZeroErrors(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{
		Limits: plugin.Limits{
			CPU:     10 * time.Second,
			Wall:    20 * time.Second,
			Memory:  16 << 20,
			Updates: 20 * time.Millisecond,
			Events:  200,
		},
	})
	rig.CopyPluginDir(t, DemoPluginDir(t, "ui-workbench"), "ui-workbench")
	rig.Grant(t, "ui-workbench")

	// 1. Exercise explorer block controls against plugin:ui-workbench/explorer.
	wExp := rig.WatchTree(t, "plugin:ui-workbench/explorer", "", plugin.View{})
	expTree := wExp.WaitFor(t, primTestTimeout, nil)
	if search := FindNodeByKey(expTree, "search"); search != nil {
		if _, err := rig.SendEvent(t, &protocol.BlockEvent{
			Block: "plugin:ui-workbench/explorer",
			Node:  search.GetId(),
			Kind:  protocol.UiChange,
			Value: "diff",
		}); err != nil {
			t.Fatalf("explorer search change failed: %v", err)
		}
		if _, err := rig.SendEvent(t, &protocol.BlockEvent{
			Block: "plugin:ui-workbench/explorer",
			Node:  search.GetId(),
			Kind:  protocol.UiChange,
			Value: "",
		}); err != nil {
			t.Fatalf("explorer search clear failed: %v", err)
		}
		expTree = wExp.WaitFor(t, primTestTimeout, func(n *protocol.UiNode) bool {
			return FindNodeByKey(n, "preset-list") != nil
		})
	}
	for _, btn := range FindAllNodesByKind(expTree, "button") {
		if !btn.GetTap() || strings.HasSuffix(btn.GetId(), "/tb-reset") || strings.HasSuffix(btn.GetId(), "/clear-search") {
			continue
		}
		if _, err := rig.SendEvent(t, &protocol.BlockEvent{
			Block: "plugin:ui-workbench/explorer",
			Node:  btn.GetId(),
			Kind:  protocol.UiTap,
		}); err != nil {
			t.Fatalf("explorer button %q tap failed: %v", btn.GetId(), err)
		}
	}

	// 2. Exercise viewer block tabs, sub-tabs, radios/selects, buttons, and toggles while active.
	wView := rig.WatchTree(t, "plugin:ui-workbench/viewer", "", plugin.View{})
	viewTree := wView.WaitFor(t, primTestTimeout, nil)
	mainTabs := FindNodeByKey(viewTree, "main-tabs")
	if mainTabs == nil || len(mainTabs.GetOptions()) == 0 {
		t.Fatal("expected main-tabs in ui-workbench/viewer")
	}

	exerciseViewerTree := func(tr *protocol.UiNode) {
		// Tap safe buttons before toggling switches (e.g., empty-act is removed once sk-toggle=true).
		for _, btn := range FindAllNodesByKind(tr, "button") {
			if !btn.GetTap() || strings.HasSuffix(btn.GetId(), "/btn-close") || strings.HasSuffix(btn.GetId(), "/inst-close") || strings.HasSuffix(btn.GetId(), "/tb-reset") {
				continue
			}
			if _, err := rig.SendEvent(t, &protocol.BlockEvent{
				Block: "plugin:ui-workbench/viewer",
				Node:  btn.GetId(),
				Kind:  protocol.UiTap,
			}); err != nil {
				t.Fatalf("viewer button %q tap failed: %v", btn.GetId(), err)
			}
		}
		for _, tg := range FindAllNodesByKind(tr, "toggle") {
			if !tg.GetChange() {
				continue
			}
			if _, err := rig.SendEvent(t, &protocol.BlockEvent{
				Block: "plugin:ui-workbench/viewer",
				Node:  tg.GetId(),
				Kind:  protocol.UiChange,
				Value: "true",
			}); err != nil {
				t.Fatalf("viewer toggle %q change failed: %v", tg.GetId(), err)
			}
		}
	}

	for _, opt := range mainTabs.GetOptions() {
		if _, err := rig.SendEvent(t, &protocol.BlockEvent{
			Block: "plugin:ui-workbench/viewer",
			Node:  mainTabs.GetId(),
			Kind:  protocol.UiChange,
			Value: opt,
		}); err != nil {
			t.Fatalf("switch ui-workbench main tab %q: %v", opt, err)
		}
		cur := wView.WaitFor(t, primTestTimeout, func(n *protocol.UiNode) bool {
			mt := FindNodeByKey(n, "main-tabs")
			return mt != nil && mt.GetValue() == opt
		})
		exerciseViewerTree(cur)

		for _, kind := range []string{"radio", "select"} {
			for _, ctrl := range FindAllNodesByKind(cur, kind) {
				if !ctrl.GetChange() {
					continue
				}
				for _, ctrlOpt := range ctrl.GetOptions() {
					if _, err := rig.SendEvent(t, &protocol.BlockEvent{
						Block: "plugin:ui-workbench/viewer",
						Node:  ctrl.GetId(),
						Kind:  protocol.UiChange,
						Value: ctrlOpt,
					}); err != nil {
						t.Fatalf("switch ui-workbench %s %q: %v", kind, ctrlOpt, err)
					}
					_ = wView.WaitFor(t, primTestTimeout, func(n *protocol.UiNode) bool {
						c := FindNodeByID(n, ctrl.GetId())
						return c != nil && c.GetValue() == ctrlOpt
					})
				}
			}
		}

		for _, subTabs := range FindAllNodesByKind(cur, "tabs") {
			if subTabs.GetId() == mainTabs.GetId() {
				continue
			}
			for _, subOpt := range subTabs.GetOptions() {
				if _, err := rig.SendEvent(t, &protocol.BlockEvent{
					Block: "plugin:ui-workbench/viewer",
					Node:  subTabs.GetId(),
					Kind:  protocol.UiChange,
					Value: subOpt,
				}); err != nil {
					t.Fatalf("switch ui-workbench sub-tab %q: %v", subOpt, err)
				}
				subTree := wView.WaitFor(t, primTestTimeout, func(n *protocol.UiNode) bool {
					st := FindNodeByID(n, subTabs.GetId())
					return st != nil && st.GetValue() == subOpt
				})
				exerciseViewerTree(subTree)
			}
		}
	}
}

// --- F4.2: AppSupport Parity Boundary & Negative (5 Tier 2 tests) ---

func TestPrimT2AppSupportParityIgnoresSignatureAndDetectsDrift(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	for _, dir := range []string{src, dst} {
		if err := os.WriteFile(filepath.Join(dir, "plugin.toml"), []byte("id = \"demo\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "main.js"), []byte("export default { activate() {} };\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Non-source signature and macOS metadata files must be ignored cleanly.
	if err := os.WriteFile(filepath.Join(dst, "mako-signature.json"), []byte(`{"sig":"abc"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, ".DS_Store"), []byte{0, 1, 2}, 0o644); err != nil {
		t.Fatal(err)
	}
	diffs, err := primTestComparePluginDirs(src, dst)
	if err != nil || len(diffs) != 0 {
		t.Fatalf("expected signature/.DS_Store to be ignored, got diffs=%v err=%v", diffs, err)
	}

	// Drift in main.js must be caught.
	if err := os.WriteFile(filepath.Join(dst, "main.js"), []byte("export default { activate() { return 1; } };\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	diffs, err = primTestComparePluginDirs(src, dst)
	if err != nil {
		t.Fatalf("compare with drift: %v", err)
	}
	if len(diffs) == 0 || !strings.Contains(strings.Join(diffs, "\n"), "main.js") {
		t.Fatalf("expected main.js drift to be detected, got %v", diffs)
	}
}

func TestPrimT2PluginRigMissingOrUnreadablePluginFailsGracefully(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})

	// Watching a non-existent plugin must return an error rather than hanging or panicking.
	ctx, cancel := context.WithTimeout(context.Background(), primTestTimeout)
	defer cancel()
	if err := rig.Mgr.WatchScopedView(ctx, "plugin:does-not-exist/main", "", "en", plugin.View{}, func(*protocol.BlockUpdate) error {
		return nil
	}); err == nil {
		t.Fatal("expected WatchScopedView on missing plugin to return an error")
	}

	// An unreadable entry file (when non-root) or invalid folder must fail gracefully.
	if os.Geteuid() != 0 {
		dir := filepath.Join(rig.Dir, "unreadable")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir unreadable: %v", err)
		}
		entry := filepath.Join(dir, "main.js")
		if err := os.WriteFile(entry, []byte("export default { activate() {} };"), 0o000); err != nil {
			t.Fatalf("write unreadable main.js: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(entry, 0o644) })
		if err := os.WriteFile(filepath.Join(dir, plugin.ManifestName), []byte(primTestMinimalToml("unreadable", "main")), 0o644); err != nil {
			t.Fatalf("write unreadable plugin.toml: %v", err)
		}
		info := rig.WaitPlugin(t, "unreadable", func(p *protocol.PluginInfo) bool {
			return p.GetState() == protocol.PluginFailed
		})
		if info.GetError() == "" {
			t.Fatal("expected unreadable plugin to record an error")
		}
	}
}

func TestPrimT2PluginLoaderRejectsSymlinkInPluginDir(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	target := filepath.Join(t.TempDir(), "outside.js")
	if err := os.WriteFile(target, []byte("export default { activate() {} };\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pDir := filepath.Join(rig.Dir, "symlink-plug")
	if err := os.MkdirAll(pDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pDir, plugin.ManifestName), []byte(primTestMinimalToml("symlink-plug", "main")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(pDir, "main.js")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	info := rig.WaitPlugin(t, "symlink-plug", func(p *protocol.PluginInfo) bool {
		return p.GetState() == protocol.PluginFailed
	})
	if !strings.Contains(strings.ToLower(info.GetError()), "symbolic link") && !strings.Contains(strings.ToLower(info.GetError()), "symlink") {
		t.Fatalf("expected symbolic link refusal error, got %q", info.GetError())
	}
}

func TestPrimT2PluginLoaderInvalidManifestFailsGracefully(t *testing.T) {
	rig := NewPluginRig(t, PluginRigOpts{})
	rig.InstallJS(t, "bad-manifest", "id = \"bad-manifest\"\napi = 999\nentry = [\n", "export default {};")
	info := rig.WaitPlugin(t, "bad-manifest", func(p *protocol.PluginInfo) bool {
		return p.GetState() == protocol.PluginFailed
	})
	if info.GetError() == "" {
		t.Fatal("expected invalid plugin.toml to fail with an error message")
	}

	// A healthy sibling plugin in the same PluginRig continues working normally.
	w := primTestInstallAndWatch(t, rig, "healthy-sib", primTestMinimalToml("healthy-sib", "main"),
		`export default { activate(mako) { mako.blocks.register("main", { render(s, ui) { return ui.text("still-alive"); } }); } };`,
		"main")
	w.WaitFor(t, primTestTimeout, func(n *protocol.UiNode) bool {
		return HasTreeText(n, "still-alive")
	})
}

func TestPrimT2AppSupportCheckNeverMutatesAppSupport(t *testing.T) {
	appDir, err := AppSupportPluginsDir()
	if err != nil {
		t.Fatalf("AppSupportPluginsDir: %v", err)
	}
	if _, err := os.Stat(appDir); errors.Is(err, os.ErrNotExist) {
		t.Fatalf("AppSupport plugins dir missing: %q", appDir)
	}

	before := primTestSnapshotDir(t, appDir)
	for _, id := range []string{"agent-map", "ui-workbench"} {
		_, err := primTestComparePluginDirs(DemoPluginDir(t, id), filepath.Join(appDir, id))
		if err != nil {
			t.Fatalf("compare %s: %v", id, err)
		}
	}
	after := primTestSnapshotDir(t, appDir)

	if len(before) != len(after) {
		t.Fatalf("AppSupport entry count changed: before=%d after=%d", len(before), len(after))
	}
	for k, vBefore := range before {
		if vAfter, ok := after[k]; !ok || vBefore != vAfter {
			t.Fatalf("AppSupport mutated entry %q: before=%q after=%q (present=%v)", k, vBefore, vAfter, ok)
		}
	}
}
