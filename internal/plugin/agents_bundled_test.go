package plugin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/plugin/hostapi"
	"github.com/ironpark/mako/internal/protocol"
)

// allowAllGit allows Git every folder, so that a start refused is refused
// for the folder the plugin shows.
type allowAllGit struct{ *fakeGitHost }

func (allowAllGit) Allowed(GitTarget) bool { return true }

// bundledStarter is a bundled plugin with a block shown for the folder
// shown, and agents to start: none allowed without asking.
func bundledStarter(t *testing.T, shown string, agents *fakeAgents) (*plugin, *host) {
	t.Helper()
	m := New(Config{DataDir: t.TempDir(), Git: allowAllGit{newFakeGitHost()}, Agents: agents})
	t.Cleanup(m.shutdown)
	p := newPlugin(m, "bundled-starter")
	p.bundled = true
	p.inst = map[string]*instance{"panel": {key: "panel", id: "panel", scopeKey: protocol.FolderScope(protocol.LocalScopeNode, shown), subs: 1}}
	return p, &host{p: p}
}

// TestBundledPluginAsksBeforeItStartsAnAgent: a bundled plugin starts an
// agent as any plugin does, once the user allows it, and in a folder it
// shows only when the folder is really within, through its links.
func TestBundledPluginAsksBeforeItStartsAnAgent(t *testing.T) {
	shown := t.TempDir()
	sub := filepath.Join(shown, "worktree")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(shown, "link-out")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shown, "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	agents := &fakeAgents{}
	p, h := bundledStarter(t, shown, agents)

	res, err := h.MessagesStartAgent(context.Background(), &hostapi.StartAgentRequest{
		Agent: "claude", Project: "p1", Root: sub, Prompt: "fix the build", NoYolo: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.GetAsked() {
		t.Fatal("a bundled plugin started an agent without asking the user")
	}
	ask := p.asks.get()
	if ask == nil || ask.req.Root != sub || !ask.req.NoYolo {
		t.Fatalf("ask = %+v; want one for %q with YOLO off", ask, sub)
	}
	if agents.starts() != 0 {
		t.Fatal("an agent started before the user allowed it")
	}
	if p.m.grant.get(p.id).AgentsAllowed {
		t.Fatal("the plugin reads as allowed to start agents")
	}

	for name, root := range map[string]string{
		"link out":   filepath.Join(shown, "link-out"),
		"under link": filepath.Join(shown, "link-out", "x"),
		"file":       filepath.Join(shown, "file.txt"),
		"relative":   "worktree",
		"missing":    filepath.Join(shown, "missing"),
		"outside":    outside,
	} {
		if p.showsLocalFolderHolding(root) {
			t.Errorf("%s: %q passes as a shown folder", name, root)
		}
		_, err := h.MessagesStartAgent(context.Background(), &hostapi.StartAgentRequest{Agent: "claude", Project: "p1", Root: root, Prompt: "x"})
		if err == nil || !strings.Contains(err.Error(), "knows no project") {
			t.Errorf("%s: start in %q: %v; want it refused", name, root, err)
		}
	}
	for _, root := range []string{shown, sub} {
		if !p.showsLocalFolderHolding(root) {
			t.Errorf("%q does not pass as a shown folder", root)
		}
	}

	// A shown folder that is itself a link holds what its target holds.
	via := filepath.Join(t.TempDir(), "via")
	if err := os.Symlink(shown, via); err != nil {
		t.Fatal(err)
	}
	p2, _ := bundledStarter(t, via, &fakeAgents{})
	if !p2.showsLocalFolderHolding(sub) {
		t.Errorf("%q is not within the shown link %q", sub, via)
	}

	// A plugin not bundled has no folder of its own to start in.
	p.bundled = false
	if _, err := h.MessagesStartAgent(context.Background(), &hostapi.StartAgentRequest{Agent: "claude", Project: "p1", Root: sub, Prompt: "x"}); err == nil {
		t.Fatal("a plugin not bundled started an agent outside the projects")
	}
}
