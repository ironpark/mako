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
