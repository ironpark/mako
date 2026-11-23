package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/layoutdoc"
	"github.com/ironpark/mako/internal/prompttmpl"
	"github.com/ironpark/mako/internal/protocol"
)

func TestWorkflow1OfflinePluginPipeline(t *testing.T) {
	env := newTestEnv(t, func(o *testConfigOpts) {
		o.NodeID = "wf1-node"
		o.Name = "wf1-daemon"
	})

	workDir := t.TempDir()
	keyPath := filepath.Join(workDir, "pub.key")
	env.mustCLI("plugins", "keygen", keyPath)

	kanbanSrc := filepath.Join(repoRootDir, "examples", "plugins", "kanban")
	pkgPath := filepath.Join(workDir, "kanban.makoplugin")
	env.mustCLI("plugins", "pack", kanbanSrc, "-key", keyPath, "-o", pkgPath)
	env.mustCLI("plugins", "verify", pkgPath)

	regDir := filepath.Join(workDir, "reg")
	if err := os.MkdirAll(regDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	env.mustCLI("plugins", "index", regDir, pkgPath, "-url", srv.URL+"/kanban.makoplugin", "-registry-name", "E2E Registry")

	mux.HandleFunc("/mako-registry.json", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(regDir, "mako-registry.json"))
	})
	mux.HandleFunc("/kanban.makoplugin", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, pkgPath)
	})

	d := startDaemon(t, env)

	env.mustCLI("plugins", "registries", "add", srv.URL+"/mako-registry.json")
	searchOut := env.mustCLI("plugins", "search", "kanban")
	if !strings.Contains(searchOut, "kanban") {
		t.Fatalf("search output missing kanban:\n%s", searchOut)
	}

	env.mustCLI("plugins", "install", "kanban")

	c := d.dial(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Wait until daemon reports kanban state before acting on it.
	waitPluginState := func(want string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			list, err := c.Plugins(ctx)
			if err == nil {
				for _, p := range list.GetPlugins() {
					if p.GetId() == "kanban" && p.GetState() == want {
						return
					}
				}
			}
			time.Sleep(30 * time.Millisecond)
		}
		list, _ := c.Plugins(ctx)
		t.Fatalf("timed out waiting for kanban state %q, got %+v", want, list)
	}
	waitPluginState(protocol.PluginWaiting)

	env.mustCLI("plugins", "grant", "kanban")
	waitPluginState(protocol.PluginReady)

	watchCtx, watchCancel := context.WithCancel(ctx)
	defer watchCancel()
	trees := make(chan *protocol.UiNode, 16)
	watchErrs := make(chan error, 1)
	go func() {
		var cur *protocol.UiNode
		err := c.WatchBlockScoped(watchCtx, "plugin:kanban/board", "project:p1", func(u *protocol.BlockUpdate) {
			if u.GetTree() != nil {
				cur = u.GetTree()
			} else if len(u.GetOps()) > 0 && cur != nil {
				if next, applyErr := protocol.ApplyUi(cur, u.GetOps()); applyErr == nil {
					cur = next
				}
			}
			if cur != nil {
				select {
				case trees <- cur:
				default:
				}
			}
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			select {
			case watchErrs <- err:
			default:
			}
		}
	}()

	waitTreeText := func(want string) {
		t.Helper()
		deadline := time.After(10 * time.Second)
		for {
			select {
			case tr := <-trees:
				if uiTreeContainsText(tr, want) {
					return
				}
			case err := <-watchErrs:
				t.Fatalf("WatchBlockScoped error waiting for %q: %v", want, err)
			case <-deadline:
				t.Fatalf("timed out waiting for %q in kanban WatchBlockScoped tree", want)
			}
		}
	}

	waitTreeText("1/5 done")

	if _, err := c.BlockEvent(ctx, &protocol.BlockEvent{
		Block: "plugin:kanban/board",
		Scope: "project:p1",
		Node:  "/columns/col-done",
		Kind:  protocol.UiDrop,
		Value: "c1",
	}); err != nil {
		t.Fatalf("c.BlockEvent: %v", err)
	}
	waitTreeText("2/5 done")
	watchCancel()

	env.mustCLI("plugins", "deny", "kanban")
	waitPluginState(protocol.PluginDenied)

	env.mustCLI("plugins", "remove", "kanban")
	nonBundledCount := func(list *protocol.Plugins) int {
		n := 0
		for _, p := range list.GetPlugins() {
			if !p.GetBundled() {
				n++
			}
		}
		return n
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		list, err := c.Plugins(ctx)
		if err == nil && nonBundledCount(list) == 0 {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	list, err := c.Plugins(ctx)
	if err != nil || nonBundledCount(list) != 0 {
		t.Fatalf("expected 0 non-bundled plugins after remove, got %+v err=%v", list, err)
	}

	d.stop(t)
}

func TestWorkflow2MockClaudeInboxAndWorkspace(t *testing.T) {
	scriptDir := t.TempDir()
	mockClaude := filepath.Join(scriptDir, "claude")
	script := `#!/bin/sh
# Daemon probe invocations (e.g., --help, plugin validate) do not set AGENT_MAKO_SESSION.
if [ -z "$AGENT_MAKO_SESSION" ]; then
  exit 0
fi
mako-msg list >/dev/null 2>&1 || true
mako-msg send "$AGENT_MAKO_SESSION" "ping-from-e2e" >/dev/null 2>&1 || true
exec sleep 30
`
	if err := os.WriteFile(mockClaude, []byte(script), 0o755); err != nil {
		t.Fatalf("write mock claude: %v", err)
	}

	projectDir := t.TempDir()
	env := newTestEnv(t, func(o *testConfigOpts) {
		o.NodeID = "wf2-node"
		o.Name = "wf2-daemon"
		o.Agents = map[string]any{
			"claude": map[string]any{
				"command": []string{mockClaude},
			},
		}
	})

	// Verify mako agents lists configured claude offline.
	agentsOut := env.mustCLI("agents")
	if !strings.Contains(agentsOut, "claude") {
		t.Fatalf("expected claude in mako agents:\n%s", agentsOut)
	}

	d := startDaemon(t, env)
	c := d.dial(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Exercise SetWorkspace / Workspace with layoutdoc.Default() + Apply.
	cat := layoutdoc.BuiltinCatalog()
	updatedDoc, err := layoutdoc.Apply(layoutdoc.Default(), []layoutdoc.Op{
		{Op: "select", ID: layoutdoc.DetailsNode, Block: layoutdoc.BlockGit},
	}, cat)
	if err != nil {
		t.Fatalf("layoutdoc.Apply: %v", err)
	}
	docJSON, err := json.Marshal(updatedDoc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetWorkspace(ctx, &protocol.Workspace{
		Doc:     docJSON,
		Version: time.Now().UnixNano(),
		NodeId:  c.Node().GetId(),
		State: &protocol.WorkspaceState{
			WindowLayouts: map[string]*protocol.LayoutEntry{
				layoutdoc.DefaultName: {Doc: docJSON},
			},
		},
	}); err != nil {
		t.Fatalf("SetWorkspace: %v", err)
	}
	ws, err := c.Workspace(ctx)
	if err != nil {
		t.Fatalf("Workspace: %v", err)
	}
	storedEntry := ws.GetState().GetWindowLayouts()[layoutdoc.DefaultName]
	if storedEntry == nil || len(storedEntry.GetDoc()) == 0 {
		t.Fatalf("expected default window layout in workspace state, got %+v", ws)
	}
	parsedDoc, err := layoutdoc.Parse(storedEntry.GetDoc(), cat)
	if err != nil {
		t.Fatalf("Parse stored workspace layout: %v", err)
	}
	if got := parsedDoc.Node(layoutdoc.DetailsNode).ActiveBlock(); got != layoutdoc.BlockGit {
		t.Fatalf("stored active block = %q, want %q", got, layoutdoc.BlockGit)
	}

	// Pair workspace layout with prompttmpl.LoadAll and ExpandWith (Scenario 4).
	tmplDir := t.TempDir()
	tmplSrc := "---\ndescription: Workflow handoff\nargs:\n  - name: block\n    type: string\n    required: true\n---\nActive details block: {{block}}"
	if err := os.WriteFile(filepath.Join(tmplDir, "handoff.md"), []byte(tmplSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	loadedTmpls, brokenTmpls := prompttmpl.LoadAll(tmplDir)
	if len(brokenTmpls) != 0 || len(loadedTmpls) == 0 {
		t.Fatalf("LoadAll: loaded=%d broken=%+v", len(loadedTmpls), brokenTmpls)
	}
	handoffTmpl, err := prompttmpl.Find(tmplDir, "handoff")
	if err != nil {
		t.Fatalf("Find(handoff): %v", err)
	}
	expanded, err := handoffTmpl.ExpandWith(ctx, parsedDoc.Node(layoutdoc.DetailsNode).ActiveBlock(), nil, nil)
	if err != nil || strings.TrimSpace(expanded) != "Active details block: "+layoutdoc.BlockGit {
		t.Fatalf("ExpandWith: got=%q err=%v", expanded, err)
	}

	// Open mock claude session and verify mako-msg delivery via ProjectAgents.
	sess, err := c.Open(ctx, &protocol.OpenSession{
		Agent:   "claude",
		Project: projectDir,
		Name:    "e2e-claude",
	})
	if err != nil {
		t.Fatalf("c.Open(claude): %v", err)
	}
	if sess.GetId() == "" {
		t.Fatal("opened session has empty ID")
	}

	// Verify CLI consistency while session is running.
	nodesOut := env.mustCLI("nodes")
	if !strings.Contains(nodesOut, "wf2-daemon") {
		t.Fatalf("mako nodes missing wf2-daemon:\n%s", nodesOut)
	}
	lsOut := env.mustCLI("ls")
	if !strings.Contains(lsOut, "e2e-claude") || !strings.Contains(lsOut, "claude") {
		t.Fatalf("mako ls missing running claude session:\n%s", lsOut)
	}

	// Poll ProjectAgents until mock claude's mako-msg send lands.
	var delivered bool
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		pa, err := c.ProjectAgents(ctx, &protocol.ReadProjectAgents{Path: projectDir})
		if err == nil {
			for _, m := range pa.GetMessages() {
				if m.GetFromSessionId() == sess.GetId() && m.GetText() == "ping-from-e2e" {
					delivered = true
					break
				}
			}
			if delivered {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !delivered {
		pa, _ := c.ProjectAgents(ctx, &protocol.ReadProjectAgents{Path: projectDir})
		t.Fatalf("timed out waiting for 'ping-from-e2e' in ProjectAgents: %+v\ndaemon logs:\n%s", pa, d.output.String())
	}

	if err := c.Kill(ctx, &protocol.CloseSession{SessionId: sess.GetId()}); err != nil {
		t.Fatalf("c.Kill: %v", err)
	}

	// Confirm session disappears / stops in mako ls.
	lsAfter := env.mustCLI("ls")
	if strings.TrimSpace(lsAfter) != "no sessions" && strings.Contains(lsAfter, "running") {
		t.Fatalf("expected killed session to stop, mako ls:\n%s", lsAfter)
	}

	d.stop(t)
}
