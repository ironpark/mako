package gui

import (
	"slices"
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
)

func TestPrefsNormalizeOutOfRangeValues(t *testing.T) {
	p := prefs{Theme: "", FontSize: 80, LineHeight: 0.2, CursorStyle: "beam", Scrollback: -5}.normalized()
	if p.Theme != "system" || p.FontSize != maxFontSize || p.LineHeight != minLineHeight ||
		p.CursorStyle != "block" || p.Scrollback != 0 || p.DefaultAgent == "" {
		t.Fatalf("normalized = %+v", p)
	}
}

func TestHostDraftRoundTrips(t *testing.T) {
	cfg := config.Config{
		NodeID: "n1", Name: "mbp", P2P: config.P2P{Listen: ":4243"}, SSHKey: "~/.ssh/id_ed25519.pub",
		Peers:     []string{"10.0.0.2", "10.0.0.3:5000"},
		Discovery: config.Discovery{MDNS: true, TailnetHosts: []string{"box"}},
	}
	d := draftFrom(&cfg)
	d.Peers += "\n  \n10.0.0.4"
	got, err := d.apply(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Peers) != 3 || got.Peers[2] != "10.0.0.4" {
		t.Fatalf("peers = %q", got.Peers)
	}
	if got.NodeID != "n1" {
		t.Fatal("apply dropped a field the draft does not edit")
	}
}

func TestAgentDraftRoundTrips(t *testing.T) {
	agents := map[string]agentspec.Agent{
		"claude": {Command: []string{"claude"}, Args: []string{"--verbose"}, Env: map[string]string{"B": "2", "A": "1"},
			Profiles: []agentspec.Profile{
				{Name: "opus", Description: "복잡한 Go 동시성 검토\n취소 처리와 경쟁 상태를 확인합니다.", Args: []string{"--model", "opus"}},
				{Name: "work", Env: map[string]string{"ANTHROPIC_API_KEY": "sk-x"}},
			}},
		"lazy": {Command: []string{"lazygit", "--path", "my dir"}},
	}
	rows := draftAgents(agents)
	var claude, opus agentDraft
	for _, r := range rows {
		switch {
		case r.Agent == "claude" && !r.IsProfile:
			claude = r
		case r.Profile == "opus":
			opus = r
		}
	}
	if claude.Args != "--verbose" || claude.Env != "A=1\nB=2" || opus.Args != "--model opus" {
		t.Fatalf("claude %+v, opus %+v", claude, opus)
	}
	if opus.Description != agents["claude"].Profiles[0].Description {
		t.Fatalf("profile description = %q", opus.Description)
	}
	got, err := applyAgents(rows)
	if err != nil {
		t.Fatal(err)
	}
	if !sameAgents(draftAgents(got), rows) {
		t.Fatalf("round trip changed the agents: %+v", got)
	}
	if c := got["lazy"].Command; len(c) != 3 || c[2] != "my dir" {
		t.Fatalf("lazy command = %q", c)
	}
	if p, _ := got["claude"].Profile("work"); p.Env["ANTHROPIC_API_KEY"] != "sk-x" {
		t.Fatalf("work profile = %+v", p)
	}
	// Built-ins the configuration left out still appear, to be given flags.
	if !slices.ContainsFunc(rows, func(r agentDraft) bool { return r.Agent == "codex" && r.Command == "codex" }) {
		t.Fatal("the built-in codex is not in the draft")
	}

	for _, bad := range []func([]agentDraft){
		func(r []agentDraft) {
			r[slices.IndexFunc(r, func(d agentDraft) bool { return d.Profile == "work" })].Profile = "opus"
		},
		func(r []agentDraft) {
			r[slices.IndexFunc(r, func(d agentDraft) bool { return d.Profile == "work" })].Profile = "my work"
		},
		func(r []agentDraft) { r[0].Env = "not an assignment" },
		func(r []agentDraft) { r[0].Args = "--unterminated 'quote" },
	} {
		r := slices.Clone(rows)
		bad(r)
		if _, err := applyAgents(r); err == nil {
			t.Errorf("a broken draft was accepted: %+v", r)
		}
	}
}

func TestAddingAndRemovingProfiles(t *testing.T) {
	m := newModel(&config.Config{NodeID: "n1", Agents: agentspec.Defaults()}, "")
	m.addProfile("claude")
	m.addProfile("claude")
	var names []string
	for _, r := range ggui.Peek(m.AgentDraft) {
		if r.Agent == "claude" && r.IsProfile {
			names = append(names, r.Profile)
		}
	}
	if !slices.Equal(names, []string{"profile", "profile2"}) {
		t.Fatalf("claude's profiles = %q", names)
	}
	if err := m.addAgent("lazy git"); err == nil {
		t.Fatal("an agent name with a space was accepted")
	}
	if err := m.addAgent("lazygit"); err != nil {
		t.Fatal(err)
	}
	rows := ggui.Peek(m.AgentDraft)
	m.removeAgentRow(rows[slices.IndexFunc(rows, func(r agentDraft) bool { return r.Agent == "claude" && !r.IsProfile })].ID)
	if slices.ContainsFunc(ggui.Peek(m.AgentDraft), func(r agentDraft) bool { return r.Agent == "claude" }) {
		t.Fatal("removing an agent left its profiles")
	}
}

func TestSettingsDeveloperPinnedAtBottom(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	last := settingsNav[len(settingsNav)-1]
	if !last.bottom || len(last.sections) != 1 || last.sections[0].id != "developer" {
		t.Fatalf("expected Developer as the bottom-pinned last nav group, got %+v", last)
	}
	m := newModel(&config.Config{NodeID: "n1", Agents: agentspec.Defaults()}, "")
	m.Page.Set("settings")
	m.Section.Set("general")

	p := ggui.ProbeBuilder(func() ggui.Widget {
		return ggui.Provide(ggui.ReducedMotionKey, true, settingsPage(m))
	}, ggui.Sz(1600, 900))
	defer p.Close()
	for range 3 {
		p.Frame()
	}

	dev, ok := p.Semantics().Find(ggui.RoleButton, T("Developer"))
	if !ok {
		t.Fatalf("Developer button missing from settings sidebar:\n%s", p.Semantics().String())
	}
	about, ok := p.Semantics().Find(ggui.RoleButton, T("About"))
	if !ok {
		t.Fatalf("About button missing from settings sidebar:\n%s", p.Semantics().String())
	}
	if dev.Rect.Origin.Y <= about.Rect.Origin.Y {
		t.Fatalf("Developer (y=%.1f) should sit below About (y=%.1f)", dev.Rect.Origin.Y, about.Rect.Origin.Y)
	}
	if dev.Rect.Origin.Y < 800 {
		t.Fatalf("Developer (y=%.1f) should be pinned near the bottom of a 900px sidebar", dev.Rect.Origin.Y)
	}

	p.Tap(T("Developer"))
	for range 3 {
		p.Frame()
	}
	if got := ggui.Peek(m.Section); got != "developer" {
		t.Fatalf("tapping Developer selected %q, want developer", got)
	}
}

func TestSettingsWideScreenLayout(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(&config.Config{NodeID: "n1", Agents: agentspec.Defaults()}, "")
	m.Page.Set("settings")
	m.Section.Set("general")

	p := ggui.ProbeBuilder(func() ggui.Widget {
		return ggui.Provide(ggui.ReducedMotionKey, true, settingsPage(m))
	}, ggui.Sz(1680, 960))
	defer p.Close()
	step := func() {
		for range 3 {
			p.Frame()
		}
	}
	step()

	// On a wide screen, General places Sessions on the left and Language /
	// Idle agents / Connection on the right, spanning wider than the old 1120px cap.
	defAgent, ok := p.Semantics().Find(ggui.RoleSelect, T("Default agent"))
	if !ok {
		t.Fatalf("Default agent control missing:\n%s", p.Semantics().String())
	}
	lang, ok := p.Semantics().Find(ggui.RoleSelect, T("Language"))
	if !ok {
		t.Fatalf("Language control missing:\n%s", p.Semantics().String())
	}
	if lang.Rect.Origin.X <= defAgent.Rect.Origin.X+defAgent.Rect.Size.W {
		t.Fatalf("wide General should place Language (x=%.1f) to the right of Default agent (right=%.1f)",
			lang.Rect.Origin.X, defAgent.Rect.Origin.X+defAgent.Rect.Size.W)
	}
	// Content area runs well past sidebar (272) + old 1120 cap on a 1680px window.
	if right := lang.Rect.Origin.X + lang.Rect.Size.W; right <= sidebarWidth+1120 {
		t.Fatalf("wide settings content right edge %.1f did not expand beyond old 1120 cap", right)
	}

	// On a narrow screen, General collapses back to a single vertical column.
	p.Resize(ggui.Sz(900, 960))
	step()
	defAgentNarrow, ok := p.Semantics().Find(ggui.RoleSelect, T("Default agent"))
	if !ok {
		t.Fatalf("Default agent control missing when narrow:\n%s", p.Semantics().String())
	}
	langNarrow, ok := p.Semantics().Find(ggui.RoleSelect, T("Language"))
	if !ok {
		t.Fatalf("Language control missing when narrow:\n%s", p.Semantics().String())
	}
	if langNarrow.Rect.Origin.Y >= defAgentNarrow.Rect.Origin.Y {
		t.Fatalf("narrow General should stack Language (y=%.1f) above Default agent (y=%.1f)",
			langNarrow.Rect.Origin.Y, defAgentNarrow.Rect.Origin.Y)
	}

	// Switch back to wide and verify Appearance (with merged Terminal),
	// Daemon (with merged Host + Discovery), and Shell use two columns.
	p.Resize(ggui.Sz(1680, 960))

	m.Section.Set("appearance")
	step()
	paneGlow, ok := p.Semantics().Find(ggui.RoleSwitch, T("Glow round the current pane"))
	if !ok {
		t.Fatalf("Glow round the current pane missing:\n%s", p.Semantics().String())
	}
	usageOverlay, ok := p.Semantics().Find(ggui.RoleSwitch, T("Show usage overlay"))
	if !ok {
		t.Fatalf("Show usage overlay missing:\n%s", p.Semantics().String())
	}
	if usageOverlay.Rect.Origin.X <= paneGlow.Rect.Origin.X+paneGlow.Rect.Size.W {
		t.Fatalf("wide Appearance should place Agent usage (x=%.1f) beside Panes (right=%.1f)",
			usageOverlay.Rect.Origin.X, paneGlow.Rect.Origin.X+paneGlow.Rect.Size.W)
	}
	cursorStyle, ok := p.Semantics().Find(ggui.RoleSelect, T("Cursor style"))
	if !ok {
		t.Fatalf("Cursor style missing in merged Appearance:\n%s", p.Semantics().String())
	}
	lineHeight, ok := p.Semantics().Find(ggui.RoleSlider, T("Line height"))
	if !ok {
		t.Fatalf("Line height missing in merged Appearance:\n%s", p.Semantics().String())
	}
	if cursorStyle.Rect.Origin.X <= lineHeight.Rect.Origin.X+lineHeight.Rect.Size.W {
		t.Fatalf("wide Appearance should place Cursor style (x=%.1f) beside Line height (right=%.1f)",
			cursorStyle.Rect.Origin.X, lineHeight.Rect.Origin.X+lineHeight.Rect.Size.W)
	}

	m.Section.Set("daemon")
	step()
	nodeName, ok := p.Semantics().Find(ggui.RoleTextField, T("Node name"))
	if !ok {
		t.Fatalf("Node name missing in merged Daemon:\n%s", p.Semantics().String())
	}
	p2pSwitch, ok := p.Semantics().Find(ggui.RoleSwitch, T("Link over libp2p"))
	if !ok {
		t.Fatalf("Link over libp2p missing in merged Daemon:\n%s", p.Semantics().String())
	}
	if p2pSwitch.Rect.Origin.X <= nodeName.Rect.Origin.X+nodeName.Rect.Size.W {
		t.Fatalf("wide Daemon should place libp2p (x=%.1f) beside Identity (right=%.1f)",
			p2pSwitch.Rect.Origin.X, nodeName.Rect.Origin.X+nodeName.Rect.Size.W)
	}
	bonjour, ok := p.Semantics().Find(ggui.RoleSwitch, T("Bonjour"))
	if !ok {
		t.Fatalf("Bonjour missing in merged Daemon:\n%s", p.Semantics().String())
	}
	peers, ok := p.Semantics().Find(ggui.RoleTextField, T("Peers"))
	if !ok {
		t.Fatalf("Peers missing in merged Daemon:\n%s", p.Semantics().String())
	}
	if peers.Rect.Origin.X <= bonjour.Rect.Origin.X+bonjour.Rect.Size.W {
		t.Fatalf("wide Daemon should place Peers (x=%.1f) beside Bonjour (right=%.1f)",
			peers.Rect.Origin.X, bonjour.Rect.Origin.X+bonjour.Rect.Size.W)
	}

	// Legacy merged section IDs still resolve cleanly to their new homes.
	for legacy, want := range map[string]string{
		"terminal":  "appearance",
		"host":      "daemon",
		"discovery": "daemon",
	} {
		if got := canonicalSettingsSection(legacy); got != want {
			t.Fatalf("canonicalSettingsSection(%q) = %q, want %q", legacy, got, want)
		}
	}

	// Verify Agents, API keys, Teams, Templates, Plugins (when present), and Extensions
	// live together under "Agents & extensions".
	var agentIDs []string
	for _, g := range settingsGroups(true) {
		if g.title == "Agents & extensions" {
			for _, s := range g.sections {
				agentIDs = append(agentIDs, s.id)
			}
		}
	}
	wantAgentIDs := []string{"agents", "apikeys", "teams", "templates", "skills", "plugins", "extensions", "policies"}
	if !slices.Equal(agentIDs, wantAgentIDs) {
		t.Fatalf("Agents & extensions sections = %v, want %v", agentIDs, wantAgentIDs)
	}

	m.Section.Set("shell")
	step()
	shellProg, ok := p.Semantics().Find(ggui.RoleTextField, T("Shell program"))
	if !ok {
		t.Fatalf("Shell program missing:\n%s", p.Semantics().String())
	}
	shellEnv, ok := p.Semantics().Find(ggui.RoleTextField, T("Shell environment"))
	if !ok {
		t.Fatalf("Shell environment missing:\n%s", p.Semantics().String())
	}
	if shellEnv.Rect.Origin.X <= shellProg.Rect.Origin.X+shellProg.Rect.Size.W {
		t.Fatalf("wide Shell should place Environment (x=%.1f) beside Program (right=%.1f)",
			shellEnv.Rect.Origin.X, shellProg.Rect.Origin.X+shellProg.Rect.Size.W)
	}
}

func TestSettingsListBesideScalesWidth(t *testing.T) {
	var listW, detailW float64
	lb := &listBesideLayout{
		list: ggui.FromFuncs(func(c ggui.Constraints, _ ggui.Env) ggui.Size {
			listW = c.MaxW
			return ggui.Sz(c.MaxW, 100)
		}, func(*ggui.Canvas, ggui.Rect) {}),
		detail: ggui.FromFuncs(func(c ggui.Constraints, _ ggui.Env) ggui.Size {
			detailW = c.MaxW
			return ggui.Sz(c.MaxW, 200)
		}, func(*ggui.Canvas, ggui.Rect) {}),
		narrow: ggui.FromFuncs(func(c ggui.Constraints, _ ggui.Env) ggui.Size {
			return ggui.Sz(c.MaxW, 300)
		}, func(*ggui.Canvas, ggui.Rect) {}),
	}
	env := ggui.Env{}
	// Below settingsWide: narrow branch.
	sz := lb.Layout(ggui.Constraints{MinW: 700, MaxW: 700, MaxH: ggui.Unbounded}, env)
	if lb.wide || sz.H != 300 {
		t.Fatalf("at 700px expected narrow (h=300), got wide=%v sz=%+v", lb.wide, sz)
	}
	// At 1500px: list scales proportionally up to listBesideMaxWidth (440), detail gets the rest.
	sz = lb.Layout(ggui.Constraints{MinW: 1500, MaxW: 1500, MaxH: ggui.Unbounded}, env)
	if !lb.wide || listW != listBesideMaxWidth || detailW != 1500-listBesideMaxWidth-settingsColumnGap || sz.H != 200 {
		t.Fatalf("at 1500px got wide=%v listW=%.1f detailW=%.1f sz=%+v", lb.wide, listW, detailW, sz)
	}
}
