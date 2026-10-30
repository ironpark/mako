package gui

import (
	"encoding/json"
	"runtime"
	"slices"
	"testing"

	"github.com/ironpark/ggui"

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
