package daemon

import (
	"slices"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/plugin"
)

// A plugin's message is headed as the plugin's, never a session's, the
// window's or mako's; the agents it starts are saved with it as their
// parent, and it stops those alone.
func TestPluginAgentsGoThroughTheAgentsPaths(t *testing.T) {
	d, s := projectMCPDaemon(t)
	a := pluginAgents{d}
	if _, err := a.Send("ci", s.ID(), "the build is red"); err != nil {
		t.Fatal(err)
	}
	inbox, _ := d.projectAgents.messages.pending(s.ID())
	if len(inbox) != 1 || inbox[0].From != "plugin:ci" {
		t.Fatalf("inbox %+v", inbox)
	}
	prompt := d.agentPrompt(inbox)
	if !strings.Contains(prompt, "from the plugin ci: code the user installed") || strings.Contains(prompt, "from session") || !strings.Contains(prompt, "the build is red") {
		t.Fatalf("the agent is given %q", prompt)
	}
	if _, err := a.Send("ci", "nobody", "x"); err == nil {
		t.Fatal("a message to no session was left")
	}
	child, err := a.Start(t.Context(), "ci", plugin.AgentStart{Agent: "claude", Root: t.TempDir(), Prompt: "look at the build", Name: "CI fixer"})
	if err != nil {
		t.Fatal(err)
	}
	d.sessionMu.Lock()
	parent := d.saved[child].ParentID
	d.sessionMu.Unlock()
	if parent != "plugin:ci" {
		t.Fatalf("the agent's parent is %q", parent)
	}
	if n := a.Running("ci"); n != 1 {
		t.Fatalf("running %d", n)
	}
	if a.Running("other") != 0 {
		t.Fatal("another plugin counts the agent")
	}
	if err := a.Stop("other", child); err == nil {
		t.Fatal("another plugin stopped the agent")
	}
	if err := a.Stop("ci", s.ID()); err == nil {
		t.Fatal("the plugin stopped a session it did not start")
	}
	if err := a.Stop("ci", child); err != nil {
		t.Fatal(err)
	}
	if n := a.Running("ci"); n != 0 {
		t.Fatalf("running %d after the stop", n)
	}
	if _, err := a.Start(t.Context(), "ci", plugin.AgentStart{Agent: "nope", Root: t.TempDir(), Prompt: "x"}); err == nil {
		t.Fatal("an unknown agent started")
	}
}

// An agent a plugin starts with NoYolo asks before it acts, whatever its
// agent's settings say; one started without it keeps them.
func TestPluginAgentsStartWithoutYolo(t *testing.T) {
	d, _ := projectMCPDaemon(t)
	d.agentFile.mu.Lock()
	claude := d.agentFile.agents["claude"]
	claude.Yolo = true
	d.agentFile.agents["claude"] = claude
	d.agentFile.mu.Unlock()
	a := pluginAgents{d}
	yolo := func(noYolo bool) bool {
		t.Helper()
		id, err := a.Start(t.Context(), "ci", plugin.AgentStart{Agent: "claude", Root: t.TempDir(), Prompt: "x", NoYolo: noYolo})
		if err != nil {
			t.Fatal(err)
		}
		s, ok := d.sessions.get(id)
		if !ok {
			t.Fatalf("no session %s", id)
		}
		return slices.Contains(s.Command(), "--dangerously-skip-permissions")
	}
	if !yolo(false) {
		t.Fatal("an agent set to YOLO started without it")
	}
	if yolo(true) {
		t.Fatal("an agent started with NoYolo runs in YOLO mode")
	}
}
