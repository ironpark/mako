package plugin

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

// fakeGitHub answers the example plugin's requests as GitHub's API would,
// for the repositories o/r and o/p1, and records the runs asked to run again.
func fakeGitHub(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var reruns []string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/actions/runs":
			if r.Header.Get("Authorization") != "Bearer tok" {
				http.Error(w, "no token", http.StatusUnauthorized)
				return
			}
			io.WriteString(w, `{"workflow_runs":[
				{"id":11,"name":"test","head_branch":"main","run_number":41,"status":"completed","conclusion":"failure","html_url":"https://github.com/o/r/actions/runs/11"},
				{"id":10,"name":"build","head_branch":"main","run_number":40,"status":"completed","conclusion":"success","html_url":"https://github.com/o/r/actions/runs/10"},
				{"id":12,"name":"lint","head_branch":"dev","run_number":42,"status":"in_progress","conclusion":null,"html_url":"https://github.com/o/r/actions/runs/12"}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/p1/actions/runs":
			if r.Header.Get("Authorization") != "Bearer tok" {
				http.Error(w, "no token", http.StatusUnauthorized)
				return
			}
			io.WriteString(w, `{"workflow_runs":[
				{"id":21,"name":"deploy-p1","head_branch":"release","run_number":7,"status":"completed","conclusion":"success","html_url":"https://github.com/o/p1/actions/runs/21"}]}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/rerun"):
			mu.Lock()
			reruns = append(reruns, r.URL.Path)
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), reruns...)
	}
}

// TestExamplePlugin runs examples/plugins/ci-status against a stand-in for
// GitHub: its block lists the runs (with global repo fallback and per-project
// repo override), a tap on one opens it, its command runs the failed one again,
// and with GitHub gone it shows the runs it kept.
func TestExamplePlugin(t *testing.T) {
	gh, reruns := fakeGitHub(t)
	r := newRig(t, testLimits, map[string]string{"api.github.com": gh.URL},
		&protocol.SessionInfo{Id: "s1", Status: protocol.AgentWorking}, &protocol.SessionInfo{Id: "s2", Status: protocol.AgentIdle})
	files := map[string]string{}
	for _, name := range []string{ManifestName, "main.js"} {
		b, err := os.ReadFile(filepath.Join("../../examples/plugins/ci-status", name))
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(b)
	}
	r.write("ci-status", files)
	r.state("ci-status", protocol.PluginWaiting)
	r.grant("ci-status")
	for k, v := range map[string]string{"repo": "o/r", "token": "tok"} {
		if _, err := r.act(&protocol.PluginAction{PluginId: "ci-status", Op: protocol.PluginSet, Key: k, Value: v}); err != nil {
			t.Fatal(err)
		}
	}
	w := r.watch("plugin:ci-status/ci")
	tree := w.until("the runs", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return hasText(n, "lint") })
	for _, want := range []string{"o/r", "failure", "success", "in_progress", "#41", "1 agent at work"} {
		if !hasText(tree, want) {
			t.Fatalf("no %q in %v", want, texts(tree))
		}
	}
	row := find(tree, "test")
	if row == nil {
		t.Fatal("no row of the test run")
	}
	done, err := r.event("plugin:ci-status/ci", strings.TrimSuffix(row.GetId(), "/#1"), protocol.UiTap, "")
	if err != nil || done.GetOpenUrl() != "https://github.com/o/r/actions/runs/11" {
		t.Fatalf("tapping the run: %v %v", done, err)
	}
	if _, err := r.act(&protocol.PluginAction{PluginId: "ci-status", Op: protocol.PluginRun, Command: "rerun"}); err != nil {
		t.Fatal(err)
	}
	w.until("the note", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return hasText(n, "#41 was started again") })
	if got := reruns(); len(got) != 1 || got[0] != "/repos/o/r/actions/runs/11/rerun" {
		t.Fatalf("reruns %v", got)
	}

	// Set a project-scoped repo override for project:p1 while leaving global fallback intact.
	if _, err := r.act(&protocol.PluginAction{
		PluginId: "ci-status",
		Op:       protocol.PluginSet,
		Key:      "repo",
		Value:    "o/p1",
		Scope:    "project:p1",
	}); err != nil {
		t.Fatal(err)
	}
	var repoSetting *protocol.PluginSetting
	for _, p := range r.m.List() {
		if p.GetId() == "ci-status" {
			for _, st := range p.GetSettings() {
				if st.GetKey() == "repo" {
					repoSetting = st
				}
			}
		}
	}
	if repoSetting == nil || repoSetting.GetScope() != protocol.ScopeProject || repoSetting.GetScopedValues()["project:p1"] != "o/p1" {
		t.Fatalf("expected project-scoped repo override, got %+v", repoSetting)
	}
	wP1 := r.watchScoped("plugin:ci-status/ci", "project:p1")
	wP1.until("p1 scoped repo runs", func(n *protocol.UiNode, u *protocol.BlockUpdate) bool {
		return u.GetScope() == "project:p1" && hasText(n, "o/p1") && hasText(n, "deploy-p1")
	})
	wP2 := r.watchScoped("plugin:ci-status/ci", "project:p2")
	wP2.until("p2 global fallback", func(n *protocol.UiNode, u *protocol.BlockUpdate) bool {
		return u.GetScope() == "project:p2" && hasText(n, "o/r") && hasText(n, "lint")
	})
	wP1.cancel()
	wP2.cancel()

	// GitHub gone, the runs kept in the store are shown, and why.
	gh.Close()
	if _, err := r.event("plugin:ci-status/ci", find(tree, "Refresh").GetId(), protocol.UiTap, ""); err != nil {
		t.Fatal(err)
	}
	w.until("the runs kept", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(n, "showing the runs last read") && hasText(n, "lint")
	})
}

// TestKanbanExamplePlugin runs examples/plugins/kanban (v0.3.0, project-scoped):
// independent boards and storage per project, ctx.projectName in the board
// header, card/scroll/dragGroup/dropAccept nodes, live form values via
// mako.blocks.values, UiDrop across columns and cards, compact view ui.tabs,
// strictly scoped MCP tools (list_cards/add_card/move_card), and scoped commands.
func TestKanbanExamplePlugin(t *testing.T) {
	r := startRig(t, Config{
		Limits: testLimits,
		Sessions: func() []*protocol.SessionInfo {
			return []*protocol.SessionInfo{
				{Id: "s1", Name: "builder", Agent: "claude", State: protocol.StateRunning, Status: protocol.AgentIdle},
			}
		},
		ResolveScope: func(scopeKey string) ScopeContext {
			switch scopeKey {
			case "project:p1":
				return ScopeContext{
					SpaceID:     "sp1",
					SpaceName:   "Work",
					ProjectID:   "p1",
					ProjectName: "Alpha",
					Roots:       []string{"/tmp/alpha"},
				}
			case "project:p2":
				return ScopeContext{
					SpaceID:     "sp1",
					SpaceName:   "Work",
					ProjectID:   "p2",
					ProjectName: "Beta",
					Roots:       []string{"/tmp/beta"},
				}
			}
			return ScopeContext{}
		},
	})
	files := map[string]string{}
	for _, name := range []string{ManifestName, "main.js"} {
		b, err := os.ReadFile(filepath.Join("../../examples/plugins/kanban", name))
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(b)
	}
	r.write("kanban", files)
	r.state("kanban", protocol.PluginWaiting)
	r.grant("kanban")

	// Watch project:p1 and project:p2 simultaneously.
	w1 := r.watchScoped("plugin:kanban/board", "project:p1")
	w2 := r.watchScoped("plugin:kanban/board", "project:p2")

	tree1 := w1.until("p1 starter cards", func(n *protocol.UiNode, u *protocol.BlockUpdate) bool {
		return u.GetScope() == "project:p1" &&
			hasText(n, "Kanban · Alpha · 1/5 done") &&
			hasText(n, "Draft API spec") &&
			hasText(n, "Build Kanban prototype")
	})
	w2.until("p2 starter cards", func(n *protocol.UiNode, u *protocol.BlockUpdate) bool {
		return u.GetScope() == "project:p2" &&
			hasText(n, "Kanban · Beta · 1/5 done") &&
			hasText(n, "Draft API spec")
	})
	if card := find(tree1, "Draft API spec"); card == nil {
		t.Fatal("missing starter card")
	}
	// Verify card kind, scroll wrapper, dragGroup, and dropAccept are present on the tree.
	var hasDrag, hasDrop, hasScroll bool
	var walk func(*protocol.UiNode)
	walk = func(n *protocol.UiNode) {
		if n.GetKind() == "card" && n.GetDrag() == "c1" && n.GetDragGroup() == "card" {
			hasDrag = true
		}
		if n.GetKind() == "scroll" && strings.HasSuffix(n.GetId(), "/cards") {
			hasScroll = true
		}
		if n.GetDrop() && strings.HasSuffix(n.GetId(), "/col-done") && len(n.GetDropAccept()) == 1 && n.GetDropAccept()[0] == "card" {
			hasDrop = true
		}
		for _, c := range n.GetChildren() {
			walk(c)
		}
	}
	walk(tree1)
	if !hasDrag || !hasDrop || !hasScroll {
		t.Fatalf("expected draggable card c1 (%v), scroll (%v), and drop target col-done (%v)", hasDrag, hasScroll, hasDrop)
	}

	// Add a new card in project:p1 by tapping Add button while passing live form values.
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block:  "plugin:kanban/board",
		Scope:  "project:p1",
		Node:   "/addbar/add-btn",
		Kind:   protocol.UiTap,
		Values: map[string]string{"/addbar/draft": "Ship drag and drop", "/addbar/draft-priority": "high"},
	}); err != nil {
		t.Fatal(err)
	}
	w1.until("p1 added card", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(n, "Ship drag and drop") && hasText(n, "Kanban · Alpha · 1/6 done")
	})

	// Drop card c1 onto the Done column in project:p1: done count rises from 1/6 to 2/6.
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:kanban/board",
		Scope: "project:p1",
		Node:  "/columns/col-done",
		Kind:  protocol.UiDrop,
		Value: "c1",
	}); err != nil {
		t.Fatal(err)
	}
	w1.until("p1 card moved to Done", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(n, "Kanban · Alpha · 2/6 done")
	})

	// Drop card c2 onto card c3 inside In Progress/cards scroll container in project:p1 to reorder before it.
	if _, err := r.m.Event(t.Context(), &protocol.BlockEvent{
		Block: "plugin:kanban/board",
		Scope: "project:p1",
		Node:  "/columns/col-doing/cards/c3",
		Kind:  protocol.UiDrop,
		Value: "c2",
	}); err != nil {
		t.Fatal(err)
	}
	w1.until("p1 card moved before c3", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		var order []string
		var findCol func(*protocol.UiNode)
		findCol = func(node *protocol.UiNode) {
			if strings.HasSuffix(node.GetId(), "/col-doing/cards") {
				for _, ch := range node.GetChildren() {
					if ch.GetDrag() != "" {
						order = append(order, ch.GetDrag())
					}
				}
			}
			for _, ch := range node.GetChildren() {
				findCol(ch)
			}
		}
		findCol(n)
		return len(order) == 2 && order[0] == "c2" && order[1] == "c3"
	})

	// Exercise strictly project-scoped MCP tools: list_cards, add_card, move_card.
	tools := r.m.Tools()
	if len(tools) != 3 {
		t.Fatalf("expected 3 kanban tools, got %d", len(tools))
	}
	for _, tl := range tools {
		if tl.Scope != protocol.ScopeProject {
			t.Fatalf("expected tool %s to have project scope, got %q", tl.ID, tl.Scope)
		}
	}
	// Missing ProjectID must be refused.
	if _, err := r.m.CallToolScoped(t.Context(), "kanban", "add_card", []byte(`{"title":"Should fail"}`), ScopeContext{}); err == nil {
		t.Fatal("expected add_card without ProjectID to be refused")
	}
	if _, err := r.m.CallToolScoped(t.Context(), "kanban", "list_cards", nil, ScopeContext{}); err == nil {
		t.Fatal("expected list_cards without ProjectID to be refused")
	}
	if _, err := r.m.CallToolScoped(t.Context(), "kanban", "move_card", []byte(`{"id":"c1","column":"done"}`), ScopeContext{}); err == nil {
		t.Fatal("expected move_card without ProjectID to be refused")
	}

	// Adding and moving cards in p1 affects ONLY p1.
	res, err := r.m.CallToolScoped(t.Context(), "kanban", "add_card",
		[]byte(`{"title":"Agent task","column":"review","priority":"high"}`),
		ScopeContext{ProjectID: "p1"})
	if err != nil || !strings.Contains(res, `"Agent task"`) {
		t.Fatalf("add_card p1: %s, %v", res, err)
	}
	w1.until("p1 agent card rendered", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(n, "Agent task")
	})
	listedP1, err := r.m.CallToolScoped(t.Context(), "kanban", "list_cards", []byte(`{"column":"review"}`), ScopeContext{ProjectID: "p1"})
	if err != nil || !strings.Contains(listedP1, `"Agent task"`) {
		t.Fatalf("list_cards p1: %s, %v", listedP1, err)
	}
	listedP2, err := r.m.CallToolScoped(t.Context(), "kanban", "list_cards", nil, ScopeContext{ProjectID: "p2"})
	if err != nil || strings.Contains(listedP2, `"Agent task"`) || strings.Contains(listedP2, `"Ship drag and drop"`) {
		t.Fatalf("p2 leaked p1 cards: %s, %v", listedP2, err)
	}
	if _, err := r.m.CallToolScoped(t.Context(), "kanban", "move_card",
		[]byte(`{"id":"c2","column":"done"}`),
		ScopeContext{ProjectID: "p1"}); err != nil {
		t.Fatalf("move_card p1: %v", err)
	}
	w1.until("p1 agent moved card to done", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(n, "Kanban · Alpha · 3/7 done")
	})

	// Add an independent card to project:p2 and verify p1 does not see it.
	if res, err := r.m.CallToolScoped(t.Context(), "kanban", "add_card",
		[]byte(`{"title":"Beta only task","column":"todo"}`),
		ScopeContext{ProjectID: "p2"}); err != nil || !strings.Contains(res, `"Beta only task"`) {
		t.Fatalf("add_card p2: %s, %v", res, err)
	}
	w2.until("p2 independent card", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(n, "Beta only task") && hasText(n, "Kanban · Beta · 1/6 done") && !hasText(n, "Agent task")
	})
	if listedP1All, err := r.m.CallToolScoped(t.Context(), "kanban", "list_cards", nil, ScopeContext{ProjectID: "p1"}); err != nil || strings.Contains(listedP1All, `"Beta only task"`) {
		t.Fatalf("p1 leaked p2 card: %s, %v", listedP1All, err)
	}

	// Reset command scoped to p1 returns a toast notice, resets p1, and leaves p2 intact.
	done, err := r.act(&protocol.PluginAction{PluginId: "kanban", Op: protocol.PluginRun, Command: "reset", Scope: "project:p1"})
	if err != nil || done.GetToast() == "" {
		t.Fatalf("reset command: done=%+v err=%v", done, err)
	}
	w1.until("p1 reset", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(n, "Kanban · Alpha · 1/5 done") && !hasText(n, "Agent task")
	})
	if listedP2After, err := r.m.CallToolScoped(t.Context(), "kanban", "list_cards", nil, ScopeContext{ProjectID: "p2"}); err != nil || !strings.Contains(listedP2After, `"Beta only task"`) {
		t.Fatalf("p1 reset disturbed p2: %s, %v", listedP2After, err)
	}

	var info *protocol.PluginInfo
	for _, p := range r.m.List() {
		if p.GetId() == "kanban" {
			info = p
		}
	}
	if info == nil || len(info.GetBlocks()) != 1 || info.GetBlocks()[0].GetScope() != protocol.ScopeProject || len(info.GetTools()) != 3 || len(info.GetLogs()) == 0 || info.GetStorageUsed() == 0 {
		t.Fatalf("kanban diagnostics: blocks=%+v tools=%d logs=%d storage=%d", info.GetBlocks(), len(info.GetTools()), len(info.GetLogs()), info.GetStorageUsed())
	}

	// Switch project:p1 to compact view via scoped command and verify ui.tabs renders.
	if _, err := r.act(&protocol.PluginAction{PluginId: "kanban", Op: protocol.PluginRun, Command: "toggle-view", Scope: "project:p1"}); err != nil {
		t.Fatal(err)
	}
	w1.until("p1 compact view", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		if !hasText(n, "Board") {
			return false
		}
		var hasTabs bool
		var scan func(*protocol.UiNode)
		scan = func(node *protocol.UiNode) {
			if node.GetKind() == "tabs" && len(node.GetOptions()) == 4 {
				hasTabs = true
			}
			for _, ch := range node.GetChildren() {
				scan(ch)
			}
		}
		scan(n)
		return hasTabs
	})
}

func TestOKFExamplePlugin(t *testing.T) {
	r := startRig(t, Config{
		Limits: testLimits,
		ResolveScope: func(scopeKey string) ScopeContext {
			switch scopeKey {
			case "project:repo:github.com/ironpark/alpha":
				return ScopeContext{
					Scope:       protocol.ScopeProject,
					ScopeKey:    scopeKey,
					ProjectID:   "repo:github.com/ironpark/alpha",
					ProjectName: "Alpha",
					Roots:       []string{"/tmp/alpha"},
				}
			case "project:repo:github.com/ironpark/beta":
				return ScopeContext{
					Scope:       protocol.ScopeProject,
					ScopeKey:    scopeKey,
					ProjectID:   "repo:github.com/ironpark/beta",
					ProjectName: "Beta",
					Roots:       []string{"/tmp/beta"},
				}
			}
			return ScopeContext{}
		},
	})
	files := map[string]string{}
	for _, name := range []string{ManifestName, "main.js", "lib/okf.js"} {
		b, err := os.ReadFile(filepath.Join("../../examples/plugins/okf", name))
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(b)
	}
	r.write("okf", files)
	r.state("okf", protocol.PluginWaiting)
	r.grant("okf")

	scopeA := "project:repo:github.com/ironpark/alpha"
	scopeB := "project:repo:github.com/ironpark/beta"
	ctxA := ScopeContext{ProjectID: "repo:github.com/ironpark/alpha", ProjectName: "Alpha"}
	ctxB := ScopeContext{ProjectID: "repo:github.com/ironpark/beta", ProjectName: "Beta"}

	// First watch bootstraps starter index.md + concepts/overview.md in Alpha's isolated file vault.
	wA := r.watchScoped("plugin:okf/vault", scopeA)
	wA.until("starter OKF vault", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(n, "2 docs") && hasText(n, "Alpha Overview") && hasText(n, "index.md")
	})

	// Upsert an OKF Decision document linking to concepts/overview and a missing concept.
	upserted, err := r.m.CallToolScoped(t.Context(), "okf", "upsert_doc", []byte(`{
		"path": "decisions/001-storage.md",
		"title": "Use Sandboxed Datafs for OKF",
		"type": "Decision",
		"description": "Store OKF bundles as real Markdown files under plugin-data.",
		"tags": "storage, okf",
		"status": "active",
		"source": "architecture review",
		"verified_by": "team",
		"body": "# Storage Decision\n\nBuilds on [[concepts/overview]] and [[concepts/sync-plan]]."
	}`), ctxA)
	if err != nil || !strings.Contains(upserted, `"saved":true`) {
		t.Fatalf("upsert_doc: %s, err=%v", upserted, err)
	}
	wA.until("new decision reflected in UI", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		if !hasText(n, "Use Sandboxed Datafs for OKF") || !hasText(n, "3 docs") {
			return false
		}
		var hasHealth1 bool
		var scan func(*protocol.UiNode)
		scan = func(node *protocol.UiNode) {
			if node.GetKind() == "tabs" {
				for _, opt := range node.GetOptions() {
					if opt == "Health (1)" {
						hasHealth1 = true
					}
				}
			}
			for _, ch := range node.GetChildren() {
				scan(ch)
			}
		}
		scan(n)
		return hasHealth1
	})

	// Backlinks on concepts/overview.md include decisions/001-storage.md.
	overview, err := r.m.CallToolScoped(t.Context(), "okf", "read_doc", []byte(`{"path":"concepts/overview.md"}`), ctxA)
	if err != nil || !strings.Contains(overview, `"decisions/001-storage.md"`) {
		t.Fatalf("read_doc backlinks missing: %s, err=%v", overview, err)
	}

	// Search finds the new document.
	searched, err := r.m.CallToolScoped(t.Context(), "okf", "search", []byte(`{"query":"sandboxed"}`), ctxA)
	if err != nil || !strings.Contains(searched, `"decisions/001-storage.md"`) {
		t.Fatalf("search failed: %s, err=%v", searched, err)
	}

	// Lint detects the broken [[concepts/sync-plan]] link.
	linted, err := r.m.CallToolScoped(t.Context(), "okf", "lint", nil, ctxA)
	if err != nil || !strings.Contains(linted, `"concepts/sync-plan"`) {
		t.Fatalf("lint missed broken link: %s, err=%v", linted, err)
	}

	// Move and delete work cleanly and keep Alpha isolated from Beta.
	if moved, err := r.m.CallToolScoped(t.Context(), "okf", "move_doc", []byte(`{
		"from_path": "decisions/001-storage.md",
		"to_path": "decisions/adr-001-storage.md"
	}`), ctxA); err != nil || !strings.Contains(moved, `"moved":true`) {
		t.Fatalf("move_doc: %s, err=%v", moved, err)
	}
	listB, err := r.m.CallToolScoped(t.Context(), "okf", "list_docs", nil, ctxB)
	if err != nil || strings.Contains(listB, "adr-001-storage") {
		t.Fatalf("beta leaked alpha vault: %s, err=%v", listB, err)
	}
	if deleted, err := r.m.CallToolScoped(t.Context(), "okf", "delete_doc", []byte(`{"path":"decisions/adr-001-storage.md"}`), ctxA); err != nil || !strings.Contains(deleted, `"deleted":true`) {
		t.Fatalf("delete_doc: %s, err=%v", deleted, err)
	}
	_ = scopeB
}
