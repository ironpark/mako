package gui

import (
	"encoding/json"
	"runtime"
	"slices"
	"testing"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

func TestProfileServersRoundTrip(t *testing.T) {
	none, some := []string{}, []string{"a", "b"}
	agents := map[string]agentspec.Agent{"claude": {Profiles: []agentspec.Profile{
		{Name: "all", Description: "Reviews Go concurrency.\n취소 처리와 경쟁 상태를 확인합니다."}, {Name: "none", ContextServers: &none}, {Name: "some", ContextServers: &some},
	}}}
	got, err := applyAgents(draftAgents(agents))
	if err != nil {
		t.Fatal(err)
	}
	// Saving and reopening must preserve an explicitly empty selection.
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	got = nil
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	ps := got["claude"].Profiles
	if len(ps) != 3 || ps[0].ContextServers != nil || ps[1].ContextServers == nil || len(*ps[1].ContextServers) != 0 ||
		ps[2].ContextServers == nil || !slices.Equal(*ps[2].ContextServers, some) {
		t.Fatalf("profiles %+v", ps)
	}
	if ps[0].Description != agents["claude"].Profiles[0].Description || ps[1].Description != "" {
		t.Fatalf("profile descriptions: %+v", ps)
	}
}

func TestProfileDescriptionEditor(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(&config.Config{Agents: map[string]agentspec.Agent{
		"claude": {Profiles: []agentspec.Profile{{Name: "reviewer", Description: "Reviews Go code."}}},
	}}, "")
	var profile agentDraft
	for _, r := range ggui.Peek(m.AgentDraft) {
		if r.Agent == "claude" && r.IsProfile {
			profile = r
		}
	}
	p := ggui.ProbeBuilder(func() ggui.Widget { return profileCard(m, profile, ggui.TextStyle{}) }, ggui.Sz(800, 700))
	defer m.shutdown()
	defer p.Close()
	p.Setup(func() { setup(m) })
	p.Frame()
	field, ok := p.Find(Tf("%s description", "claude:reviewer"))
	if !ok {
		t.Fatal("no profile description editor")
	}
	p.Click(field.Center())
	p.Type(ggui.Mods{Meta: runtime.GOOS == "darwin", Ctrl: runtime.GOOS != "darwin"}, ggui.KeyA)
	p.Text("취소 처리 검토\n경쟁 상태도 확인합니다.")
	p.Frame()
	got, err := applyAgents(ggui.Peek(m.AgentDraft))
	if err != nil {
		t.Fatal(err)
	}
	if got["claude"].Profiles[0].Description != "취소 처리 검토\n경쟁 상태도 확인합니다." {
		t.Fatalf("edited description: %+v", got["claude"].Profiles[0])
	}
}

func TestSessionTitleUsesCustomName(t *testing.T) {
	s := &protocol.SessionInfo{Agent: "claude", Profile: "work", Name: "API 작업"}
	if got := sessionTitle(s, "program title"); got != s.GetName() {
		t.Fatalf("custom title = %q", got)
	}
	s.Name = ""
	if got := sessionTitle(s, "program title"); got != "program title" {
		t.Fatalf("cleared title = %q", got)
	}
	if got := sessionTitle(s, ""); got != "claude · work" {
		t.Fatalf("fallback title = %q", got)
	}
}

func TestAnAgentsSwitchSurvivesTheDraft(t *testing.T) {
	off, on := false, true
	in := map[string]agentspec.Agent{"codex": {Enabled: &off}, "agy": {Enabled: &on}}
	out, err := applyAgents(draftAgents(in))
	if err != nil {
		t.Fatal(err)
	}
	if e := out["codex"].Enabled; e == nil || *e {
		t.Fatalf("codex turned off came back %v", e)
	}
	if e := out["agy"].Enabled; e == nil || !*e {
		t.Fatalf("agy turned on came back %v", e)
	}
	if e := out["claude"].Enabled; e != nil {
		t.Fatalf("claude, never switched, came back set to %v; want it to follow whether it is installed", *e)
	}
}

// The agents page lists each agent on a line, the shell not among them,
// and a line tapped unfolds its settings, one at a time.
func TestTheAgentsListUnfoldsOneAgent(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	cfg := config.Default()
	cfg.Agents = map[string]agentspec.Agent{"claude": {Profiles: []agentspec.Profile{{Name: "opus"}}}}
	m := newModel(cfg, "")
	p := ggui.ProbeBuilder(func() ggui.Widget {
		return ggui.Provide(ggui.ReducedMotionKey, true, widget.Scroll(agentSettings(m)))
	}, ggui.Sz(760, 2000))
	defer p.Close()
	p.Frame()
	if _, ok := p.Semantics().Find(ggui.RoleButton, Tf("%s settings", "shell")); ok {
		t.Fatal("the shell is listed among the agents")
	}
	if _, ok := p.Semantics().Find(ggui.RoleTextField, Tf("%s command", "claude")); ok {
		t.Fatal("an agent's settings show before it is opened")
	}
	click := func(name string) {
		b, ok := p.Semantics().Find(ggui.RoleButton, Tf("%s settings", name))
		if !ok {
			t.Fatalf("no line for %s:\n%s", name, p.Semantics().String())
		}
		p.Click(b.Rect.Origin.Add(ggui.Pt(20, b.Rect.Size.H/2)))
		p.Frame()
	}
	click("claude")
	if _, ok := p.Semantics().Find(ggui.RoleTextField, Tf("%s command", "claude")); !ok {
		t.Fatalf("claude's settings did not unfold:\n%s", p.Semantics().String())
	}
	if _, ok := p.Semantics().Find(ggui.RoleTextField, Tf("%s profile name", "claude")); !ok {
		t.Fatal("claude's profile is not under it")
	}
	click("codex")
	if _, ok := p.Semantics().Find(ggui.RoleTextField, Tf("%s command", "claude")); ok {
		t.Fatal("claude stayed open with codex opened")
	}
	if _, ok := p.Semantics().Find(ggui.RoleTextField, Tf("%s command", "codex")); !ok {
		t.Fatal("codex's settings did not unfold")
	}
}

func TestProfileEffortRoundTrip(t *testing.T) {
	in := map[string]agentspec.Agent{
		"claude": {
			Profiles: []agentspec.Profile{
				{Name: "deep", Model: "claude-opus-5-5", Effort: "xhigh"},
			},
		},
		"codex": {
			Profiles: []agentspec.Profile{
				{Name: "sol", Model: "gpt-6.2-sol", Effort: "ultra"},
			},
		},
	}
	got, err := applyAgents(draftAgents(in))
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	got = nil
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if cp := got["claude"].Profiles[0]; cp.Model != "claude-opus-5-5" || cp.Effort != "xhigh" {
		t.Fatalf("claude profile = %+v", cp)
	}
	if xp := got["codex"].Profiles[0]; xp.Model != "gpt-6.2-sol" || xp.Effort != "ultra" {
		t.Fatalf("codex profile = %+v", xp)
	}
}

func TestProfileCardShowsDynamicModelsAndEfforts(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	for _, tc := range []struct {
		agent  string
		model  string
		effort string
	}{
		{"claude", "claude-opus-5-5", "high"},
		{"codex", "gpt-6.2-sol", "ultra"},
		{"agy", "gemini-4-neon-high", "xhigh"},
	} {
		t.Run(tc.agent, func(t *testing.T) {
			m := newModel(&config.Config{Agents: map[string]agentspec.Agent{
				tc.agent: {Profiles: []agentspec.Profile{{Name: "p", Model: tc.model, Effort: tc.effort}}},
			}}, "")
			m.AgentCatalogs.Set(map[string][]*protocol.AgentModel{
				tc.agent: {
					{Id: tc.model, Name: tc.model, Efforts: []string{"low", tc.effort}},
				},
			})
			var profile agentDraft
			for _, r := range ggui.Peek(m.AgentDraft) {
				if r.Agent == tc.agent && r.IsProfile {
					profile = r
				}
			}
			const cardW = 540
			p := ggui.ProbeBuilder(func() ggui.Widget { return profileCard(m, profile, ggui.TextStyle{}) }, ggui.Sz(cardW, 700))
			defer p.Close()
			p.Frame()
			target := tc.agent + ":p"
			modelNode, ok := p.Find(Tf("%s model", target))
			if !ok {
				t.Fatalf("missing model selector for %s:\n%s", target, p.Semantics().String())
			}
			effortNode, ok := p.Find(Tf("%s effort", target))
			if !ok {
				t.Fatalf("missing effort selector for %s:\n%s", target, p.Semantics().String())
			}
			// Verify Model and Effort sit side-by-side cleanly inside the card without overflowing.
			mr, er := modelNode.Rect, effortNode.Rect
			if mr.Size.W < 100 || er.Size.W < 100 {
				t.Fatalf("collapsed selector width: model=%+v effort=%+v", mr, er)
			}
			if mr.Origin.X+mr.Size.W > er.Origin.X {
				t.Fatalf("model overlaps effort: model=%+v effort=%+v", mr, er)
			}
			if er.Origin.X+er.Size.W > cardW {
				t.Fatalf("effort overflows card (%dpx): effort=%+v", cardW, er)
			}
			if mr.Origin.Y != er.Origin.Y {
				t.Fatalf("model and effort misaligned vertically: model=%+v effort=%+v", mr, er)
			}
		})
	}
}

func TestHomeMigrateDialogRenders(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.HomeMigrate.TargetIsolate.Set(true)
	m.HomeMigrate.Info.Set(&protocol.AgentHomeInfo{
		Supported:     true,
		FromDir:       "/Users/me",
		ToDir:         "/Users/me/.mako/home",
		SavedSessions: 2,
		HasConfig:     true,
	})
	m.HomeMigrate.Open.Set(true)

	p := ggui.ProbeBuilder(func() ggui.Widget { return homeMigrateDialog(m) }, ggui.Sz(800, 600))
	defer p.Close()
	p.Frame()
	for _, want := range []string{
		T("Migrate & apply"),
		T("Switch without migrating"),
		T("Cancel"),
		T("Copy configuration and credentials"),
		Tf("Move Mako sessions (%d)", 2),
	} {
		if _, ok := p.Find(want); !ok {
			t.Fatalf("missing %q in homeMigrateDialog:\n%s", want, p.Semantics().String())
		}
	}
}

func TestYoloFlagsLiftAndRoundTrip(t *testing.T) {
	in := map[string]agentspec.Agent{
		"claude": {
			Args: []string{"--dangerously-skip-permissions", "--verbose"},
			Profiles: []agentspec.Profile{
				{Name: "p1", Args: []string{"--dangerously-skip-permissions"}},
			},
		},
		"codex": {
			Yolo: true,
			Profiles: []agentspec.Profile{
				{Name: "safe", Yolo: new(false)},
			},
		},
	}
	drafts := draftAgents(in)
	got, err := applyAgents(drafts)
	if err != nil {
		t.Fatal(err)
	}
	if !got["claude"].Yolo || !slices.Equal(got["claude"].Args, []string{"--verbose"}) {
		t.Fatalf("claude = %+v", got["claude"])
	}
	if cp := got["claude"].Profiles[0]; cp.Yolo == nil || !*cp.Yolo || len(cp.Args) != 0 {
		t.Fatalf("claude profile = %+v", cp)
	}
	if !got["codex"].Yolo {
		t.Fatalf("codex Yolo should be true")
	}
	if xp := got["codex"].Profiles[0]; xp.Yolo == nil || *xp.Yolo {
		t.Fatalf("codex safe profile = %+v", xp)
	}
}

func TestAgentEditorShowsYoloSwitch(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(&config.Config{Agents: map[string]agentspec.Agent{
		"claude": {Profiles: []agentspec.Profile{{Name: "p"}}},
	}}, "")
	var claudeDraft agentDraft
	var profiles []agentDraft
	for _, r := range ggui.Peek(m.AgentDraft) {
		if r.Agent == "claude" {
			if r.IsProfile {
				profiles = append(profiles, r)
			} else {
				claudeDraft = r
			}
		}
	}
	p := ggui.ProbeBuilder(func() ggui.Widget { return widget.Scroll(agentEditor(m, claudeDraft, profiles)) }, ggui.Sz(700, 900))
	defer p.Close()
	p.Frame()
	if _, ok := p.Find(Tf("%s YOLO", "claude")); !ok {
		t.Fatalf("missing YOLO switch for claude:\n%s", p.Semantics().String())
	}
	if _, ok := p.Find(Tf("%s YOLO", "claude:p")); !ok {
		t.Fatalf("missing YOLO override selector for claude:p:\n%s", p.Semantics().String())
	}
}

func TestBuiltinMCPToggleAndProfileServersIncludeMako(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	ids := m.mcpServerIDs()
	if !slices.Contains(ids, "mako") {
		t.Fatalf("mcpServerIDs() = %v, want built-in mako included", ids)
	}
	p := ggui.ProbeBuilder(func() ggui.Widget { return builtinMCPCard(m) }, ggui.Sz(700, 200))
	defer p.Close()
	p.Frame()
	sw, ok := p.Find(T("Built-in Mako MCP"))
	if !ok {
		t.Fatalf("missing Built-in Mako MCP switch:\n%s", p.Semantics().String())
	}
	if !ggui.Peek(m.ext.MCPServers)["mako"].On() {
		t.Fatal("expected built-in mako MCP enabled by default")
	}
	p.Click(sw.Center())
	p.Frame()
	srv, ok := m.cfg.ContextServers["mako"]
	if !ok || srv.On() {
		t.Fatalf("expected context_servers.mako disabled after toggle, got %+v (ok=%v)", srv, ok)
	}
	p.Click(sw.Center())
	p.Frame()
	if !m.cfg.ContextServers["mako"].On() {
		t.Fatalf("expected context_servers.mako re-enabled after second toggle, got %+v", m.cfg.ContextServers["mako"])
	}
}

func TestProfileExecutionSwitchesRoundTripAndToggle(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	off := false
	in := map[string]agentspec.Agent{
		"claude": {
			AllowAgent:    &off,
			AllowTemplate: &off,
			Profiles: []agentspec.Profile{
				{Name: "default-on"},
				{Name: "restricted", AllowAgent: &off, AllowTemplate: &off},
			},
		},
	}
	got, err := applyAgents(draftAgents(in))
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	got = nil
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got["claude"].AgentAllowed() || got["claude"].TemplateAllowed() {
		t.Fatalf("agent-level switches lost in round-trip: %+v", got["claude"])
	}
	ps := got["claude"].Profiles
	if len(ps) != 2 || !ps[0].AgentAllowed() || !ps[0].TemplateAllowed() || ps[1].AgentAllowed() || ps[1].TemplateAllowed() {
		t.Fatalf("round-trip profiles = %+v", ps)
	}

	m := newModel(&config.Config{Agents: map[string]agentspec.Agent{
		"claude": {Profiles: []agentspec.Profile{{Name: "reviewer"}}},
	}}, "")
	var agentRow, profile agentDraft
	for _, r := range ggui.Peek(m.AgentDraft) {
		if r.Agent == "claude" && !r.IsProfile {
			agentRow = r
		}
		if r.Agent == "claude" && r.IsProfile {
			profile = r
		}
	}
	p := ggui.ProbeBuilder(func() ggui.Widget { return widget.Scroll(agentEditor(m, agentRow, []agentDraft{profile})) }, ggui.Sz(700, 1600))
	defer p.Close()
	p.Frame()

	for _, label := range []string{
		Tf("%s allow agent spawning", "claude"),
		Tf("%s allow in templates", "claude"),
		Tf("%s allow agent spawning", "claude:reviewer"),
		Tf("%s allow in templates", "claude:reviewer"),
	} {
		sw, ok := p.Find(label)
		if !ok {
			t.Fatalf("missing switch %q:\n%s", label, p.Semantics().String())
		}
		p.Click(sw.Center())
		p.Frame()
	}

	after, err := applyAgents(ggui.Peek(m.AgentDraft))
	if err != nil {
		t.Fatal(err)
	}
	if after["claude"].AgentAllowed() || after["claude"].TemplateAllowed() {
		t.Fatalf("expected agent-level switches off after toggling, got %+v", after["claude"])
	}
	rp := after["claude"].Profiles[0]
	if rp.AgentAllowed() || rp.TemplateAllowed() {
		t.Fatalf("expected both profile switches off after toggling, got %+v", rp)
	}
}
