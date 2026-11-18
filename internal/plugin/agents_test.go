package plugin

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/protocol"
)

// fakeAgents stands for the daemon's agents: what a plugin sent and
// started, and the sessions it may stop.
type fakeAgents struct {
	mu      sync.Mutex
	sent    []string
	started []AgentStart
	running map[string]int
	stopped []string
}

func (f *fakeAgents) Send(plugin, session, text string) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if session != "s1" {
		return 0, fmt.Errorf("the agent of session %s takes no messages", session)
	}
	f.sent = append(f.sent, plugin+">"+session+": "+text)
	return uint64(len(f.sent)), nil
}

func (f *fakeAgents) Start(_ context.Context, plugin string, req AgentStart) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, req)
	if f.running == nil {
		f.running = map[string]int{}
	}
	f.running[plugin]++
	return fmt.Sprint("child", len(f.started)), nil
}

func (f *fakeAgents) Stop(plugin, session string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.HasPrefix(session, "child") {
		return fmt.Errorf("a plugin stops only the agents it started")
	}
	f.stopped = append(f.stopped, plugin+":"+session)
	return nil
}

func (f *fakeAgents) Running(plugin string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running[plugin]
}

func (f *fakeAgents) starts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.started)
}

// starter is a plugin whose command "go" asks to start an agent in p1,
// and whose handlers keep what they were told of it.
func starter(id, perms string) map[string]string {
	return map[string]string{
		ManifestName: manifest(id, strings.Split(perms, ","), ""),
		"main.js": `let mako;
async function note(line) {
  const kept = await mako.storage.getItem({ key: "seen" });
  await mako.storage.setItem({ key: "seen", value: (kept.found ? kept.value + ";" : "") + line });
}
export default {
  activate(m) {
    mako = m;
    m.blocks.register("ui", { state: {}, render: (s, ui) => ui.text("x") });
    m.commands.register("go", async () => {
      const r = await mako.messages.startAgent({ agent: "claude", project: "p1", root: "/proj", prompt: "fix the build", name: "fixer" });
      await note("asked " + r.asked);
    });
  },
  on: {
    agentStarted: (e) => note("started " + e.session.id),
    agentRefused: (e) => note("refused " + e.error),
  },
};`,
	}
}

func agentsRig(t *testing.T, agents *fakeAgents) *rig {
	return startRig(t, Config{Limits: testLimits, Agents: agents,
		Projects: func() []ProjectRoots { return []ProjectRoots{{ID: "p1", Name: "Proj", Roots: []string{"/proj"}}} }})
}

func (r *rig) run(id string) error {
	_, err := r.act(&protocol.PluginAction{PluginId: id, Op: protocol.PluginRun, Command: "go"})
	return err
}

func (r *rig) ask(id string) *protocol.PluginAgentAsk {
	return r.info(id, func(p *protocol.PluginInfo) bool { return p.GetAgentAsk() != nil }).GetAgentAsk()
}

func TestAPluginsFirstAgentIsTheUsersToAllow(t *testing.T) {
	agents := &fakeAgents{}
	r := agentsRig(t, agents)
	r.write("ci", starter("ci", "sessions:write,storage"))
	r.state("ci", protocol.PluginWaiting)
	r.grant("ci")
	// The permission alone starts nothing: the user is asked.
	if err := r.run("ci"); err != nil {
		t.Fatal(err)
	}
	ask := r.ask("ci")
	if ask.GetAgent() != "claude" || ask.GetProject() != "Proj" || ask.GetRoot() != "/proj" || ask.GetPrompt() != "fix the build" || ask.GetName() != "fixer" {
		t.Fatalf("the ask is %v", ask)
	}
	if agents.starts() != 0 {
		t.Fatal("an agent started before the user allowed it")
	}
	// A second ask waits for the first.
	if err := r.run("ci"); err == nil || !strings.Contains(err.Error(), "waits for the user") {
		t.Fatalf("a second ask: %v", err)
	}
	if _, err := r.act(&protocol.PluginAction{PluginId: "ci", Op: protocol.PluginAllowAgent, Key: "a-wrong", Value: protocol.AgentOnce}); err == nil {
		t.Fatal("another ask's ID was taken")
	}
	if _, err := r.act(&protocol.PluginAction{PluginId: "ci", Op: protocol.PluginAllowAgent, Key: ask.GetId(), Value: protocol.AgentOnce}); err != nil {
		t.Fatal(err)
	}
	r.waitSeen("ci", "asked true;started child1")
	// Once was once: the next asks again; refused, the plugin is told.
	if err := r.run("ci"); err != nil {
		t.Fatal(err)
	}
	ask = r.ask("ci")
	if _, err := r.act(&protocol.PluginAction{PluginId: "ci", Op: protocol.PluginDenyAgent, Key: ask.GetId()}); err != nil {
		t.Fatal(err)
	}
	r.waitSeen("ci", "asked true;started child1;asked true;refused the user refused to start the agent")
	// Always: this one and the next start without asking.
	if err := r.run("ci"); err != nil {
		t.Fatal(err)
	}
	ask = r.ask("ci")
	if _, err := r.act(&protocol.PluginAction{PluginId: "ci", Op: protocol.PluginAllowAgent, Key: ask.GetId(), Value: protocol.AgentAlways}); err != nil {
		t.Fatal(err)
	}
	r.waitSeen("ci", "asked true;started child1;asked true;refused the user refused to start the agent;asked true;started child2")
	if err := r.run("ci"); err != nil {
		t.Fatal(err)
	}
	r.waitSeen("ci", "asked true;started child1;asked true;refused the user refused to start the agent;asked true;started child2;asked false;started child3")
	if !r.info("ci", func(*protocol.PluginInfo) bool { return true }).GetAgentsAllowed() {
		t.Fatal("the listing does not say the plugin starts agents without asking")
	}
	// Three of its agents run: a fourth is refused before anything.
	if err := r.run("ci"); err == nil || !strings.Contains(err.Error(), "agents the plugin started run") {
		t.Fatalf("a fourth agent: %v", err)
	}
	// Forgotten, it asks again; revoked, it may not ask.
	if _, err := r.act(&protocol.PluginAction{PluginId: "ci", Op: protocol.PluginForgetAgents}); err != nil {
		t.Fatal(err)
	}
	if r.info("ci", func(*protocol.PluginInfo) bool { return true }).GetAgentsAllowed() {
		t.Fatal("forgotten, the plugin still starts agents without asking")
	}
}

func TestAnAskOutlivesNothingThePluginLoses(t *testing.T) {
	agents := &fakeAgents{}
	r := agentsRig(t, agents)
	r.write("ci", starter("ci", "sessions:write,storage"))
	r.state("ci", protocol.PluginWaiting)
	r.grant("ci")
	if err := r.run("ci"); err != nil {
		t.Fatal(err)
	}
	r.ask("ci")
	// Disabled while it asks: the ask goes, and allowing it then starts
	// nothing.
	if _, err := r.act(&protocol.PluginAction{PluginId: "ci", Op: protocol.PluginDisable}); err != nil {
		t.Fatal(err)
	}
	r.info("ci", func(p *protocol.PluginInfo) bool { return p.GetAgentAsk() == nil })
	if _, err := r.act(&protocol.PluginAction{PluginId: "ci", Op: protocol.PluginEnable}); err != nil {
		t.Fatal(err)
	}
	// An ask left unanswered is refused in time.
	if err := r.run("ci"); err != nil {
		t.Fatal(err)
	}
	r.ask("ci")
	p, _ := r.m.plugin("ci")
	p.expireAsk(time.Now().Add(agentAskFor + time.Second))
	r.info("ci", func(p *protocol.PluginInfo) bool { return p.GetAgentAsk() == nil })
	// The refusal of the ask dropped as it was disabled reaches it once it
	// runs again, or not at all, as the two race.
	r.waitSeenEnd("ci", "asked true;refused the user did not answer within 10m0s")
	// Allowed after the plugin's permissions were refused: nothing starts.
	if err := r.run("ci"); err != nil {
		t.Fatal(err)
	}
	ask := r.ask("ci")
	if _, err := r.act(&protocol.PluginAction{PluginId: "ci", Op: protocol.PluginDeny}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.act(&protocol.PluginAction{PluginId: "ci", Op: protocol.PluginAllowAgent, Key: ask.GetId(), Value: protocol.AgentOnce}); err == nil {
		t.Fatal("an ask of a refused plugin was allowed")
	}
	healthy(t, r)
	if agents.starts() != 0 {
		t.Fatalf("%d agents started", agents.starts())
	}
}

func TestPluginMessagesAndStartsOutOfBoundsAreRefused(t *testing.T) {
	agents := &fakeAgents{}
	r := agentsRig(t, agents)
	cases := map[string]struct{ load, want string }{
		"empty":   {`await mako.messages.sendMessage({ sessionId: "s1", text: "  " })`, "a message is text"},
		"big":     {`await mako.messages.sendMessage({ sessionId: "s1", text: "x".repeat(40000) })`, "a message is text"},
		"nowhere": {`await mako.messages.sendMessage({ sessionId: "s2", text: "hi" })`, "takes no messages"},
		"flood":   {`for (let i = 0; i < 40; i++) await mako.messages.sendMessage({ sessionId: "s1", text: "hi" })`, "messages a minute"},
		"project": {`await mako.messages.startAgent({ agent: "claude", project: "p1", root: "/etc", prompt: "x" })`, "knows no project"},
		"prompt":  {`await mako.messages.startAgent({ agent: "claude", project: "p1", root: "/proj", prompt: "" })`, "a prompt is text"},
		"noagent": {`await mako.messages.startAgent({ project: "p1", root: "/proj", prompt: "x" })`, "name the agent"},
		"spoof":   {`await mako.messages.startAgent({ agent: "claude", project: "p1", root: "/proj", prompt: "x", name: "a‮b" })`, "reordering"},
		"stop":    {`await mako.messages.stopAgent({ sessionId: "s1" })`, "only the agents it started"},
		"hourly":  {`for (let i = 0; i < 12; i++) { await mako.messages.startAgent({ agent: "claude", project: "p1", root: "/proj", prompt: "x" }).catch((e) => { if (!/waits for the user/.test(e.message)) throw e }) }`, "in the last hour"},
	}
	for id, c := range cases {
		r.write(id, map[string]string{ManifestName: manifest(id, []string{"sessions:write"}, ""), "main.js": block(c.load+`; s.msg = "got through"`, `ui.text(s.msg)`)})
		r.state(id, protocol.PluginWaiting)
		r.grant(id)
		tree := r.watch("plugin:"+id+"/ui").until(c.want, func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return hasText(n, "error:") })
		if !hasText(tree, c.want) {
			t.Errorf("%s: %v", id, texts(tree))
		}
	}
	// What did go through is headed as the plugin's.
	agents.mu.Lock()
	sent := strings.Join(agents.sent, "\n")
	agents.mu.Unlock()
	if !strings.HasPrefix(sent, "flood>s1: hi") {
		t.Fatalf("sent %q", sent)
	}
	if agents.starts() != 0 {
		t.Fatal("an agent started without the user")
	}
	healthy(t, r)
}
