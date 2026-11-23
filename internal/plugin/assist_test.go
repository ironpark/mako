package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/plugin/hostapi"
	"github.com/ironpark/mako/internal/protocol"
)

func TestAssistPermissionsDenied(t *testing.T) {
	h := &host{}
	methods := []struct {
		name string
		perm string
	}{
		{"decision.status", hostapi.PermDecision},
		{"decision.evaluate", hostapi.PermDecision},
		{"decision.submit", hostapi.PermDecision},
		{"agents.listAgents", hostapi.PermAgentsAsk},
		{"agents.askAgent", hostapi.PermAgentsAsk},
		{"agents.cancelAsk", hostapi.PermAgentsAsk},
	}
	for _, tc := range methods {
		_, err := hostapi.Dispatch(context.Background(), grants{}, h, tc.name, []byte(`{}`))
		var denied *hostapi.DeniedError
		if !errors.As(err, &denied) || denied.Permission != tc.perm {
			t.Errorf("%s without permission: got %v, want DeniedError(%s)", tc.name, err, tc.perm)
		}
	}
}

func TestAssistDecisionNilAndEvaluate(t *testing.T) {
	// 1. Decisions == nil -> unavailable.
	rNil := startRig(t, Config{Limits: testLimits})
	rNil.write("dec-nil", map[string]string{
		ManifestName: manifest("dec-nil", []string{"decision", "storage"}, ""),
		"main.js": `let mako;
export default {
  activate(m) {
    mako = m;
    m.blocks.register("ui", { state: {}, render: (s, ui) => ui.text("ok") });
    m.commands.register("go", async () => {
      const st = await mako.decision.status();
      const tr = await mako.decision.tryEvaluate({ state: "x", type: "noul", instructions: "ok?" });
      await mako.storage.setItem({ key: "out", value: JSON.stringify({ st, tr }) });
    });
  },
};`,
	})
	rNil.state("dec-nil", protocol.PluginWaiting)
	rNil.grant("dec-nil")
	if err := rNil.run("dec-nil"); err != nil {
		t.Fatal(err)
	}
	pNil, _ := rNil.m.plugin("dec-nil")
	raw, ok, _ := pNil.store.getScoped("", "out")
	if !ok {
		t.Fatal("missing stored output")
	}
	var nilOut struct {
		St struct {
			Available bool   `json:"available"`
			Provider  string `json:"provider"`
		} `json:"st"`
		Tr struct {
			Ok          bool   `json:"ok"`
			Unavailable bool   `json:"unavailable"`
			Error       string `json:"error"`
		} `json:"tr"`
	}
	if err := json.Unmarshal([]byte(raw), &nilOut); err != nil {
		t.Fatal(err)
	}
	if nilOut.St.Available || nilOut.Tr.Ok || !nilOut.Tr.Unavailable || !strings.Contains(nilOut.Tr.Error, "decision unavailable") {
		t.Fatalf("unexpected nil decision output: %+v", nilOut)
	}

	// 2. Decisions present -> status, provider/model stripping, helpers, bounds, rate limit.
	dec := &fakeDecisions{
		status: DecisionStatus{Available: true, Provider: "typesafe", Model: "jev"},
		respond: func(req []byte) ([]byte, error) {
			var m map[string]any
			if err := json.Unmarshal(req, &m); err != nil {
				return nil, err
			}
			if _, has := m["provider"]; has {
				return nil, errors.New("provider was not stripped")
			}
			if _, has := m["model"]; has {
				return nil, errors.New("model was not stripped")
			}
			return []byte(`{"provider":"typesafe","model":"jev","answers":{"decision":{"type":"noul","yes":true,"probability":0.9}},"yes":true,"probability":0.9}`), nil
		},
	}
	r := startRig(t, Config{Limits: testLimits, Decisions: dec})
	r.write("dec-ok", map[string]string{
		ManifestName: manifest("dec-ok", []string{"decision", "storage"}, ""),
		"main.js": `let mako;
export default {
  activate(m) {
    mako = m;
    m.blocks.register("ui", { state: {}, render: (s, ui) => ui.text("ok") });
    m.commands.register("go", async () => {
      const st = await mako.decision.status();
      const ev = await mako.decision.evaluate({ provider: "openai", model: "gpt-4o", state: "s", type: "noul", instructions: "yes?" });
      const y = await mako.decision.yes("s", "yes?");
      let tooBig = "";
      try { await mako.decision.evaluate("x".repeat(260 * 1024)); } catch (e) { tooBig = e.message; }
      const qs = {};
      for (let i = 0; i < 33; i++) qs["q" + i] = { type: "noul", instructions: "ok" };
      let tooMany = "";
      try { await mako.decision.evaluate({ state: "s", questions: qs }); } catch (e) { tooMany = e.message; }
      let rateErr = "";
      try {
        for (let i = 0; i < 65; i++) await mako.decision.evaluate({ state: "s", type: "noul", instructions: "ok" });
      } catch (e) { rateErr = e.message; }
      await mako.storage.setItem({ key: "out", value: JSON.stringify({ st, ev, y, tooBig, tooMany, rateErr }) });
    });
  },
};`,
	})
	r.state("dec-ok", protocol.PluginWaiting)
	r.grant("dec-ok")
	if err := r.run("dec-ok"); err != nil {
		t.Fatal(err)
	}
	p, _ := r.m.plugin("dec-ok")
	raw, ok, _ = p.store.getScoped("", "out")
	if !ok {
		t.Fatal("missing stored output")
	}
	var out struct {
		St struct {
			Available bool   `json:"available"`
			Provider  string `json:"provider"`
			Model     string `json:"model"`
		} `json:"st"`
		Ev struct {
			Provider  string `json:"provider"`
			Model     string `json:"model"`
			Yes       bool   `json:"yes"`
			ElapsedMs int    `json:"elapsedMs"`
		} `json:"ev"`
		Y       bool   `json:"y"`
		TooBig  string `json:"tooBig"`
		TooMany string `json:"tooMany"`
		RateErr string `json:"rateErr"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	if !out.St.Available || out.St.Provider != "typesafe" || out.St.Model != "jev" {
		t.Fatalf("status = %+v", out.St)
	}
	if !out.Ev.Yes || !out.Y || out.Ev.Provider != "typesafe" {
		t.Fatalf("evaluate/yes = %+v, %v", out.Ev, out.Y)
	}
	if !strings.Contains(out.TooBig, "256 KiB") {
		t.Fatalf("tooBig = %q", out.TooBig)
	}
	if !strings.Contains(out.TooMany, "32") {
		t.Fatalf("tooMany = %q", out.TooMany)
	}
	if !strings.Contains(out.RateErr, "60 times a minute") {
		t.Fatalf("rateErr = %q", out.RateErr)
	}
}

func TestAssistDecisionSubmit(t *testing.T) {
	blk := make(chan struct{})
	dec := &fakeDecisions{
		status: DecisionStatus{Available: true, Provider: "typesafe", Model: "jev"},
		block:  blk,
		respond: func(req []byte) ([]byte, error) {
			if strings.Contains(string(req), "fail-unavail") {
				return nil, fmt.Errorf("%w: no key", ErrDecisionUnavailable)
			}
			if strings.Contains(string(req), "fail-err") {
				return nil, errors.New("upstream 500")
			}
			return []byte(`{"answers":{"q":{"choice":"a"}}}`), nil
		},
	}
	r := startRig(t, Config{Limits: testLimits, Decisions: dec})
	r.write("dec-sub", map[string]string{
		ManifestName: manifest("dec-sub", []string{"decision", "storage"}, ""),
		"main.js": `let mako;
async function note(line) {
  const kept = await mako.storage.getItem({ key: "seen" });
  await mako.storage.setItem({ key: "seen", value: (kept.found ? kept.value + ";" : "") + line });
}
export default {
  activate(m) {
    mako = m;
    m.blocks.register("ui", { state: { last: "" }, render: (s, ui) => ui.text(s.last || "idle") });
    m.commands.register("go", async () => {
      const r1 = await mako.decision.submit({ state: "ok", type: "choice", options: ["a"] }, { tag: "t1" });
      const r2 = await mako.decision.submit({ state: "fail-unavail", type: "choice", options: ["a"] }, { tag: "t2" });
      let third = "";
      try {
        await mako.decision.submit({ state: "third", type: "choice", options: ["a"] }, { tag: "t3" });
      } catch (e) { third = e.message; }
      await note("sub:" + Boolean(r1.requestId) + ":" + Boolean(r2.requestId) + ":" + third);
    });
  },
  on: {
    decisionAnswered: async (ev) => {
      mako.blocks.state("ui").last = "ans:" + ev.tag;
      await note("ans:" + ev.tag + ":" + JSON.parse(ev.responseJson).answers.q.choice);
    },
    decisionFailed: async (ev) => {
      await note("fail:" + ev.tag + ":" + ev.code);
    },
  },
};`,
	})
	r.state("dec-sub", protocol.PluginWaiting)
	r.grant("dec-sub")
	w := r.watch("plugin:dec-sub/ui")
	w.until("idle", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return hasText(n, "idle") })

	if err := r.run("dec-sub"); err != nil {
		t.Fatal(err)
	}
	// Release the two in-flight submits.
	close(blk)

	// Wait for block re-render triggered by decisionAnswered event.
	w.until("ans:t1", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return hasText(n, "ans:t1") })

	p, _ := r.m.plugin("dec-sub")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		v, _, _ := p.store.getScoped("", "seen")
		if strings.Contains(v, "ans:t1:a") && strings.Contains(v, "fail:t2:unavailable") && strings.Contains(v, "at most 2") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	v, _, _ := p.store.getScoped("", "seen")
	t.Fatalf("unexpected seen log: %q", v)
}

func TestAssistAgentsAskLifecycle(t *testing.T) {
	asksHost := &fakeAsks{
		choices: []AgentChoice{
			{Agent: "claude", Profile: "explorer", Description: "Fast explorer", Model: "sonnet", Installed: true, Askable: true},
			{Agent: "claude", Profile: "writer", Description: "Writer", Model: "opus", Installed: true, Askable: true},
			{Agent: "codex", Profile: "disabled", Installed: true, Askable: false, Reason: "not allowed for automation (allow_template is off)"},
		},
		answer: func(q AgentQuestion) (string, error) {
			return fmt.Sprintf(`{"answer":"from %s/%s in %s (%s)"}`, q.Agent, q.Profile, q.Root, q.Format), nil
		},
	}
	r := startRig(t, Config{
		Limits: testLimits,
		Asks:   asksHost,
		Projects: func() []ProjectRoots {
			return []ProjectRoots{{ID: "p1", Name: "Alpha", Roots: []string{"/alpha", "/alpha-2"}}}
		},
		ResolveScope: func(scopeKey string) ScopeContext {
			if scopeKey == "project:p1" {
				return ScopeContext{Scope: "project", ScopeKey: scopeKey, ProjectID: "p1", ProjectName: "Alpha", Roots: []string{"/alpha", "/alpha-2"}}
			}
			return ScopeContext{Scope: "global"}
		},
	})

	r.write("ask-p", map[string]string{
		ManifestName: manifest("ask-p", []string{"agents:ask", "storage"}, `
[[contributes.command]]
id = "list"
title = "List"

[[contributes.command]]
id = "ask2"
title = "Ask Writer"
`),
		"main.js": `let mako;
async function note(line) {
  const kept = await mako.storage.getItem({ key: "seen" });
  await mako.storage.setItem({ key: "seen", value: (kept.found ? kept.value + ";" : "") + line });
}
export default {
  activate(m) {
    mako = m;
    m.blocks.register("ui", { state: {}, render: (s, ui) => ui.text("ok") });
    m.commands.register("list", async () => {
      const list = await mako.agents.list();
      await mako.storage.setItem({ key: "list", value: JSON.stringify(list) });
    });
    m.commands.register("go", async (ctx) => {
      const r = await ctx.agents.ask({
        agent: "claude",
        profile: "explorer",
        prompt: "What is storage?",
        format: "json",
        jsonSchema: { type: "object" },
        title: "OKF: What is storage?",
        tag: "turn-1",
      });
      await note("asked:" + r.asked);
    });
    m.commands.register("ask2", async (ctx) => {
      const r = await ctx.agents.ask({
        agent: "claude",
        profile: "writer",
        prompt: "Another question",
        tag: "turn-2",
      });
      await note("asked2:" + r.asked);
    });
  },
  on: {
    agentAnswered: (ev) => note("ans:" + ev.tag + ":" + ev.output + ":" + ev.truncated),
    agentAskFailed: (ev) => note("fail:" + ev.tag + ":" + ev.code),
  },
};`,
	})
	r.state("ask-p", protocol.PluginWaiting)
	r.grant("ask-p")

	// Check list().
	if _, err := r.act(&protocol.PluginAction{PluginId: "ask-p", Op: protocol.PluginRun, Command: "list"}); err != nil {
		t.Fatal(err)
	}
	p, _ := r.m.plugin("ask-p")
	listJSON, _, _ := p.store.getScoped("", "list")
	if !strings.Contains(listJSON, `"label":"claude · explorer"`) || !strings.Contains(listJSON, `"askable":false`) || !strings.Contains(listJSON, `allow_template is off`) {
		t.Fatalf("list output = %s", listJSON)
	}

	// First ask in project:p1 scope (empty root defaults to "/alpha").
	if _, err := r.act(&protocol.PluginAction{PluginId: "ask-p", Op: protocol.PluginRun, Command: "go", Scope: "project:p1"}); err != nil {
		t.Fatal(err)
	}
	ask := r.ask("ask-p")
	if ask.GetKind() != "ask" || ask.GetAgent() != "claude" || ask.GetProfile() != "explorer" || ask.GetRoot() != "/alpha" || ask.GetName() != "OKF: What is storage?" {
		t.Fatalf("unexpected ask info: %+v", ask)
	}

	// Allow once -> runs fakeAsks and delivers agentAnswered.
	if _, err := r.act(&protocol.PluginAction{PluginId: "ask-p", Op: protocol.PluginAllowAgent, Key: ask.GetId(), Value: protocol.AgentOnce}); err != nil {
		t.Fatal(err)
	}
	r.waitSeen("ask-p", `asked:true;ans:turn-1:{"answer":"from claude/explorer in /alpha (json)"}:false`)
	qs := asksHost.recordedAsks()
	if len(qs) != 1 || qs[0].Root != "/alpha" || qs[0].Format != "json" || qs[0].Timeout != askDefaultFor || qs[0].JSONSchema != `{"type":"object"}` {
		t.Fatalf("recorded asks = %+v", qs)
	}

	// Next ask still prompts; Deny -> agentAskFailed code=refused.
	if _, err := r.act(&protocol.PluginAction{PluginId: "ask-p", Op: protocol.PluginRun, Command: "go", Scope: "project:p1"}); err != nil {
		t.Fatal(err)
	}
	ask = r.ask("ask-p")
	if _, err := r.act(&protocol.PluginAction{PluginId: "ask-p", Op: protocol.PluginDenyAgent, Key: ask.GetId()}); err != nil {
		t.Fatal(err)
	}
	r.waitSeenEnd("ask-p", "asked:true;fail:turn-1:refused")

	// Allow always for claude/explorer -> stored in AsksAllowed.
	if _, err := r.act(&protocol.PluginAction{PluginId: "ask-p", Op: protocol.PluginRun, Command: "go", Scope: "project:p1"}); err != nil {
		t.Fatal(err)
	}
	ask = r.ask("ask-p")
	if _, err := r.act(&protocol.PluginAction{PluginId: "ask-p", Op: protocol.PluginAllowAgent, Key: ask.GetId(), Value: protocol.AgentAlways}); err != nil {
		t.Fatal(err)
	}
	r.waitSeenEnd("ask-p", `asked:true;ans:turn-1:{"answer":"from claude/explorer in /alpha (json)"}:false`)

	// PluginInfo.AgentsAllowed is true, and list() marks explorer alwaysAllowed=true but writer false.
	if !r.info("ask-p", func(*protocol.PluginInfo) bool { return true }).GetAgentsAllowed() {
		t.Fatal("AgentsAllowed should be true after always-allow ask")
	}
	if _, err := r.act(&protocol.PluginAction{PluginId: "ask-p", Op: protocol.PluginRun, Command: "list"}); err != nil {
		t.Fatal(err)
	}
	listJSON, _, _ = p.store.getScoped("", "list")
	var listed []struct {
		Profile       string `json:"profile"`
		AlwaysAllowed bool   `json:"alwaysAllowed"`
	}
	_ = json.Unmarshal([]byte(listJSON), &listed)
	for _, it := range listed {
		if it.Profile == "explorer" && !it.AlwaysAllowed {
			t.Fatal("explorer should have alwaysAllowed=true")
		}
		if it.Profile == "writer" && it.AlwaysAllowed {
			t.Fatal("writer should have alwaysAllowed=false")
		}
	}

	// Subsequent explorer ask runs without prompting (asked:false).
	if _, err := r.act(&protocol.PluginAction{PluginId: "ask-p", Op: protocol.PluginRun, Command: "go", Scope: "project:p1"}); err != nil {
		t.Fatal(err)
	}
	r.waitSeenEnd("ask-p", `asked:false;ans:turn-1:{"answer":"from claude/explorer in /alpha (json)"}:false`)

	// Different profile (writer) still asks!
	if _, err := r.act(&protocol.PluginAction{PluginId: "ask-p", Op: protocol.PluginRun, Command: "ask2", Scope: "project:p1"}); err != nil {
		t.Fatal(err)
	}
	ask2 := r.ask("ask-p")
	if ask2.GetProfile() != "writer" {
		t.Fatalf("expected writer ask, got %+v", ask2)
	}
	if _, err := r.act(&protocol.PluginAction{PluginId: "ask-p", Op: protocol.PluginDenyAgent, Key: ask2.GetId()}); err != nil {
		t.Fatal(err)
	}
	r.waitSeenEnd("ask-p", "asked2:true;fail:turn-2:refused")

	// Forget agents clears AsksAllowed -> explorer asks again.
	if _, err := r.act(&protocol.PluginAction{PluginId: "ask-p", Op: protocol.PluginForgetAgents}); err != nil {
		t.Fatal(err)
	}
	if r.info("ask-p", func(*protocol.PluginInfo) bool { return true }).GetAgentsAllowed() {
		t.Fatal("AgentsAllowed should be false after forget_agents")
	}
	if _, err := r.act(&protocol.PluginAction{PluginId: "ask-p", Op: protocol.PluginRun, Command: "go", Scope: "project:p1"}); err != nil {
		t.Fatal(err)
	}
	r.ask("ask-p")

	// Revoke (deny) clears everything and cancels pending ask.
	if _, err := r.act(&protocol.PluginAction{PluginId: "ask-p", Op: protocol.PluginDeny}); err != nil {
		t.Fatal(err)
	}
	r.info("ask-p", func(info *protocol.PluginInfo) bool { return info.GetAgentAsk() == nil })
}

func TestAssistAgentsAskBoundsCancelTimeoutAndTruncation(t *testing.T) {
	origMin := askMinFor
	askMinFor = 30 * time.Millisecond
	t.Cleanup(func() { askMinFor = origMin })

	blk := make(chan struct{})
	asksHost := &fakeAsks{
		choices: []AgentChoice{
			{Agent: "claude", Profile: "", Installed: true, Askable: true},
			{Agent: "codex", Profile: "off", Installed: true, Askable: false, Reason: "allow_template is off"},
		},
		block: blk,
		answer: func(q AgentQuestion) (string, error) {
			if strings.Contains(q.Prompt, "huge") {
				return strings.Repeat("a", 70*1024), nil
			}
			return "short", nil
		},
	}
	r := startRig(t, Config{
		Limits:   testLimits,
		Asks:     asksHost,
		Projects: func() []ProjectRoots { return []ProjectRoots{{ID: "p1", Name: "P1", Roots: []string{"/p1"}}} },
	})

	r.write("ask-b", map[string]string{
		ManifestName: manifest("ask-b", []string{"agents:ask", "storage"}, `
[[contributes.command]]
id = "cancel-wait"
title = "Cancel Wait"

[[contributes.command]]
id = "run-two"
title = "Run Two"

[[contributes.command]]
id = "timeout"
title = "Timeout"

[[contributes.command]]
id = "huge"
title = "Huge"

[[contributes.command]]
id = "bounds"
title = "Bounds"
`),
		"main.js": `let mako;
async function note(line) {
  const kept = await mako.storage.getItem({ key: "seen" });
  await mako.storage.setItem({ key: "seen", value: (kept.found ? kept.value + ";" : "") + line });
}
export default {
  activate(m) {
    mako = m;
    m.blocks.register("ui", { state: {}, render: (s, ui) => ui.text("ok") });
    m.commands.register("cancel-wait", async () => {
      const r = await mako.agents.ask({ agent: "claude", project: "p1", root: "/p1", prompt: "hello", tag: "cw" });
      const c = await mako.agents.cancel(r.requestId);
      await note("cw:" + r.asked + ":" + c.found);
    });
    m.commands.register("run-two", async () => {
      const r1 = await mako.agents.ask({ agent: "claude", project: "p1", root: "/p1", prompt: "one", tag: "r1" });
      const r2 = await mako.agents.ask({ agent: "claude", project: "p1", root: "/p1", prompt: "two", tag: "r2" });
      let third = "";
      try {
        await mako.agents.ask({ agent: "claude", project: "p1", root: "/p1", prompt: "three", tag: "r3" });
      } catch (e) { third = e.message; }
      const c1 = await mako.agents.cancel(r1.requestId);
      await note("two:" + Boolean(r1.requestId) + ":" + Boolean(r2.requestId) + ":" + c1.found + ":" + third);
    });
    m.commands.register("timeout", async () => {
      await mako.agents.ask({ agent: "claude", project: "p1", root: "/p1", prompt: "slow", timeoutMs: 40, tag: "to" });
    });
    m.commands.register("huge", async () => {
      await mako.agents.ask({ agent: "claude", project: "p1", root: "/p1", prompt: "huge", tag: "hg" });
    });
    m.commands.register("bounds", async () => {
      const errs = {};
      try { await mako.agents.ask({ agent: "codex", profile: "off", project: "p1", root: "/p1", prompt: "x" }); } catch (e) { errs.unask = e.message; }
      try { await mako.agents.ask({ agent: "claude", project: "p1", root: "/etc", prompt: "x" }); } catch (e) { errs.root = e.message; }
      try {
        for (let i = 0; i < 32; i++) {
          await mako.agents.ask({ agent: "claude", project: "p1", root: "/p1", prompt: "x" }).catch((e) => {
            if (!/at most 2|waits for the user/.test(e.message)) throw e;
          });
        }
      } catch (e) { errs.rate = e.message; }
      await mako.storage.setItem({ key: "bounds", value: JSON.stringify(errs) });
    });
  },
  on: {
    agentAnswered: (ev) => note("ans:" + ev.tag + ":" + ev.output.length + ":" + ev.truncated),
    agentAskFailed: (ev) => note("fail:" + ev.tag + ":" + ev.code),
  },
};`,
	})
	r.state("ask-b", protocol.PluginWaiting)
	r.grant("ask-b")

	// 1. Cancel waiting ask.
	if _, err := r.act(&protocol.PluginAction{PluginId: "ask-b", Op: protocol.PluginRun, Command: "cancel-wait"}); err != nil {
		t.Fatal(err)
	}
	r.waitSeenEnd("ask-b", "fail:cw:canceled")

	// Pre-grant claude/ for fast running tests.
	if err := r.m.grant.update("ask-b", func(e *grantEntry) { e.AsksAllowed = []string{"claude/"} }); err != nil {
		t.Fatal(err)
	}

	// 2. Run two concurrently -> 3rd rejected, cancel 1st running -> canceled, disable cancels 2nd running.
	if _, err := r.act(&protocol.PluginAction{PluginId: "ask-b", Op: protocol.PluginRun, Command: "run-two"}); err != nil {
		t.Fatal(err)
	}
	r.waitSeenEnd("ask-b", "fail:r1:canceled")
	p, _ := r.m.plugin("ask-b")
	seen, _, _ := p.store.getScoped("", "seen")
	if !strings.Contains(seen, "at most 2") {
		t.Fatalf("expected concurrency error in %q", seen)
	}
	// Disable plugin cancels the remaining running ask (r2).
	if _, err := r.act(&protocol.PluginAction{PluginId: "ask-b", Op: protocol.PluginDisable}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		p.pendingMu.Lock()
		n := len(p.askJobs)
		p.pendingMu.Unlock()
		if n == 0 {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	p.pendingMu.Lock()
	left := len(p.askJobs)
	p.pendingMu.Unlock()
	if left != 0 {
		t.Fatalf("disable did not cancel running ask; %d jobs left", left)
	}
	if _, err := r.act(&protocol.PluginAction{PluginId: "ask-b", Op: protocol.PluginEnable}); err != nil {
		t.Fatal(err)
	}

	// 3. Timeout while blocked -> code=timeout.
	if _, err := r.act(&protocol.PluginAction{PluginId: "ask-b", Op: protocol.PluginRun, Command: "timeout"}); err != nil {
		t.Fatal(err)
	}
	r.waitSeenEnd("ask-b", "fail:to:timeout")

	// 4. Unblock and test 70 KiB output truncation to 64 KiB + truncated=true.
	close(blk)
	if _, err := r.act(&protocol.PluginAction{PluginId: "ask-b", Op: protocol.PluginRun, Command: "huge"}); err != nil {
		t.Fatal(err)
	}
	r.waitSeenEnd("ask-b", fmt.Sprintf("ans:hg:%d:true", maxAskOutput))

	// 5. Bounds: unaskable reason, bad root, hourly rate limit (30/hr).
	if _, err := r.act(&protocol.PluginAction{PluginId: "ask-b", Op: protocol.PluginRun, Command: "bounds"}); err != nil {
		t.Fatal(err)
	}
	raw, _, _ := p.store.getScoped("", "bounds")
	var errs struct {
		Unask string `json:"unask"`
		Root  string `json:"root"`
		Rate  string `json:"rate"`
	}
	_ = json.Unmarshal([]byte(raw), &errs)
	if !strings.Contains(errs.Unask, "allow_template is off") {
		t.Fatalf("unaskable err = %q", errs.Unask)
	}
	if !strings.Contains(errs.Root, "knows no project") {
		t.Fatalf("bad root err = %q", errs.Root)
	}
	if !strings.Contains(errs.Rate, "30 times in the last hour") {
		t.Fatalf("rate err = %q", errs.Rate)
	}
}

func TestAssistAgentsAskStartedEvent(t *testing.T) {
	asksHost := &fakeAsks{
		choices: []AgentChoice{
			{Agent: "claude", Profile: "explorer", Installed: true, Askable: true},
		},
		answer: func(q AgentQuestion) (string, error) {
			return "ok:" + q.Prompt, nil
		},
	}
	r := startRig(t, Config{
		Limits:   testLimits,
		Asks:     asksHost,
		Projects: func() []ProjectRoots { return []ProjectRoots{{ID: "p1", Name: "Alpha", Roots: []string{"/alpha"}}} },
	})
	r.write("ask-start", map[string]string{
		ManifestName: manifest("ask-start", []string{"agents:ask", "storage"}, `
[[contributes.command]]
id = "go2"
title = "Go 2"

[[contributes.command]]
id = "go3"
title = "Go 3"
`),
		"main.js": `let mako;
async function push(entry) {
  const kept = await mako.storage.getItem({ key: "events" });
  const arr = kept.found ? JSON.parse(kept.value) : [];
  arr.push(entry);
  await mako.storage.setItem({ key: "events", value: JSON.stringify(arr) });
}
export default {
  activate(m) {
    mako = m;
    m.blocks.register("ui", { state: {}, render: (s, ui) => ui.text("ok") });
    m.commands.register("go", async () => {
      const res = await mako.agents.ask({ agent: "claude", profile: "explorer", project: "p1", root: "/alpha", prompt: "q1", tag: "t-once" });
      await push({ kind: "req", requestId: res.requestId, asked: res.asked, tag: "t-once" });
    });
    m.commands.register("go2", async () => {
      const res = await mako.agents.ask({ agent: "claude", profile: "explorer", project: "p1", root: "/alpha", prompt: "q2", tag: "t-always" });
      await push({ kind: "req", requestId: res.requestId, asked: res.asked, tag: "t-always" });
    });
    m.commands.register("go3", async () => {
      const res = await mako.agents.ask({ agent: "claude", profile: "explorer", project: "p1", root: "/alpha", prompt: "q3", tag: "t-fast" });
      await push({ kind: "req", requestId: res.requestId, asked: res.asked, tag: "t-fast" });
    });
  },
  on: {
    agentAskStarted: (ev) => push({ kind: ev.kind, requestId: ev.requestId, tag: ev.tag, agent: ev.agent, profile: ev.profile }),
    agentAnswered: (ev) => push({ kind: ev.kind, requestId: ev.requestId, tag: ev.tag, output: ev.output }),
  },
};`,
	})
	r.state("ask-start", protocol.PluginWaiting)
	r.grant("ask-start")

	type evRec struct {
		Kind      string `json:"kind"`
		RequestId string `json:"requestId"`
		Asked     bool   `json:"asked"`
		Tag       string `json:"tag"`
		Agent     string `json:"agent"`
		Profile   string `json:"profile"`
		Output    string `json:"output"`
	}
	p, _ := r.m.plugin("ask-start")
	waitEvents := func(wantLen int) []evRec {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if raw, ok, _ := p.store.getScoped("", "events"); ok {
				var arr []evRec
				if json.Unmarshal([]byte(raw), &arr) == nil && len(arr) >= wantLen {
					return arr
				}
			}
			time.Sleep(15 * time.Millisecond)
		}
		raw, _, _ := p.store.getScoped("", "events")
		t.Fatalf("timed out waiting for %d events; got %s", wantLen, raw)
		return nil
	}

	// 1. Pending ask -> Allow once -> agentAskStarted precedes agentAnswered.
	if _, err := r.act(&protocol.PluginAction{PluginId: "ask-start", Op: protocol.PluginRun, Command: "go"}); err != nil {
		t.Fatal(err)
	}
	ask1 := r.ask("ask-start")
	if _, err := r.act(&protocol.PluginAction{PluginId: "ask-start", Op: protocol.PluginAllowAgent, Key: ask1.GetId(), Value: protocol.AgentOnce}); err != nil {
		t.Fatal(err)
	}
	evs := waitEvents(3)
	if !evs[0].Asked || evs[0].RequestId != ask1.GetId() {
		t.Fatalf("ev[0] = %+v", evs[0])
	}
	if evs[1].Kind != EventAgentAskStarted || evs[1].RequestId != ask1.GetId() || evs[1].Tag != "t-once" || evs[1].Agent != "claude" || evs[1].Profile != "explorer" {
		t.Fatalf("ev[1] = %+v, want agentAskStarted for %s", evs[1], ask1.GetId())
	}
	if evs[2].Kind != EventAgentAnswered || evs[2].RequestId != ask1.GetId() || evs[2].Tag != "t-once" || evs[2].Output != "ok:q1" {
		t.Fatalf("ev[2] = %+v, want agentAnswered for %s", evs[2], ask1.GetId())
	}

	// 2. Pending ask -> Allow always -> agentAskStarted precedes agentAnswered.
	if _, err := r.act(&protocol.PluginAction{PluginId: "ask-start", Op: protocol.PluginRun, Command: "go2"}); err != nil {
		t.Fatal(err)
	}
	ask2 := r.ask("ask-start")
	if _, err := r.act(&protocol.PluginAction{PluginId: "ask-start", Op: protocol.PluginAllowAgent, Key: ask2.GetId(), Value: protocol.AgentAlways}); err != nil {
		t.Fatal(err)
	}
	evs = waitEvents(6)
	if evs[4].Kind != EventAgentAskStarted || evs[4].RequestId != ask2.GetId() || evs[4].Tag != "t-always" || evs[4].Agent != "claude" || evs[4].Profile != "explorer" {
		t.Fatalf("ev[4] = %+v, want agentAskStarted for %s", evs[4], ask2.GetId())
	}
	if evs[5].Kind != EventAgentAnswered || evs[5].RequestId != ask2.GetId() || evs[5].Tag != "t-always" || evs[5].Output != "ok:q2" {
		t.Fatalf("ev[5] = %+v, want agentAnswered for %s", evs[5], ask2.GetId())
	}

	// 3. Already always-allowed (asked:false) -> directly agentAnswered without agentAskStarted.
	if _, err := r.act(&protocol.PluginAction{PluginId: "ask-start", Op: protocol.PluginRun, Command: "go3"}); err != nil {
		t.Fatal(err)
	}
	evs = waitEvents(8)
	if evs[6].Asked {
		t.Fatalf("ev[6] expected asked=false: %+v", evs[6])
	}
	if evs[7].Kind != EventAgentAnswered || evs[7].Tag != "t-fast" || evs[7].Output != "ok:q3" {
		t.Fatalf("ev[7] = %+v, want direct agentAnswered (no agentAskStarted)", evs[7])
	}
}

func TestAssistKillAndReloadCancelPending(t *testing.T) {
	waitDrained := func(t *testing.T, p *plugin) {
		t.Helper()
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			p.pendingMu.Lock()
			askN, decN := len(p.askJobs), len(p.decJobs)
			p.pendingMu.Unlock()
			if askN == 0 && decN == 0 && p.asks.get() == nil {
				return
			}
			time.Sleep(15 * time.Millisecond)
		}
		p.pendingMu.Lock()
		askN, decN := len(p.askJobs), len(p.decJobs)
		p.pendingMu.Unlock()
		t.Fatalf("pending work not canceled: askJobs=%d decJobs=%d waitingAsk=%v", askN, decN, p.asks.get() != nil)
	}

	pluginJS := func(tag string) string {
		return fmt.Sprintf(`// version %s
let mako;
export default {
  activate(m) {
    mako = m;
    m.blocks.register("ui", { state: {}, render: (s, ui) => ui.text("ver:%s") });
    m.commands.register("go", async () => {
      await mako.decision.submit({ state: "s", type: "choice", options: ["a"] }, { tag: "d1" });
      await mako.agents.ask({ agent: "claude", profile: "explorer", project: "p1", root: "/p1", prompt: "running", tag: "a1" });
      await mako.agents.ask({ agent: "claude", profile: "writer", project: "p1", root: "/p1", prompt: "waiting", tag: "a2" });
    });
    m.commands.register("boom", async () => {
      throw new Error("boom");
    });
  },
};`, tag, tag)
	}

	setup := func(t *testing.T, id string, lim Limits) (*rig, *plugin, chan struct{}, chan struct{}) {
		decBlk := make(chan struct{})
		askBlk := make(chan struct{})
		dec := &fakeDecisions{
			status:  DecisionStatus{Available: true, Provider: "typesafe", Model: "jev"},
			block:   decBlk,
			respond: func([]byte) ([]byte, error) { return []byte(`{"answers":{"q":{"choice":"a"}}}`), nil },
		}
		asksHost := &fakeAsks{
			choices: []AgentChoice{
				{Agent: "claude", Profile: "explorer", Installed: true, Askable: true},
				{Agent: "claude", Profile: "writer", Installed: true, Askable: true},
			},
			block:  askBlk,
			answer: func(AgentQuestion) (string, error) { return "ok", nil },
		}
		r := startRig(t, Config{
			Limits:    lim,
			Decisions: dec,
			Asks:      asksHost,
			Projects:  func() []ProjectRoots { return []ProjectRoots{{ID: "p1", Name: "P1", Roots: []string{"/p1"}}} },
		})
		r.write(id, map[string]string{
			ManifestName: manifest(id, []string{"decision", "agents:ask"}, `
[[contributes.command]]
id = "boom"
title = "Boom"
`),
			"main.js": pluginJS("v1"),
		})
		r.state(id, protocol.PluginWaiting)
		r.grant(id)
		// Pre-allow claude/explorer so it starts running immediately while claude/writer waits for approval.
		if err := r.m.grant.update(id, func(e *grantEntry) { e.AsksAllowed = []string{"claude/explorer"} }); err != nil {
			t.Fatal(err)
		}
		if _, err := r.act(&protocol.PluginAction{PluginId: id, Op: protocol.PluginRun, Command: "go"}); err != nil {
			t.Fatal(err)
		}
		p, _ := r.m.plugin(id)
		// Confirm all 3 pending pieces are active before triggering kill/reload.
		r.ask(id)
		p.pendingMu.Lock()
		askN, decN := len(p.askJobs), len(p.decJobs)
		p.pendingMu.Unlock()
		if askN != 1 || decN != 1 {
			t.Fatalf("expected 1 running ask + 1 running decision, got askJobs=%d decJobs=%d", askN, decN)
		}
		return r, p, decBlk, askBlk
	}

	t.Run("kill", func(t *testing.T) {
		lim := testLimits
		lim.Failures = 1
		r, p, decBlk, askBlk := setup(t, "kill-p", lim)
		defer close(decBlk)
		defer close(askBlk)

		if _, err := r.act(&protocol.PluginAction{PluginId: "kill-p", Op: protocol.PluginRun, Command: "boom"}); err == nil {
			t.Fatal("expected boom command to fail")
		}
		info := r.state("kill-p", protocol.PluginFailed)
		if info.GetAgentAsk() != nil {
			t.Fatalf("waiting ask survived kill(): %+v", info.GetAgentAsk())
		}
		waitDrained(t, p)
	})

	t.Run("reload", func(t *testing.T) {
		r, p, decBlk, askBlk := setup(t, "reload-p", testLimits)
		defer close(decBlk)
		defer close(askBlk)

		w := r.watch("plugin:reload-p/ui")
		w.until("ver:v1", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return hasText(n, "ver:v1") })

		// Modify plugin code to trigger reload().
		r.write("reload-p", map[string]string{"main.js": pluginJS("v2")})
		w.until("ver:v2", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return hasText(n, "ver:v2") })
		waitDrained(t, p)
		info := r.info("reload-p", func(i *protocol.PluginInfo) bool { return i.GetAgentAsk() == nil })
		if info.GetAgentAsk() != nil {
			t.Fatalf("waiting ask survived reload(): %+v", info.GetAgentAsk())
		}
	})
}
