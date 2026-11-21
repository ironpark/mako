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
// for the repository o/r, and records the runs asked to run again.
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
// GitHub: its block lists the runs, a tap on one opens it, its command
// runs the failed one again, and with GitHub gone it shows the runs it
// kept.
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
	// GitHub gone, the runs kept in the store are shown, and why.
	gh.Close()
	if _, err := r.event("plugin:ci-status/ci", find(tree, "Refresh").GetId(), protocol.UiTap, ""); err != nil {
		t.Fatal(err)
	}
	w.until("the runs kept", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(n, "showing the runs last read") && hasText(n, "lint")
	})
}

// TestKanbanExamplePlugin runs examples/plugins/kanban: starter cards load,
// a new card is submitted, dragging a card across columns and onto another
// card moves and reorders it via UiDrop, and the compact view toggle works.
func TestKanbanExamplePlugin(t *testing.T) {
	r := newRig(t, testLimits, nil,
		&protocol.SessionInfo{Id: "s1", Name: "builder", Agent: "claude", State: protocol.StateRunning, Status: protocol.AgentIdle})
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

	w := r.watch("plugin:kanban/board")
	tree := w.until("starter cards", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(n, "Draft API spec") && hasText(n, "Build Kanban prototype")
	})
	if card := find(tree, "Draft API spec"); card == nil {
		t.Fatal("missing starter card")
	}
	// Verify drag payload and drop target flags are present on the tree.
	var hasDrag, hasDrop bool
	var walk func(*protocol.UiNode)
	walk = func(n *protocol.UiNode) {
		if n.GetDrag() == "c1" {
			hasDrag = true
		}
		if n.GetDrop() && strings.HasSuffix(n.GetId(), "/col-done") {
			hasDrop = true
		}
		for _, c := range n.GetChildren() {
			walk(c)
		}
	}
	walk(tree)
	if !hasDrag || !hasDrop {
		t.Fatalf("expected draggable card c1 (%v) and drop target col-done (%v)", hasDrag, hasDrop)
	}

	// Add a new card via input submit.
	if _, err := r.event("plugin:kanban/board", "/addbar/draft", protocol.UiSubmit, "Ship drag and drop"); err != nil {
		t.Fatal(err)
	}
	w.until("added card", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(n, "Ship drag and drop")
	})

	// Drop card c1 onto the Done column: done count rises from 1/6 to 2/6.
	if _, err := r.event("plugin:kanban/board", "/columns/col-done", protocol.UiDrop, "c1"); err != nil {
		t.Fatal(err)
	}
	w.until("card moved to Done", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(n, "Kanban · 2/6 done")
	})

	// Drop card c2 onto card c3 inside In Progress to reorder before it.
	if _, err := r.event("plugin:kanban/board", "/columns/col-doing/c3", protocol.UiDrop, "c2"); err != nil {
		t.Fatal(err)
	}
	tree = w.until("card moved before c3", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		var order []string
		var findCol func(*protocol.UiNode)
		findCol = func(node *protocol.UiNode) {
			if strings.HasSuffix(node.GetId(), "/col-doing") {
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
	_ = tree

	// Switch to compact view via command.
	if _, err := r.act(&protocol.PluginAction{PluginId: "kanban", Op: protocol.PluginRun, Command: "toggle-view"}); err != nil {
		t.Fatal(err)
	}
	w.until("compact view", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return hasText(n, "Board") && hasText(n, "Todo (1)")
	})
}
