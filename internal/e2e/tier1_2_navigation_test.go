//go:build e2e

package e2e

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ironpark/ggfx"
	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/gui"
	"github.com/ironpark/mako/internal/protocol"
)

// navTestConfig returns an isolated config suitable for headless AppProbe tests
// wired to a DaemonRig socket.
func navTestConfig() *config.Config {
	cfg := config.Default()
	cfg.Discovery = config.Discovery{}
	cfg.P2P.Disabled = true
	return cfg
}

// navTestOpenAppProbe launches a headless GUI AppProbe connected to r's daemon
// and waits until the probe has rendered at least one frame.
func navTestOpenAppProbe(t *testing.T, r *DaemonRig) *gui.AppProbe {
	t.Helper()
	ap := gui.NewAppProbe(navTestConfig(), r.Daemon.Socket(), ggui.Sz(1280, 800))
	t.Cleanup(ap.Close)
	ap.Probe().Frame()
	return ap
}

// navTestSidebarManifest builds a minimal plugin.toml declaring one sidebar
// block with sessions:read permission.
func navTestSidebarManifest(id, blockID, title string) string {
	return fmt.Sprintf(`id = %q
version = "0.1.0"
publisher = "e2e-nav"
api = 1
entry = "main.js"
permissions = ["sessions:read"]

[[contributes.block]]
id = %q
title = %q
icon = "compass"
slots = ["sidebar"]
`, id, blockID, title)
}

// navTestInstallSidebarPlugin installs, grants, and waits until plugin id is
// ready/running on r.
func navTestInstallSidebarPlugin(t *testing.T, r *DaemonRig, id, blockID, title, js string) string {
	t.Helper()
	r.InstallPlugin(t, id, navTestSidebarManifest(id, blockID, title), js)
	r.Grant(t, id)
	r.WaitPlugin(t, id, func(p *protocol.PluginInfo) bool {
		st := p.GetState()
		return st == protocol.PluginRunning || st == protocol.PluginReady
	})
	return "plugin:" + id + "/" + blockID
}

// navTestWait pumps AppProbe frames until pred returns true or timeout expires.
func navTestWait(t *testing.T, ap *gui.AppProbe, timeout time.Duration, desc string, pred func() bool) {
	t.Helper()
	if timeout <= 0 {
		timeout = defaultWaitTimeout
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ap.Probe().Frame()
		if pred() {
			return
		}
		time.Sleep(defaultPollInterval)
	}
	t.Fatalf("timed out after %v waiting for %s (sidebarPage=%q selected=%q)\nsemantics:\n%s",
		timeout, desc, ap.SidebarPage(), ap.SelectedSession(), ap.Probe().Semantics().String())
}

// navTestWaitSemantics waits until the AppProbe semantics tree contains substr.
func navTestWaitSemantics(t *testing.T, ap *gui.AppProbe, substr string) {
	t.Helper()
	navTestWait(t, ap, defaultWaitTimeout, fmt.Sprintf("semantics containing %q", substr), func() bool {
		return strings.Contains(ap.Probe().Semantics().String(), substr)
	})
}

// navTestFocusGraph locates the RoleGroup semantic node named caption and
// clicks near its top-left margin (outside any graph node circle) so the
// graph canvas receives keyboard focus without altering node selection.
func navTestFocusGraph(t *testing.T, p *ggui.Probe, caption string) ggui.Found {
	t.Helper()
	grp, ok := p.FindRole(ggui.RoleGroup, caption)
	if !ok {
		t.Fatalf("graph group %q not found in semantics:\n%s", caption, p.Semantics().String())
	}
	if grp.Rect.Size.W < 40 || grp.Rect.Size.H < 40 {
		t.Fatalf("graph group %q has unexpectedly small rect: %+v", caption, grp.Rect)
	}
	p.Click(ggui.Pt(grp.Rect.Origin.X+10, grp.Rect.Origin.Y+10))
	p.Frame()
	return grp
}

// navTestExtractSelected parses "Selected:<id>" from the semantics output.
func navTestExtractSelected(sem string) string {
	const prefix = "Selected:"
	idx := strings.Index(sem, prefix)
	if idx < 0 {
		return ""
	}
	rest := sem[idx+len(prefix):]
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		if c == '\n' || c == '\r' || c == '"' || c == ' ' || c == '\t' {
			return rest[:i]
		}
	}
	return rest
}

// -----------------------------------------------------------------------------
// Tier 1: F1.10 Feature Coverage (workspaceCenterView Crossfade & Shortcuts)
// -----------------------------------------------------------------------------

func TestNavigationTier1CrossfadeToSidebarPage(t *testing.T) {
	r := NewDaemonRig(t, DaemonRigOpts{})
	_ = r.OpenShellSession(t, "")

	const pluginID = "nav-crossfade"
	const blockID = "view"
	const menuTitle = "Crossfade View"
	js := `export default {
  activate(mako) {
    mako.blocks.register("view", {
      state: {},
      render(_state, ui) {
        return ui.column([
          ui.text("Crossfade Plugin Active", { key: "banner", variant: "heading" }),
          ui.text("Interactive workspace replacement", { key: "sub" }),
        ], { fill: true, gap: 8 });
      },
    });
  },
};`
	blockName := navTestInstallSidebarPlugin(t, r, pluginID, blockID, menuTitle, js)

	ap := navTestOpenAppProbe(t, r)
	p := ap.Probe()
	img := ggfx.NewImage(1280, 800)

	navTestWaitSemantics(t, ap, menuTitle)
	if got := ap.SidebarPage(); got != "" {
		t.Fatalf("initial SidebarPage = %q, want empty", got)
	}

	ap.OpenSidebarPage(blockName)
	if got := ap.SidebarPage(); got != blockName {
		t.Fatalf("SidebarPage = %q, want %q", got, blockName)
	}

	// Step through intermediate crossfade frames (30ms..150ms) and verify
	// non-zero layout, valid paint, and no visual blanking or panic.
	var sawPluginBody bool
	for step := 1; step <= 5; step++ {
		p.Advance(30 * time.Millisecond)
		sz := p.Draw(img)
		if sz.W <= 0 || sz.H <= 0 {
			t.Fatalf("step %d (%dms): blank/zero frame size %+v", step, step*30, sz)
		}
		sem := p.Semantics().String()
		if strings.TrimSpace(sem) == "" {
			t.Fatalf("step %d (%dms): semantics tree became empty during crossfade", step, step*30)
		}
		if strings.Contains(sem, "Crossfade Plugin Active") {
			sawPluginBody = true
		}
	}
	if !sawPluginBody {
		navTestWaitSemantics(t, ap, "Crossfade Plugin Active")
	}
	if got := ap.SidebarPage(); got != blockName {
		t.Fatalf("post-crossfade SidebarPage = %q, want %q", got, blockName)
	}
}

func TestNavigationTier1EscapeClosesSidebarPage(t *testing.T) {
	r := NewDaemonRig(t, DaemonRigOpts{})

	const pluginID = "nav-esc"
	const blockID = "view"
	const menuTitle = "Escape View"
	js := `export default {
  activate(mako) {
    mako.blocks.register("view", {
      state: {},
      render(_state, ui) {
        return ui.column([
          ui.text("Escape View Body", { key: "body" }),
        ], { fill: true, gap: 8 });
      },
    });
  },
};`
	blockName := navTestInstallSidebarPlugin(t, r, pluginID, blockID, menuTitle, js)

	ap := navTestOpenAppProbe(t, r)
	p := ap.Probe()

	navTestWaitSemantics(t, ap, menuTitle)
	ap.OpenSidebarPage(blockName)
	navTestWaitSemantics(t, ap, "Escape View Body")
	if got := ap.SidebarPage(); got != blockName {
		t.Fatalf("SidebarPage = %q, want %q", got, blockName)
	}

	p.Key("escape")
	p.Advance(150 * time.Millisecond)

	if got := ap.SidebarPage(); got != "" {
		t.Fatalf("after Esc, SidebarPage = %q, want empty", got)
	}
	if strings.Contains(p.Semantics().String(), "Escape View Body") {
		t.Fatalf("plugin body remained visible after Esc:\n%s", p.Semantics().String())
	}
}

func TestNavigationTier1ArrowKeysCycleGraphNodes(t *testing.T) {
	t.Run("daemon_plugin_app_probe_pipeline", func(t *testing.T) {
		r := NewDaemonRig(t, DaemonRigOpts{})

		const pluginID = "nav-arrows"
		const blockID = "view"
		const menuTitle = "Arrow Graph View"
		js := `export default {
  activate(mako) {
    mako.blocks.register("view", {
      state: { selected: "" },
      render(state, ui) {
        return ui.column([
          ui.text("Selected:" + (state.selected || "none"), { key: "sel" }),
          ui.graph(
            [
              { id: "node-a", label: "Node A", tone: "accent" },
              { id: "node-b", label: "Node B", tone: "success" },
              { id: "node-c", label: "Node C", tone: "warning" },
            ],
            [
              { from: "node-a", to: "node-b" },
              { from: "node-b", to: "node-c" },
            ],
            {
              key: "topo",
              caption: "Arrow Graph",
              value: state.selected,
              onChange: ["selectNode"],
            },
          ),
        ], { fill: true, gap: 8 });
      },
      on: {
        selectNode(state, nodeId) {
          state.selected = String(nodeId ?? "");
        },
      },
    });
  },
};`
		blockName := navTestInstallSidebarPlugin(t, r, pluginID, blockID, menuTitle, js)

		ap := navTestOpenAppProbe(t, r)
		p := ap.Probe()

		navTestWaitSemantics(t, ap, menuTitle)
		ap.OpenSidebarPage(blockName)
		navTestWaitSemantics(t, ap, "Selected:none")
		navTestFocusGraph(t, p, "Arrow Graph")

		valid := map[string]bool{"node-a": true, "node-b": true, "node-c": true}
		waitSelectedChange := func(prev string, desc string) string {
			var next string
			navTestWait(t, ap, defaultWaitTimeout, desc, func() bool {
				cur := navTestExtractSelected(p.Semantics().String())
				if cur != "" && cur != "none" && cur != prev && valid[cur] {
					next = cur
					return true
				}
				return false
			})
			return next
		}

		// Step forward twice (Right, Down -> 0 -> 1 -> 2), then backward twice (Left, Up -> 2 -> 1 -> 0).
		p.Type(ggui.Mods{}, ggui.KeyArrowRight)
		s1 := waitSelectedChange("none", "ArrowRight selection")

		p.Type(ggui.Mods{}, ggui.KeyArrowDown)
		s2 := waitSelectedChange(s1, "ArrowDown selection")

		p.Type(ggui.Mods{}, ggui.KeyArrowDown)
		s3 := waitSelectedChange(s2, "second ArrowDown selection")
		if s3 == s1 {
			t.Fatalf("expected 3 distinct selections across Right/Down/Down, got s1=%q s2=%q s3=%q", s1, s2, s3)
		}

		p.Type(ggui.Mods{}, ggui.KeyArrowLeft)
		navTestWait(t, ap, defaultWaitTimeout, "ArrowLeft returning to "+s2, func() bool {
			return navTestExtractSelected(p.Semantics().String()) == s2
		})

		p.Type(ggui.Mods{}, ggui.KeyArrowUp)
		navTestWait(t, ap, defaultWaitTimeout, "ArrowUp returning to "+s1, func() bool {
			return navTestExtractSelected(p.Semantics().String()) == s1
		})
	})

	t.Run("ui_tree_probe_direct_arrow_events", func(t *testing.T) {
		var mu sync.Mutex
		var events []string
		tree := &protocol.UiNode{
			Id:   "",
			Kind: "column",
			Children: []*protocol.UiNode{
				{
					Id:     "/g",
					Kind:   "graph",
					Text:   "Direct Graph",
					Value:  "n1",
					Change: true,
					GraphNodes: []*protocol.UiGraphNode{
						{Id: "n1", Label: "One"},
						{Id: "n2", Label: "Two"},
						{Id: "n3", Label: "Three"},
					},
					GraphEdges: []*protocol.UiGraphEdge{
						{From: "n1", To: "n2"},
						{From: "n2", To: "n3"},
					},
				},
			},
		}
		p := gui.NewUiTreeProbe(tree, func(id, kind, value string) {
			mu.Lock()
			defer mu.Unlock()
			if id == "/g" && kind == protocol.UiChange {
				events = append(events, value)
			}
		}, ggui.Sz(640, 480))
		defer p.Close()
		p.Frame()

		navTestFocusGraph(t, p, "Direct Graph")
		p.Type(ggui.Mods{}, ggui.KeyArrowRight)
		p.Type(ggui.Mods{}, ggui.KeyArrowDown)
		p.Type(ggui.Mods{}, ggui.KeyArrowLeft)
		p.Type(ggui.Mods{}, ggui.KeyArrowUp)
		p.Frame()

		mu.Lock()
		got := append([]string(nil), events...)
		mu.Unlock()
		if len(got) != 4 {
			t.Fatalf("expected 4 onChange events from Right/Down/Left/Up, got %d: %+v", len(got), got)
		}
	})
}

func TestNavigationTier1EnterOpensSelectedGraphSession(t *testing.T) {
	r := NewDaemonRig(t, DaemonRigOpts{})
	sess := r.OpenShellSession(t, "")
	sid := sess.GetId()

	const pluginID = "nav-enter"
	const blockID = "view"
	const menuTitle = "Enter Session Map"
	js := fmt.Sprintf(`export default {
  activate(mako) {
    mako.blocks.register("view", {
      state: { selected: "" },
      render(state, ui) {
        return ui.column([
          ui.text("Selected:" + (state.selected || "none"), { key: "sel" }),
          ui.graph(
            [
              { id: %q, label: "Live Shell", tone: "success" },
            ],
            [],
            {
              key: "topo",
              caption: "Enter Graph",
              value: state.selected,
              onChange: ["selectNode"],
              onDoubleTap: ["openNode"],
            },
          ),
        ], { fill: true, gap: 8 });
      },
      on: {
        selectNode(state, nodeId) {
          state.selected = String(nodeId ?? "");
        },
        async openNode(state, nodeId) {
          const ctx = mako.blocks.context("view");
          const target = String(nodeId || state.selected || "");
          if (target) {
            await ctx.ui.openSession(target);
          }
        },
      },
    });
  },
};`, sid)
	blockName := navTestInstallSidebarPlugin(t, r, pluginID, blockID, menuTitle, js)

	ap := navTestOpenAppProbe(t, r)
	p := ap.Probe()

	navTestWaitSemantics(t, ap, menuTitle)
	ap.OpenSidebarPage(blockName)
	navTestWaitSemantics(t, ap, "Selected:none")
	navTestFocusGraph(t, p, "Enter Graph")

	// Select the session node using ArrowRight, then press Enter to open it.
	p.Type(ggui.Mods{}, ggui.KeyArrowRight)
	navTestWaitSemantics(t, ap, "Selected:"+sid)

	p.Type(ggui.Mods{}, ggui.KeyEnter)
	navTestWait(t, ap, defaultWaitTimeout, "Enter opening selected session", func() bool {
		return ap.SelectedSession() == sid && ap.SidebarPage() == ""
	})
}

func TestNavigationTier1OpenSessionIntentClearsSidebarPageWithCrossfade(t *testing.T) {
	r := NewDaemonRig(t, DaemonRigOpts{})
	sess := r.OpenShellSession(t, "")
	sid := sess.GetId()

	const pluginID = "nav-jump"
	const blockID = "view"
	const menuTitle = "Session Jump View"
	js := fmt.Sprintf(`export default {
  activate(mako) {
    mako.blocks.register("view", {
      state: {},
      render(_state, ui) {
        return ui.column([
          ui.text("Jump Surface Ready", { key: "ready" }),
          ui.button("Jump To Session", {
            key: "btn-jump",
            variant: "primary",
            onTap: ["jump"],
          }),
        ], { fill: true, gap: 8 });
      },
      on: {
        async jump() {
          const ctx = mako.blocks.context("view");
          await ctx.ui.openSession(%q);
        },
      },
    });
  },
};`, sid)
	blockName := navTestInstallSidebarPlugin(t, r, pluginID, blockID, menuTitle, js)

	ap := navTestOpenAppProbe(t, r)
	p := ap.Probe()
	img := ggfx.NewImage(1280, 800)

	navTestWaitSemantics(t, ap, menuTitle)
	ap.OpenSidebarPage(blockName)
	navTestWaitSemantics(t, ap, "Jump To Session")

	p.Tap("Jump To Session")
	navTestWait(t, ap, defaultWaitTimeout, "openSession clearing SidebarPage", func() bool {
		return ap.SidebarPage() == "" && ap.SelectedSession() == sid
	})

	// Step through return crossfade frames cleanly without blanking or panic.
	for step := 1; step <= 5; step++ {
		p.Advance(30 * time.Millisecond)
		sz := p.Draw(img)
		if sz.W <= 0 || sz.H <= 0 {
			t.Fatalf("return step %d: invalid frame size %+v", step, sz)
		}
		if strings.TrimSpace(p.Semantics().String()) == "" {
			t.Fatalf("return step %d: empty semantics during return crossfade", step)
		}
	}
	if got := ap.SidebarPage(); got != "" {
		t.Fatalf("post-crossfade SidebarPage = %q, want empty", got)
	}
	if got := ap.SelectedSession(); got != sid {
		t.Fatalf("SelectedSession = %q, want %q", got, sid)
	}
}

// -----------------------------------------------------------------------------
// Tier 2: F1.10 Boundary & Negative Cases
// -----------------------------------------------------------------------------

func TestNavigationTier2EscapeCapturedByInputAndDialog(t *testing.T) {
	r := NewDaemonRig(t, DaemonRigOpts{})

	const pluginID = "nav-esc-guard"
	const blockID = "view"
	const menuTitle = "Escape Guard View"
	js := `export default {
  activate(mako) {
    mako.blocks.register("view", {
      state: { q: "", note: "", modal: false },
      render(state, ui) {
        const children = [
          ui.text("Escape Guard Active", { key: "hdr" }),
          ui.input(state.q, {
            key: "search-box",
            variant: "search",
            placeholder: "Search nodes...",
            onChange: ["setQ"],
          }),
          ui.input(state.note, {
            key: "note-box",
            placeholder: "Filter note...",
            onChange: ["setNote"],
          }),
          ui.button("Open Modal", {
            key: "open-modal",
            onTap: ["openModal"],
          }),
        ];
        if (state.modal) {
          children.push(ui.dialog("Confirm Action", [
            ui.text("Modal Body Text", { key: "mbody" }),
            ui.button("Done", { key: "mdone", onTap: ["closeModal"] }),
          ], { key: "dlg", onChange: ["onDlg"] }));
        }
        return ui.column(children, { fill: true, gap: 8 });
      },
      on: {
        setQ(state, v) { state.q = String(v ?? ""); },
        setNote(state, v) { state.note = String(v ?? ""); },
        openModal(state) { state.modal = true; },
        closeModal(state) { state.modal = false; },
        onDlg(state, v) {
          if (v === "dismiss") state.modal = false;
        },
      },
    });
  },
};`
	blockName := navTestInstallSidebarPlugin(t, r, pluginID, blockID, menuTitle, js)

	ap := navTestOpenAppProbe(t, r)
	p := ap.Probe()

	navTestWaitSemantics(t, ap, menuTitle)
	ap.OpenSidebarPage(blockName)
	navTestWaitSemantics(t, ap, "Search nodes...")

	t.Run("search_box_focused", func(t *testing.T) {
		field, ok := p.FindRole(ggui.RoleTextField, "Search nodes...")
		if !ok {
			t.Fatalf("search box not found:\n%s", p.Semantics().String())
		}
		p.Click(field.Center())
		p.Text("abc")
		p.Key("escape")
		p.Advance(150 * time.Millisecond)

		if got := ap.SidebarPage(); got != blockName {
			t.Fatalf("Esc with focused search box closed SidebarPage: got %q, want %q", got, blockName)
		}
	})

	t.Run("text_input_focused", func(t *testing.T) {
		field, ok := p.FindRole(ggui.RoleTextField, "Filter note...")
		if !ok {
			t.Fatalf("text input not found:\n%s", p.Semantics().String())
		}
		p.Click(field.Center())
		p.Key("escape")
		p.Advance(150 * time.Millisecond)

		if got := ap.SidebarPage(); got != blockName {
			t.Fatalf("Esc with focused text input closed SidebarPage: got %q, want %q", got, blockName)
		}
	})

	t.Run("modal_dialog_captures_first_escape", func(t *testing.T) {
		p.Tap("Open Modal")
		navTestWait(t, ap, defaultWaitTimeout, "modal dialog open", func() bool {
			_, ok := p.Semantics().Find(ggui.RoleDialog, "")
			return ok
		})

		// First Esc closes the modal dialog without leaving the sidebar plugin page.
		p.Key("escape")
		navTestWait(t, ap, defaultWaitTimeout, "modal dialog dismissed", func() bool {
			_, ok := p.Semantics().Find(ggui.RoleDialog, "")
			return !ok
		})
		p.Advance(150 * time.Millisecond)
		if got := ap.SidebarPage(); got != blockName {
			t.Fatalf("first Esc while modal dialog open closed SidebarPage: got %q, want %q", got, blockName)
		}

		// Ensure no text field retains focus, then second Esc returns to workspace.
		if hdr, ok := p.Semantics().Find(ggui.RoleText, "Escape Guard Active"); ok {
			p.Click(ggui.Pt(hdr.Rect.Origin.X+hdr.Rect.Size.W/2, hdr.Rect.Origin.Y+hdr.Rect.Size.H/2))
		}
		p.Key("escape")
		p.Advance(150 * time.Millisecond)
		if got := ap.SidebarPage(); got != "" {
			t.Fatalf("second Esc after dialog closed left SidebarPage = %q, want empty", got)
		}
	})
}

func TestNavigationTier2EscapeInWorkspaceIsNoOp(t *testing.T) {
	r := NewDaemonRig(t, DaemonRigOpts{})
	_ = r.OpenShellSession(t, "")

	const pluginID = "nav-noop"
	const blockID = "view"
	const menuTitle = "Noop View"
	js := `export default {
  activate(mako) {
    mako.blocks.register("view", {
      state: {},
      render(_state, ui) {
        return ui.text("Noop");
      },
    });
  },
};`
	_ = navTestInstallSidebarPlugin(t, r, pluginID, blockID, menuTitle, js)

	ap := navTestOpenAppProbe(t, r)
	p := ap.Probe()

	navTestWaitSemantics(t, ap, menuTitle)
	if got := ap.SidebarPage(); got != "" {
		t.Fatalf("initial SidebarPage = %q, want empty", got)
	}
	beforeSess := ap.SelectedSession()

	for range 3 {
		p.Key("escape")
		p.Advance(50 * time.Millisecond)
	}

	if got := ap.SidebarPage(); got != "" {
		t.Fatalf("Esc in workspace set SidebarPage = %q, want empty", got)
	}
	if got := ap.SelectedSession(); got != beforeSess {
		t.Fatalf("Esc in workspace changed SelectedSession from %q to %q", beforeSess, got)
	}
}

func TestNavigationTier2EmptyGraphArrowsAndEnterSafe(t *testing.T) {
	t.Run("app_probe_empty_graph", func(t *testing.T) {
		r := NewDaemonRig(t, DaemonRigOpts{})

		const pluginID = "nav-empty-graph"
		const blockID = "view"
		const menuTitle = "Empty Graph View"
		js := `export default {
  activate(mako) {
    mako.blocks.register("view", {
      state: { events: 0 },
      render(state, ui) {
        return ui.column([
          ui.text("events:" + state.events, { key: "cnt" }),
          ui.graph([], [], {
            key: "empty",
            caption: "Empty Graph",
            onChange: ["bump"],
            onDoubleTap: ["bump"],
          }),
        ], { fill: true, gap: 8 });
      },
      on: {
        bump(state) { state.events += 1; },
      },
    });
  },
};`
		blockName := navTestInstallSidebarPlugin(t, r, pluginID, blockID, menuTitle, js)

		ap := navTestOpenAppProbe(t, r)
		p := ap.Probe()

		navTestWaitSemantics(t, ap, menuTitle)
		ap.OpenSidebarPage(blockName)
		navTestWaitSemantics(t, ap, "events:0")
		navTestFocusGraph(t, p, "Empty Graph")

		p.Type(ggui.Mods{}, ggui.KeyArrowRight, ggui.KeyArrowDown, ggui.KeyArrowLeft, ggui.KeyArrowUp, ggui.KeyEnter)
		p.Advance(150 * time.Millisecond)

		if got := ap.SidebarPage(); got != blockName {
			t.Fatalf("SidebarPage = %q, want %q", got, blockName)
		}
		if got := ap.SelectedSession(); got != "" {
			t.Fatalf("SelectedSession = %q, want empty", got)
		}
		if !strings.Contains(p.Semantics().String(), "events:0") {
			t.Fatalf("empty graph emitted unexpected events:\n%s", p.Semantics().String())
		}
	})

	t.Run("ui_tree_probe_empty_graph", func(t *testing.T) {
		var mu sync.Mutex
		var count int
		tree := &protocol.UiNode{
			Id:   "",
			Kind: "column",
			Children: []*protocol.UiNode{
				{
					Id:        "/empty",
					Kind:      "graph",
					Text:      "Zero Node Graph",
					Change:    true,
					DoubleTap: true,
				},
			},
		}
		p := gui.NewUiTreeProbe(tree, func(_, _, _ string) {
			mu.Lock()
			count++
			mu.Unlock()
		}, ggui.Sz(500, 400))
		defer p.Close()
		p.Frame()

		navTestFocusGraph(t, p, "Zero Node Graph")
		p.Type(ggui.Mods{}, ggui.KeyArrowRight, ggui.KeyArrowDown, ggui.KeyArrowLeft, ggui.KeyArrowUp, ggui.KeyEnter)
		p.Frame()

		mu.Lock()
		got := count
		mu.Unlock()
		if got != 0 {
			t.Fatalf("empty graph emitted %d events, want 0", got)
		}
	})
}

func TestNavigationTier2EnterWithNoSelectionDoesNotOpenSession(t *testing.T) {
	r := NewDaemonRig(t, DaemonRigOpts{})
	sess := r.OpenShellSession(t, "")
	sid := sess.GetId()

	const pluginID = "nav-nosel"
	const blockID = "view"
	const menuTitle = "Unselected Graph View"
	js := fmt.Sprintf(`export default {
  activate(mako) {
    mako.blocks.register("view", {
      state: { selected: "", dblCount: 0 },
      render(state, ui) {
        return ui.column([
          ui.text("Selected:" + (state.selected || "none"), { key: "sel" }),
          ui.text("dblCount:" + state.dblCount, { key: "dbl" }),
          ui.graph(
            [
              { id: %q, label: "Session A", tone: "accent" },
              { id: "other-node", label: "Node B", tone: "muted" },
            ],
            [
              { from: %q, to: "other-node" },
            ],
            {
              key: "g",
              caption: "Unselected Graph",
              value: state.selected,
              onChange: ["onSel"],
              onDoubleTap: ["onDbl"],
            },
          ),
        ], { fill: true, gap: 8 });
      },
      on: {
        onSel(state, v) { state.selected = String(v ?? ""); },
        async onDbl(state, v) {
          state.dblCount += 1;
          const ctx = mako.blocks.context("view");
          if (v) {
            await ctx.ui.openSession(String(v));
          }
        },
      },
    });
  },
};`, sid, sid)
	blockName := navTestInstallSidebarPlugin(t, r, pluginID, blockID, menuTitle, js)

	ap := navTestOpenAppProbe(t, r)
	p := ap.Probe()

	navTestWaitSemantics(t, ap, menuTitle)
	ap.OpenSidebarPage(blockName)
	navTestWaitSemantics(t, ap, "dblCount:0")
	navTestFocusGraph(t, p, "Unselected Graph")

	if !strings.Contains(p.Semantics().String(), "Selected:none") {
		t.Fatalf("clicking canvas margin unexpectedly selected a node:\n%s", p.Semantics().String())
	}

	p.Type(ggui.Mods{}, ggui.KeyEnter)
	p.Advance(150 * time.Millisecond)

	if got := ap.SelectedSession(); got != "" {
		t.Fatalf("Enter with no selected node opened session %q, want empty", got)
	}
	if got := ap.SidebarPage(); got != blockName {
		t.Fatalf("Enter with no selected node changed SidebarPage to %q, want %q", got, blockName)
	}
	if !strings.Contains(p.Semantics().String(), "dblCount:0") {
		t.Fatalf("Enter with no selected node fired onDoubleTap:\n%s", p.Semantics().String())
	}
}

func TestNavigationTier2RapidCrossfadeToggleConsistency(t *testing.T) {
	r := NewDaemonRig(t, DaemonRigOpts{})

	const pluginID = "nav-rapid"
	const blockID = "view"
	const menuTitle = "Rapid Toggle View"
	js := `export default {
  activate(mako) {
    mako.blocks.register("view", {
      state: {},
      render(_state, ui) {
        return ui.column([
          ui.text("Rapid Page Active", { key: "rapid-marker" }),
        ], { fill: true, gap: 8 });
      },
    });
  },
};`
	blockName := navTestInstallSidebarPlugin(t, r, pluginID, blockID, menuTitle, js)

	ap := navTestOpenAppProbe(t, r)
	p := ap.Probe()

	navTestWaitSemantics(t, ap, menuTitle)

	// Burst A: rapidly alternate OpenSidebarPage <-> Esc mid-crossfade, ending on Esc.
	for range 6 {
		ap.OpenSidebarPage(blockName)
		p.Advance(20 * time.Millisecond)
		p.Key("escape")
		p.Advance(20 * time.Millisecond)
	}
	p.Advance(200 * time.Millisecond)

	if got := ap.SidebarPage(); got != "" {
		t.Fatalf("after burst ending on Esc, SidebarPage = %q, want empty", got)
	}
	if strings.Contains(p.Semantics().String(), "Rapid Page Active") {
		t.Fatalf("stale plugin view remained after rapid toggle ending on Esc:\n%s", p.Semantics().String())
	}

	// Burst B: rapidly alternate OpenSidebarPage <-> Esc mid-crossfade, ending on OpenSidebarPage.
	for range 5 {
		ap.OpenSidebarPage(blockName)
		p.Advance(20 * time.Millisecond)
		p.Key("escape")
		p.Advance(20 * time.Millisecond)
	}
	ap.OpenSidebarPage(blockName)
	p.Advance(200 * time.Millisecond)

	if got := ap.SidebarPage(); got != blockName {
		t.Fatalf("after burst ending on OpenSidebarPage, SidebarPage = %q, want %q", got, blockName)
	}
	navTestWaitSemantics(t, ap, "Rapid Page Active")
}
