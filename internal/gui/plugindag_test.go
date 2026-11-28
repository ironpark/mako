package gui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

func TestDagGraphBranchesAndMerges(t *testing.T) {
	r := func(id string, parents ...string) *protocol.UiRow {
		return &protocol.UiRow{Id: id, Parents: parents}
	}
	// m merges b into a line from a; d is a second root's tip.
	rows := dagGraph([]*protocol.UiRow{r("m", "a", "b"), r("d", "x"), r("b", "a"), r("a"), r("x")})
	want := []dagGraphRow{
		{Col: 0, Lanes: 2, Lines: []dagGraphLine{{0, 0, true}, {0, 1, true}}},
		{Col: 2, Lanes: 3, Lines: []dagGraphLine{{0, 0, false}, {0, 0, true}, {1, 1, false}, {1, 1, true}, {2, 2, true}}},
		{Col: 1, Lanes: 3, Lines: []dagGraphLine{{0, 0, false}, {0, 0, true}, {1, 1, false}, {2, 2, false}, {2, 2, true}, {1, 0, true}}},
		{Col: 0, Lanes: 3, Lines: []dagGraphLine{{0, 0, false}, {2, 2, false}, {2, 2, true}}},
		{Col: 2, Lanes: 3, Lines: []dagGraphLine{{2, 2, false}}},
	}
	if len(rows) != len(want) {
		t.Fatalf("len(rows) = %d, want %d", len(rows), len(want))
	}
	for i := range want {
		if rows[i].Col != want[i].Col || rows[i].Lanes != want[i].Lanes || !slices.Equal(rows[i].Lines, want[i].Lines) {
			t.Errorf("row %d = %+v\nwant %+v", i, rows[i], want[i])
		}
	}
	if got := dagGraphWidth(rows); got != 3 {
		t.Fatalf("dagGraphWidth = %d, want 3", got)
	}
}

func TestPluginDagVirtualization(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	rows := make([]*protocol.UiRow, 0, 500)
	for i := range 500 {
		var parents []string
		if i < 499 {
			parents = []string{fmt.Sprintf("%040d", i+1)}
		}
		rows = append(rows, &protocol.UiRow{
			Id:      fmt.Sprintf("%040d", i),
			Parents: parents,
			Text:    fmt.Sprintf("commit %d", i),
			Detail:  "alice · 2m",
			Meta:    fmt.Sprintf("%07d", i),
			Strong:  i == 0,
			Badges:  []*protocol.UiBadge{{Text: "main", Tone: "accent", Icon: "git-branch"}},
		})
	}
	tree := &protocol.UiNode{
		Id:     "/dag",
		Kind:   "dag",
		Change: true,
		Rows:   rows,
	}
	form := newBlockForm()
	v := newTreeView(m, "plugin:mako-git/panel", "", form, tree)

	p := ggui.ProbeBuilder(func() ggui.Widget { return v.node(tree, false) }, ggui.Sz(400, 600))
	defer p.Close()
	p.Frame()

	if _, ok := p.Semantics().Find(ggui.RoleButton, "commit 0"); !ok {
		t.Fatalf("first dag row not shown:\n%s", p.Semantics().String())
	}
	built := 0
	for i := range 500 {
		if _, ok := p.Semantics().Find(ggui.RoleButton, fmt.Sprintf("commit %d", i)); ok {
			built++
		}
	}
	if built > 60 {
		t.Fatalf("%d of 500 dag rows built in a 600px viewport (virtualization broken)", built)
	}
}

func TestPluginDagSelectAndContextMenu(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")

	type ev struct{ node, kind, val string }
	var got []ev

	tree := &protocol.UiNode{
		Id:   "",
		Kind: "column",
		Children: []*protocol.UiNode{
			{
				Id:      "/commit-menu",
				Kind:    "menu",
				Variant: "context",
				Children: []*protocol.UiNode{
					{Id: "/commit-menu/cherry", Kind: "menu_item", Text: "Cherry-pick", Tap: true},
					{Id: "/commit-menu/div", Kind: "divider"},
					{Id: "/commit-menu/drop", Kind: "menu_item", Text: "Drop", Tone: "danger", Tap: true},
				},
			},
			{
				Id:          "/history",
				Kind:        "dag",
				Change:      true,
				ContextMenu: "commit-menu",
				Grow:        1,
				Rows: []*protocol.UiRow{
					{Id: "abc1234", Parents: []string{"def5678"}, Text: "Initial feature", Detail: "bob · 1h", Meta: "abc1234", Strong: true},
					{Id: "def5678", Text: "Root commit", Detail: "alice · 2h", Meta: "def5678"},
				},
			},
		},
	}

	form := newBlockForm()
	v := newTreeView(m, "plugin:mako-git/panel", "", form, tree)
	v.onEvent = func(_, node, kind, value string, _ map[string]string) {
		got = append(got, ev{node, kind, value})
	}

	p := ggui.ProbeBuilder(func() ggui.Widget { return v.node(tree, false) }, ggui.Sz(400, 300))
	defer p.Close()
	p.Frame()

	// Tap the second row and verify UiChange fires with its row ID.
	second, ok := p.FindRole(ggui.RoleButton, "Root commit")
	if !ok {
		t.Fatalf("could not find Root commit:\n%s", p.Semantics().String())
	}
	p.Click(second.Center())
	p.Frame()
	if len(got) != 1 || got[0] != (ev{"/history", protocol.UiChange, "def5678"}) {
		t.Fatalf("tap row events = %+v, want UiChange def5678", got)
	}

	// Right-click the first row to open its context menu and trigger Cherry-pick.
	first, ok := p.FindRole(ggui.RoleButton, "Initial feature")
	if !ok {
		t.Fatalf("Initial feature row not found")
	}
	got = nil
	p.ClickButton(first.Center(), ggui.MouseButtonRight)
	p.Frame()
	item, ok := p.FindRole(ggui.RoleMenuItem, "Cherry-pick")
	if !ok {
		t.Fatalf("Cherry-pick menu item not found after right-click:\n%s", p.Semantics().String())
	}
	p.Click(item.Center())
	p.Frame()
	if len(got) != 1 || got[0] != (ev{"/commit-menu/cherry", protocol.UiTap, "abc1234"}) {
		t.Fatalf("context menu tap events = %+v, want UiTap abc1234", got)
	}
}

func TestPluginLiftToolbarAndRender(t *testing.T) {
	if tb, rest := liftToolbar(nil); tb != nil || rest != nil {
		t.Fatalf("liftToolbar(nil) = (%v, %v), want (nil, nil)", tb, rest)
	}

	plain := &protocol.UiNode{
		Id:   "",
		Kind: "column",
		Children: []*protocol.UiNode{
			{Id: "/r", Kind: "row", Children: []*protocol.UiNode{{Id: "/r/t", Kind: "text", Text: "plain"}}},
		},
	}
	if tb, rest := liftToolbar(plain); tb != nil || rest != plain {
		t.Fatalf("liftToolbar(plain) = (%v, %v), want (nil, plain)", tb, rest)
	}

	tree := &protocol.UiNode{
		Id:   "",
		Kind: "column",
		Fill: true,
		Children: []*protocol.UiNode{
			{
				Id:      "/bar",
				Kind:    "row",
				Variant: "toolbar",
				Children: []*protocol.UiNode{
					{Id: "/bar/pull", Kind: "button", Icon: "arrow-down", Text: "Pull", Variant: "ghost", Tap: true,
						Badges: []*protocol.UiBadge{{Text: "3", Variant: "mono", Tone: "muted"}}},
				},
			},
			{Id: "/body", Kind: "text", Text: "Body content"},
		},
	}
	tb, rest := liftToolbar(tree)
	if tb == nil || tb.GetId() != "/bar" {
		t.Fatalf("lifted toolbar = %v, want /bar", tb)
	}
	if rest == nil || rest == tree {
		t.Fatalf("expected shallow copy for rest")
	}
	if len(rest.GetChildren()) != 1 || rest.GetChildren()[0].GetId() != "/body" {
		t.Fatalf("rest children = %+v, want [/body]", rest.GetChildren())
	}
	if len(tree.GetChildren()) != 2 {
		t.Fatalf("original tree mutated: len(children) = %d", len(tree.GetChildren()))
	}
	if !rest.GetFill() {
		t.Fatalf("rest did not preserve Fill=true")
	}

	// Render the lifted toolbar via renderTreeNode and ensure it paints cleanly.
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	form := newBlockForm()
	p := ggui.ProbeBuilder(func() ggui.Widget {
		return renderTreeNode(m, "plugin:mako-git/panel", "", "mako-git", form, tree, tb)
	}, ggui.Sz(320, 40))
	defer p.Close()
	p.Frame()
	if _, ok := p.Semantics().Find(ggui.RoleButton, "Pull"); !ok {
		t.Fatalf("lifted toolbar Pull button not found:\n%s", p.Semantics().String())
	}
}

func TestPluginAutoSplitBreakpoint(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	tree := &protocol.UiNode{
		Id:        "/sp",
		Kind:      "split",
		Direction: "auto",
		Ratio:     0.5,
		Children: []*protocol.UiNode{
			{Id: "/sp/a", Kind: "button", Text: "Pane A", Tap: true},
			{Id: "/sp/b", Kind: "button", Text: "Pane B", Tap: true},
		},
	}

	// Narrow (<640): vertical — Pane B sits below Pane A.
	narrowForm := newBlockForm()
	vNarrow := newTreeView(m, "plugin:mako-git/panel", "", narrowForm, tree)
	pn := ggui.ProbeBuilder(func() ggui.Widget { return vNarrow.node(tree, false) }, ggui.Sz(500, 400))
	defer pn.Close()
	pn.Frame()
	aN, okA := pn.FindRole(ggui.RoleButton, "Pane A")
	bN, okB := pn.FindRole(ggui.RoleButton, "Pane B")
	if !okA || !okB {
		t.Fatalf("narrow panes missing:\n%s", pn.Semantics().String())
	}
	if bN.Rect.Origin.Y <= aN.Rect.Origin.Y {
		t.Fatalf("narrow auto split should stack vertically: A=%v B=%v", aN.Rect, bN.Rect)
	}

	// Wide (>=640): horizontal — Pane B sits to the right of Pane A.
	wideForm := newBlockForm()
	vWide := newTreeView(m, "plugin:mako-git/panel", "", wideForm, tree)
	pw := ggui.ProbeBuilder(func() ggui.Widget { return vWide.node(tree, false) }, ggui.Sz(700, 400))
	defer pw.Close()
	pw.Frame()
	aW, okA := pw.FindRole(ggui.RoleButton, "Pane A")
	bW, okB := pw.FindRole(ggui.RoleButton, "Pane B")
	if !okA || !okB {
		t.Fatalf("wide panes missing:\n%s", pw.Semantics().String())
	}
	if bW.Rect.Origin.X <= aW.Rect.Origin.X {
		t.Fatalf("wide auto split should sit side-by-side: A=%v B=%v", aW.Rect, bW.Rect)
	}
}

func TestPluginCheckboxTreeItemAndMenuEvents(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	type ev struct{ node, kind, val string }
	var got []ev

	tree := &protocol.UiNode{
		Id:   "",
		Kind: "column",
		Children: []*protocol.UiNode{
			{
				Id:   "/branch-menu",
				Kind: "menu",
				Icon: "git-branch",
				Text: "main",
				Children: []*protocol.UiNode{
					{Id: "/branch-menu/main", Kind: "menu_item", Text: "main", Checked: true, Tap: true},
					{Id: "/branch-menu/dev", Kind: "menu_item", Text: "dev", Tap: true},
				},
			},
			{
				Id:     "/amend",
				Kind:   "checkbox",
				Text:   "Amend",
				Check:  "off",
				Change: true,
			},
			{
				Id:   "/changes",
				Kind: "tree",
				Children: []*protocol.UiNode{
					{
						Id:     "/changes/readme",
						Kind:   "tree_item",
						Text:   "README.md",
						Icon:   "file-diff",
						Check:  "on",
						Checks: true,
						Tap:    true,
						Badges: []*protocol.UiBadge{
							{Text: "+12", Tone: "success", Variant: "mono"},
							{Text: "-3", Tone: "danger", Variant: "mono"},
						},
					},
				},
			},
		},
	}

	form := newBlockForm()
	v := newTreeView(m, "plugin:mako-git/panel", "", form, tree)
	v.onEvent = func(_, node, kind, value string, _ map[string]string) {
		got = append(got, ev{node, kind, value})
	}

	p := ggui.ProbeBuilder(func() ggui.Widget { return v.node(tree, false) }, ggui.Sz(400, 400))
	defer p.Close()
	p.Frame()

	// 1. Toggle standalone checkbox -> UiChange "on".
	amend, ok := p.FindRole(ggui.RoleCheckbox, "Amend")
	if !ok {
		t.Fatalf("Amend checkbox not found:\n%s", p.Semantics().String())
	}
	p.Click(amend.Center())
	p.Frame()
	if len(got) != 1 || got[0] != (ev{"/amend", protocol.UiChange, "on"}) {
		t.Fatalf("checkbox events = %+v, want UiChange on", got)
	}

	// 2. Toggle tree_item check -> "check" "off".
	got = nil
	chk, ok := p.FindRole(ggui.RoleCheckbox, "Select README.md")
	if !ok {
		t.Fatalf("tree_item checkbox not found:\n%s", p.Semantics().String())
	}
	p.Click(chk.Center())
	p.Frame()
	if len(got) != 1 || got[0] != (ev{"/changes/readme", uiCheckEvent, "off"}) {
		t.Fatalf("tree_item check events = %+v, want check off", got)
	}

	// 3. Open dropdown menu and select "dev" -> UiTap "".
	got = nil
	trig, ok := p.FindRole(ggui.RoleMenu, "main")
	if !ok {
		t.Fatalf("branch menu trigger not found:\n%s", p.Semantics().String())
	}
	p.Click(trig.Center())
	p.Frame()
	devItem, ok := p.FindRole(ggui.RoleMenuItem, "dev")
	if !ok {
		t.Fatalf("dev menu item not found:\n%s", p.Semantics().String())
	}
	p.Click(devItem.Center())
	p.Frame()
	if len(got) != 1 || got[0] != (ev{"/branch-menu/dev", protocol.UiTap, ""}) {
		t.Fatalf("menu item tap events = %+v, want UiTap /branch-menu/dev", got)
	}
}

func TestPluginDialogDismissAndProvenance(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.setPlugins([]*protocol.PluginInfo{
		{Id: "mako-git", Bundled: true},
		{Id: "acme", Bundled: false},
	})

	dlgTree := func() *protocol.UiNode {
		return &protocol.UiNode{
			Id:   "",
			Kind: "column",
			Children: []*protocol.UiNode{
				{Id: "/main", Kind: "text", Text: "Behind"},
				{
					Id:      "/confirm",
					Kind:    "dialog",
					Text:    "Discard changes?",
					Variant: "danger",
					Change:  true,
					Children: []*protocol.UiNode{
						{Id: "/confirm/msg", Kind: "text", Text: "This cannot be undone."},
						{Id: "/confirm/ok", Kind: "button", Text: "Discard", Variant: "danger", Tap: true},
					},
				},
			},
		}
	}

	// Third-party plugin dialog shows "<plugin id> asks" provenance and sends dismiss on Escape.
	type ev struct{ node, kind, val string }
	var got []ev
	t3 := dlgTree()
	v3 := newTreeView(m, "plugin:acme/panel", "", newBlockForm(), t3)
	v3.onEvent = func(_, node, kind, value string, _ map[string]string) {
		got = append(got, ev{node, kind, value})
	}
	p3 := ggui.ProbeBuilder(func() ggui.Widget { return v3.node(t3, false) }, ggui.Sz(600, 400))
	defer p3.Close()
	p3.Frame()
	if !strings.Contains(p3.Semantics().String(), "acme asks") {
		t.Fatalf("third-party dialog missing provenance footer 'acme asks':\n%s", p3.Semantics().String())
	}
	p3.Key("escape")
	p3.Frame()
	if len(got) != 1 || got[0] != (ev{"/confirm", protocol.UiChange, "dismiss"}) {
		t.Fatalf("Escape dismiss events = %+v, want UiChange dismiss", got)
	}

	// Bundled plugin dialog does NOT show the third-party provenance footer.
	tB := dlgTree()
	vB := newTreeView(m, "plugin:mako-git/panel", "", newBlockForm(), tB)
	pB := ggui.ProbeBuilder(func() ggui.Widget { return vB.node(tB, false) }, ggui.Sz(600, 400))
	defer pB.Close()
	pB.Frame()
	if strings.Contains(pB.Semantics().String(), "mako-git asks") {
		t.Fatalf("bundled plugin dialog should not show third-party provenance footer:\n%s", pB.Semantics().String())
	}
}

func TestPluginIntentsAndPermissions(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.Conn.Set(connState{Self: &protocol.Node{Id: "node-self"}})

	// 1. CopyText -> clipboard. The intent runs in a Probe's frame, as it
	// does in the app's, so it writes the Probe's own clipboard. Outside a
	// frame ggui uses the system clipboard: the test would overwrite the
	// user's, and fail when another process writes it in between.
	clipProbe := ggui.ProbeBuilder(func() ggui.Widget { return widget.Text("") }, ggui.Sz(100, 100))
	defer clipProbe.Close()
	var copied string
	clipProbe.Post(func() {
		m.handlePluginDone(&protocol.PluginDone{CopyText: "deadbeef"})
		copied = ggui.CurrentClipboard().Read()
	})
	clipProbe.Frame()
	if copied != "deadbeef" {
		t.Fatalf("clipboard = %q, want deadbeef", copied)
	}

	// 2. openDiff intent -> Diffs tab opened.
	m.handlePluginDone(&protocol.PluginDone{
		Open: []*protocol.PluginOpen{{
			Kind:    "diff",
			Node:    "local",
			Folder:  "/repo",
			Repo:    "/repo",
			Path:    "a.go",
			Status:  protocol.GitModified,
			Added:   5,
			Removed: 2,
			Preview: true,
		}},
	})
	diffs := ggui.Peek(m.Diffs)
	if len(diffs) != 1 {
		t.Fatalf("len(Diffs) = %d, want 1", len(diffs))
	}
	for _, d := range diffs {
		if d.Root.Node != "node-self" || d.Repo != "/repo" || d.File.GetPath() != "a.go" || !d.Preview {
			t.Fatalf("opened diff = %+v", d)
		}
	}

	// 3. openFile intent with line -> Docs tab opened with line stored.
	m.handlePluginDone(&protocol.PluginDone{
		Open: []*protocol.PluginOpen{{
			Kind:   "file",
			Node:   "local",
			Folder: "/repo",
			Path:   "main.go",
			Line:   42,
		}},
	})
	id := docTabID("node-self", "/repo/main.go")
	if _, ok := ggui.Peek(m.Docs)[id]; !ok {
		t.Fatalf("expected doc %q in m.Docs: %+v", id, ggui.Peek(m.Docs))
	}
	if doc := m.docs[id]; doc == nil || doc.line != 42 {
		t.Fatalf("doc line = %+v, want 42", doc)
	}

	// 4. openForge PR & runs intents -> m.Forge.Open updated.
	m.handlePluginDone(&protocol.PluginDone{
		Open: []*protocol.PluginOpen{{
			Kind:   "pr",
			Node:   "local",
			Folder: "/repo",
			Number: 17,
		}},
	})
	if o := ggui.Peek(m.Forge.Open); o.Kind != protocol.ForgePR || o.Number != 17 || o.Root != (fileRoot{Node: "node-self", Path: "/repo"}) {
		t.Fatalf("Forge.Open (pr) = %+v", o)
	}
	m.handlePluginDone(&protocol.PluginDone{
		Open: []*protocol.PluginOpen{{
			Kind:   "runs",
			Node:   "local",
			Folder: "/repo",
			Number: 99,
		}},
	})
	if o := ggui.Peek(m.Forge.Open); o.Kind != protocol.ForgeRuns || o.Number != 99 {
		t.Fatalf("Forge.Open (runs) = %+v", o)
	}

	// 5. create_pr intent -> sets RepoPick and opens PR dialog.
	m.handlePluginDone(&protocol.PluginDone{
		Open: []*protocol.PluginOpen{{
			Kind:   "create_pr",
			Node:   "local",
			Folder: "/repo-b",
		}},
	})
	if !ggui.Peek(m.Forge.PROpen) {
		t.Fatal("expected Forge.PROpen to be true after create_pr intent")
	}
	if rp := ggui.Peek(m.RepoPick); rp.At != (fileRoot{Node: "node-self", Path: "/repo-b"}) {
		t.Fatalf("RepoPick = %+v, want /repo-b", rp)
	}

	// 6. Permission descriptions for git:read, git:write, forge:read.
	for _, perm := range []string{"git:read", "git:write", "forge:read"} {
		if got := permissionText(perm); got == "" || got == perm {
			t.Fatalf("permissionText(%q) = %q, want descriptive string", perm, got)
		}
	}
}

func TestPluginPlaceholderNamesDangerContrastAndDagScrollPreserved(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")

	// 1. Placeholder provides accessible names for icon-only button/menu and tree_item checkbox.
	tree := &protocol.UiNode{
		Id:   "/root",
		Kind: "column",
		Children: []*protocol.UiNode{
			{Id: "/refresh", Kind: "button", Icon: "refresh-cw", Placeholder: "Refresh status", Variant: "ghost", Tap: true},
			{
				Id: "/more", Kind: "menu", Icon: "ellipsis", Placeholder: "Git actions", Variant: "ghost",
				Children: []*protocol.UiNode{{Id: "/more/fetch", Kind: "menu_item", Text: "Fetch", Tap: true}},
			},
			{
				Id: "/files", Kind: "tree",
				Children: []*protocol.UiNode{
					{Id: "/files/a", Kind: "tree_item", Text: "main.go", Placeholder: "Commit main.go", Check: "on", Checks: true},
				},
			},
		},
	}
	form := newBlockForm()
	p := ggui.ProbeBuilder(func() ggui.Widget {
		return newTreeView(m, "plugin:mako-git/panel", "", form, tree).node(tree, false)
	}, ggui.Sz(360, 240))
	defer p.Close()
	p.Frame()
	sem := p.Semantics().String()
	if _, ok := p.Semantics().Find(ggui.RoleButton, "Refresh status"); !ok {
		t.Fatalf("icon-only button missing placeholder accessible name:\n%s", sem)
	}
	if _, ok := p.Semantics().Find(ggui.RoleMenu, "Git actions"); !ok {
		t.Fatalf("icon-only menu missing placeholder accessible name:\n%s", sem)
	}
	if _, ok := p.Semantics().Find(ggui.RoleCheckbox, "Commit main.go"); !ok {
		t.Fatalf("tree_item checkbox missing placeholder override name:\n%s", sem)
	}

	// 2. Solid danger button variant maintains >= 4.5:1 WCAG AA contrast in both light & dark themes.
	for _, pal := range []skin.Palette{skin.Dark, skin.Light} {
		st := pluginDangerButtonStyle(skin.Theme(pal))
		if cr := wcagContrast(st.Fill, st.Label); cr < 4.5 {
			t.Errorf("%s theme danger button rest contrast = %.2f, want >= 4.5", pal.Name, cr)
		}
		if cr := wcagContrast(st.Hover, st.Label); cr < 4.5 {
			t.Errorf("%s theme danger button hover contrast = %.2f, want >= 4.5", pal.Name, cr)
		}
	}

	// 3. DAG rows signal and scroll offset are backed by blockForm across unrelated tree re-renders.
	rows := make([]*protocol.UiRow, 25)
	for i := range rows {
		rows[i] = &protocol.UiRow{Id: fmt.Sprintf("c%02d", i), Text: fmt.Sprintf("Commit %02d", i)}
	}
	dagA := &protocol.UiNode{Id: "/hist", Kind: "dag", MaxHeight: 160, Rows: rows}
	dagForm := newBlockForm()
	renderCount := ggui.State(0)
	p2 := ggui.ProbeBuilder(func() ggui.Widget {
		_ = renderCount.Get()
		return newTreeView(m, "plugin:mako-git/panel", "", dagForm, dagA).node(dagA, false)
	}, ggui.Sz(360, 200))
	defer p2.Close()
	p2.Frame()

	ds := dagForm.dags["/hist"]
	if ds == nil {
		t.Fatal("expected dagViewState stored in blockForm.dags")
	}
	ds.offset.Set(120)
	p2.Frame()

	// Trigger an unrelated re-render (e.g. toggling a file checkbox in Changes).
	renderCount.Set(1)
	p2.Frame()
	if got := ggui.Peek(ds.offset); got != 120 {
		t.Fatalf("DAG scroll offset after unrelated re-render = %v, want 120", got)
	}
	if dagForm.dags["/hist"] != ds {
		t.Fatal("expected same dagViewState instance across re-renders")
	}
}
