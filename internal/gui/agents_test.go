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
	if _, ok := p.Find(Tf("%s permission", "claude")); ok {
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
	if _, ok := p.Semantics().Find(ggui.RoleTextField, Tf("%s command", "claude")); ok {
		t.Fatal("built-in agent should not expose fixed command input")
	}
	if _, ok := p.Find(Tf("%s permission", "claude")); !ok {
		t.Fatalf("claude's settings did not unfold:\n%s", p.Semantics().String())
	}
	if _, ok := p.Semantics().Find(ggui.RoleTextField, Tf("%s profile name", "claude")); !ok {
		t.Fatal("claude's profile is not under it")
	}
	click("codex")
	if _, ok := p.Find(Tf("%s permission", "claude")); ok {
		t.Fatal("claude stayed open with codex opened")
	}
	if _, ok := p.Find(Tf("%s permission", "codex")); !ok {
		t.Fatal("codex's settings did not unfold")
	}
	if _, ok := p.Semantics().Find(ggui.RoleTextField, Tf("%s command", "codex")); ok {
		t.Fatal("built-in codex should not expose fixed command input")
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
			permNode, ok := p.Find(Tf("%s permission", target))
			if !ok {
				t.Fatalf("missing permission selector for %s:\n%s", target, p.Semantics().String())
			}
			// Verify Model, Effort, and Permission sit side-by-side in a single row cleanly inside the card without overflowing.
			mr, er, pr := modelNode.Rect, effortNode.Rect, permNode.Rect
			if mr.Size.W < 100 || er.Size.W < 100 || pr.Size.W < 100 {
				t.Fatalf("collapsed selector width: model=%+v effort=%+v permission=%+v", mr, er, pr)
			}
			if mr.Origin.X+mr.Size.W > er.Origin.X {
				t.Fatalf("model overlaps effort: model=%+v effort=%+v", mr, er)
			}
			if er.Origin.X+er.Size.W > pr.Origin.X {
				t.Fatalf("effort overlaps permission: effort=%+v permission=%+v", er, pr)
			}
			if pr.Origin.X+pr.Size.W > cardW {
				t.Fatalf("permission overflows card (%dpx): permission=%+v", cardW, pr)
			}
			if mr.Origin.Y != er.Origin.Y || er.Origin.Y != pr.Origin.Y {
				t.Fatalf("model, effort, and permission misaligned vertically: model=%+v effort=%+v permission=%+v", mr, er, pr)
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

func TestAgentEditorShowsUnifiedPermissionAndSeparatedCards(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(&config.Config{Agents: map[string]agentspec.Agent{
		"claude": {
			Model:      "claude-sonnet-4-6",
			Effort:     "medium",
			Permission: "acceptEdits",
			Profiles: []agentspec.Profile{
				{Name: "p1", Permission: agentspec.PermissionYOLO},
				{Name: "p2", Permission: "plan"},
			},
		},
	}}, "")
	m.AgentCatalogs.Set(map[string][]*protocol.AgentModel{
		"claude": {{Id: "claude-sonnet-4-6", Name: "Sonnet 4.6", Efforts: []string{"low", "medium", "high"}}},
	})
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
	const width = 620
	p := ggui.ProbeBuilder(func() ggui.Widget {
		return ggui.Provide(ggui.ReducedMotionKey, true, widget.Scroll(agentDetail(m, claudeDraft, profiles)))
	}, ggui.Sz(width, 1800))
	defer p.Close()
	p.Frame()

	// Built-in agent has no Command input.
	if _, ok := p.Find(Tf("%s command", "claude")); ok {
		t.Fatal("built-in agent should not show command input")
	}

	// Base agent has Model, Effort, and Permission aligned on one horizontal row.
	mNode, ok := p.Find(Tf("%s model", "claude"))
	if !ok {
		t.Fatalf("missing model selector for claude:\n%s", p.Semantics().String())
	}
	eNode, ok := p.Find(Tf("%s effort", "claude"))
	if !ok {
		t.Fatalf("missing effort selector for claude:\n%s", p.Semantics().String())
	}
	permNode, ok := p.Find(Tf("%s permission", "claude"))
	if !ok {
		t.Fatalf("missing permission selector for claude:\n%s", p.Semantics().String())
	}
	if mNode.Rect.Origin.Y != eNode.Rect.Origin.Y || eNode.Rect.Origin.Y != permNode.Rect.Origin.Y {
		t.Fatalf("agent model/effort/permission misaligned: model=%+v effort=%+v perm=%+v", mNode.Rect, eNode.Rect, permNode.Rect)
	}

	// Separated profile cards render independently below the base agent card.
	p1Perm, ok := p.Find(Tf("%s permission", "claude:p1"))
	if !ok {
		t.Fatalf("missing permission selector for claude:p1:\n%s", p.Semantics().String())
	}
	p2Perm, ok := p.Find(Tf("%s permission", "claude:p2"))
	if !ok {
		t.Fatalf("missing permission selector for claude:p2:\n%s", p.Semantics().String())
	}
	if !(permNode.Rect.Origin.Y < p1Perm.Rect.Origin.Y && p1Perm.Rect.Origin.Y < p2Perm.Rect.Origin.Y) {
		t.Fatalf("expected base card then p1 card then p2 card vertically: base=%v p1=%v p2=%v", permNode.Rect.Origin.Y, p1Perm.Rect.Origin.Y, p2Perm.Rect.Origin.Y)
	}
	if _, ok := p.Find(Tf("Add %s profile", "claude")); !ok {
		t.Fatalf("missing Add profile button:\n%s", p.Semantics().String())
	}
}

func TestAdvancedOptionsCollapsedByDefaultAndFlagsOverwriteToggle(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(&config.Config{Agents: map[string]agentspec.Agent{
		"claude": {
			Args: []string{"--verbose"},
			Profiles: []agentspec.Profile{
				{Name: "clean", Args: []string{"--quiet"}},
			},
		},
	}}, "")
	var profile agentDraft
	for _, r := range ggui.Peek(m.AgentDraft) {
		if r.Agent == "claude" && r.IsProfile {
			profile = r
		}
	}
	p := ggui.ProbeBuilder(func() ggui.Widget {
		return ggui.Provide(ggui.ReducedMotionKey, true, widget.Scroll(profileCard(m, profile, ggui.TextStyle{})))
	}, ggui.Sz(600, 1200))
	defer p.Close()
	p.Frame()

	target := "claude:clean"
	// Flags and Environment stay collapsed until Advanced is clicked.
	if _, ok := p.Find(Tf("%s flags", target)); ok {
		t.Fatal("flags input should be hidden while Advanced is collapsed")
	}
	if _, ok := p.Find(Tf("%s environment", target)); ok {
		t.Fatal("environment input should be hidden while Advanced is collapsed")
	}
	advBtn, ok := p.Find(Tf("%s advanced options", target))
	if !ok {
		t.Fatalf("missing advanced options toggle:\n%s", p.Semantics().String())
	}
	p.Click(advBtn.Center())
	p.Frame()

	if _, ok := p.Find(Tf("%s flags", target)); !ok {
		t.Fatalf("flags input missing after expanding Advanced:\n%s", p.Semantics().String())
	}
	if _, ok := p.Find(Tf("%s environment", target)); !ok {
		t.Fatalf("environment input missing after expanding Advanced:\n%s", p.Semantics().String())
	}
	overwriteBtn, ok := p.Find(T("Overwrite"))
	if !ok {
		t.Fatalf("missing Overwrite toggle option:\n%s", p.Semantics().String())
	}
	p.Click(overwriteBtn.Center())
	p.Frame()

	got, err := applyAgents(ggui.Peek(m.AgentDraft))
	if err != nil {
		t.Fatal(err)
	}
	if !got["claude"].Profiles[0].ReplaceArgs {
		t.Fatalf("expected ReplaceArgs=true after clicking Overwrite, got %+v", got["claude"].Profiles[0])
	}
}

func TestPermissionAndReplaceArgsRoundTrip(t *testing.T) {
	in := map[string]agentspec.Agent{
		"claude": {
			Model:      "claude-sonnet-4-6",
			Effort:     "medium",
			Permission: "acceptEdits",
			Args:       []string{"--verbose"},
			Profiles: []agentspec.Profile{
				{Name: "yolo", Permission: agentspec.PermissionYOLO, Args: []string{"--strict"}, ReplaceArgs: true},
				{Name: "plan", Permission: "plan"},
				{Name: "normal", Permission: agentspec.PermissionDefault},
				{Name: "inherit"},
			},
		},
		"codex": {
			Model:      "gpt-5.4",
			Effort:     "high",
			Permission: "readOnly",
			Profiles: []agentspec.Profile{
				{Name: "auto", Permission: "auto", ReplaceArgs: true},
			},
		},
	}
	draft1 := draftAgents(in)
	got, err := applyAgents(draft1)
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
	draft2 := draftAgents(got)
	if !sameAgents(draft1, draft2) {
		t.Fatalf("draft did not round-trip cleanly:\ndraft1=%+v\ndraft2=%+v", draft1, draft2)
	}

	cl := got["claude"]
	if cl.Model != "claude-sonnet-4-6" || cl.Effort != "medium" || cl.Permission != "acceptEdits" || cl.Yolo {
		t.Fatalf("claude = %+v", cl)
	}
	if p := cl.Profiles[0]; p.Permission != agentspec.PermissionYOLO || p.Yolo == nil || !*p.Yolo || !p.ReplaceArgs {
		t.Fatalf("claude:yolo = %+v", p)
	}
	if p := cl.Profiles[1]; p.Permission != "plan" || p.Yolo == nil || *p.Yolo || p.ReplaceArgs {
		t.Fatalf("claude:plan = %+v", p)
	}
	if p := cl.Profiles[2]; p.Permission != agentspec.PermissionDefault || p.Yolo == nil || *p.Yolo {
		t.Fatalf("claude:normal = %+v", p)
	}
	if p := cl.Profiles[3]; p.Permission != "" || p.Yolo != nil {
		t.Fatalf("claude:inherit = %+v", p)
	}
	cx := got["codex"]
	if cx.Model != "gpt-5.4" || cx.Effort != "high" || cx.Permission != "readOnly" || !cx.Profiles[0].ReplaceArgs || cx.Profiles[0].Permission != "auto" {
		t.Fatalf("codex = %+v", cx)
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

func TestClaudeCommitSignatureSettings(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	in := map[string]agentspec.Agent{
		"claude": {
			Claude: agentspec.ClaudeSettings{CommitSignature: true},
			Profiles: []agentspec.Profile{
				{Name: "default-off"},
				{Name: "sig-on", Claude: agentspec.ClaudeSettings{CommitSignature: true}},
			},
		},
	}
	got, err := applyAgents(draftAgents(in))
	if err != nil {
		t.Fatal(err)
	}
	if !got["claude"].Claude.CommitSignature {
		t.Fatalf("agent CommitSignature lost in round-trip: %+v", got["claude"])
	}
	ps := got["claude"].Profiles
	if len(ps) != 2 || ps[0].Claude.CommitSignature || !ps[1].Claude.CommitSignature {
		t.Fatalf("profile CommitSignature round-trip = %+v", ps)
	}

	// Default is off on both claude agent and profile; toggling turns them on.
	m := newModel(&config.Config{Agents: map[string]agentspec.Agent{
		"claude": {Profiles: []agentspec.Profile{{Name: "work"}}},
		"codex":  {Profiles: []agentspec.Profile{{Name: "fast"}}},
	}}, "")
	var claudeRow, claudeProfile, codexRow, codexProfile agentDraft
	for _, r := range ggui.Peek(m.AgentDraft) {
		switch {
		case r.Agent == "claude" && !r.IsProfile:
			claudeRow = r
		case r.Agent == "claude" && r.IsProfile:
			claudeProfile = r
		case r.Agent == "codex" && !r.IsProfile:
			codexRow = r
		case r.Agent == "codex" && r.IsProfile:
			codexProfile = r
		}
	}
	if claudeRow.ClaudeCommitSignature || claudeProfile.ClaudeCommitSignature {
		t.Fatalf("expected commit signature off by default: agent=%v profile=%v", claudeRow.ClaudeCommitSignature, claudeProfile.ClaudeCommitSignature)
	}

	p := ggui.ProbeBuilder(func() ggui.Widget { return widget.Scroll(agentEditor(m, claudeRow, []agentDraft{claudeProfile})) }, ggui.Sz(700, 1800))
	defer p.Close()
	p.Frame()

	for _, label := range []string{
		Tf("%s commit signature", "claude"),
		Tf("%s commit signature", "claude:work"),
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
	if !after["claude"].Claude.CommitSignature || !after["claude"].Profiles[0].Claude.CommitSignature {
		t.Fatalf("expected commit signature enabled after toggling, got %+v", after["claude"])
	}

	// Non-claude agents do not show the Claude commit signature switch.
	pCodex := ggui.ProbeBuilder(func() ggui.Widget { return widget.Scroll(agentEditor(m, codexRow, []agentDraft{codexProfile})) }, ggui.Sz(700, 1800))
	defer pCodex.Close()
	pCodex.Frame()
	if _, ok := pCodex.Find(Tf("%s commit signature", "codex")); ok {
		t.Fatal("codex agent should not show Claude commit signature switch")
	}
	if _, ok := pCodex.Find(Tf("%s commit signature", "codex:fast")); ok {
		t.Fatal("codex profile should not show Claude commit signature switch")
	}
}

func TestFastModeAgentAndProfileSettings(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	on, off := true, false
	in := map[string]agentspec.Agent{
		"claude": {
			Fast: &on,
			Profiles: []agentspec.Profile{
				{Name: "inherit"},
				{Name: "off", Fast: &off},
				{Name: "on", Fast: &on},
			},
		},
		"codex": {
			Fast: &off,
			Profiles: []agentspec.Profile{
				{Name: "fast", Fast: &on},
			},
		},
	}
	got, err := applyAgents(draftAgents(in))
	if err != nil {
		t.Fatal(err)
	}
	if got["claude"].Fast == nil || !*got["claude"].Fast {
		t.Fatalf("claude agent Fast = %v, want true", got["claude"].Fast)
	}
	cp := got["claude"].Profiles
	if len(cp) != 3 || cp[0].Fast != nil || cp[1].Fast == nil || *cp[1].Fast || cp[2].Fast == nil || !*cp[2].Fast {
		t.Fatalf("claude profiles Fast round-trip = %+v", cp)
	}
	if got["codex"].Fast == nil || *got["codex"].Fast {
		t.Fatalf("codex agent Fast = %v, want false", got["codex"].Fast)
	}
	if xp := got["codex"].Profiles; len(xp) != 1 || xp[0].Fast == nil || !*xp[0].Fast {
		t.Fatalf("codex profiles Fast round-trip = %+v", xp)
	}

	// Both claude and codex render fast mode selectors on agent and profile cards; agy does not.
	m := newModel(&config.Config{Agents: map[string]agentspec.Agent{
		"claude": {Profiles: []agentspec.Profile{{Name: "work"}}},
		"codex":  {Profiles: []agentspec.Profile{{Name: "turbo"}}},
		"agy":    {Profiles: []agentspec.Profile{{Name: "p"}}},
	}}, "")
	findRow := func(agentName string, isProfile bool) agentDraft {
		for _, r := range ggui.Peek(m.AgentDraft) {
			if r.Agent == agentName && r.IsProfile == isProfile {
				return r
			}
		}
		t.Fatalf("missing row agent=%s profile=%v", agentName, isProfile)
		return agentDraft{}
	}
	for _, tc := range []struct {
		agent, profile string
		want           bool
	}{
		{"claude", "work", true},
		{"codex", "turbo", true},
		{"agy", "p", false},
	} {
		row := findRow(tc.agent, false)
		prof := findRow(tc.agent, true)
		p := ggui.ProbeBuilder(func() ggui.Widget { return widget.Scroll(agentEditor(m, row, []agentDraft{prof})) }, ggui.Sz(700, 1800))
		p.Frame()
		_, hasAgent := p.Find(Tf("%s fast mode", tc.agent))
		_, hasProf := p.Find(Tf("%s fast mode", tc.agent+":"+tc.profile))
		p.Close()
		if hasAgent != tc.want || hasProf != tc.want {
			t.Errorf("%s: fast mode selector agent=%v profile=%v, want %v", tc.agent, hasAgent, hasProf, tc.want)
		}
	}
}

// Saving Settings > Agents keeps the sandbox of agents and profiles, next
// to their fast mode, and the cards choose it.
func TestAgentSandboxSettings(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	on, off := true, false
	in := map[string]agentspec.Agent{
		"claude": {Sandbox: agentspec.SandboxWorkspace, Fast: &on, Profiles: []agentspec.Profile{
			{Name: "inherits"},
			{Name: "open", Sandbox: agentspec.SandboxOff, Fast: &off},
		}},
		"codex": {Sandbox: agentspec.SandboxOff, Profiles: []agentspec.Profile{{Name: "boxed", Sandbox: agentspec.SandboxWorkspace}}},
	}
	got, err := applyAgents(draftAgents(in))
	if err != nil {
		t.Fatal(err)
	}
	claude, codex := got["claude"], got["codex"]
	if claude.Sandbox != agentspec.SandboxWorkspace || claude.Profiles[0].Sandbox != "" || claude.Profiles[1].Sandbox != agentspec.SandboxOff {
		t.Fatalf("claude sandbox round-trip = %+v", claude)
	}
	if codex.Sandbox != "" || codex.Profiles[0].Sandbox != agentspec.SandboxWorkspace {
		t.Fatalf("codex sandbox round-trip = %+v", codex)
	}
	if claude.Fast == nil || !*claude.Fast || claude.Profiles[0].Fast != nil || claude.Profiles[1].Fast == nil || *claude.Profiles[1].Fast {
		t.Fatalf("claude fast mode round-trip beside the sandbox = %+v", claude)
	}

	m := newModel(&config.Config{Agents: map[string]agentspec.Agent{
		"claude": {Fast: &on, Profiles: []agentspec.Profile{{Name: "work"}}},
	}}, "")
	var row, profile agentDraft
	for _, r := range ggui.Peek(m.AgentDraft) {
		if r.Agent == "claude" {
			if r.IsProfile {
				profile = r
			} else {
				row = r
			}
		}
	}
	p := ggui.ProbeBuilder(func() ggui.Widget { return widget.Scroll(agentEditor(m, row, []agentDraft{profile})) }, ggui.Sz(700, 2000))
	defer p.Close()
	p.Frame()
	for label, choice := range map[string]string{
		Tf("%s sandbox", "claude"):      T("Workspace"),
		Tf("%s sandbox", "claude:work"): T("Off"),
	} {
		sel, ok := p.Semantics().Find(ggui.RoleSelect, label)
		if !ok {
			t.Fatalf("missing select %q:\n%s", label, p.Semantics().String())
		}
		p.PerformAction(sel.ID, ggui.Action{Kind: ggui.ActionSelect, Text: choice})
	}
	after, err := applyAgents(ggui.Peek(m.AgentDraft))
	if err != nil {
		t.Fatal(err)
	}
	if after["claude"].Sandbox != agentspec.SandboxWorkspace || after["claude"].Profiles[0].Sandbox != agentspec.SandboxOff {
		t.Fatalf("after choosing, claude = %+v", after["claude"])
	}
	if f := after["claude"].Fast; f == nil || !*f {
		t.Fatalf("choosing the sandbox lost fast mode: %+v", after["claude"])
	}
}
