package gui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/prompttmpl"
	"github.com/ironpark/mako/internal/protocol"
)

// Two slashes list the templates for the session's agent; Tab completes
// one; Enter sends the prompt it makes, and over a wrong call keeps the
// draft and says what is wrong.
func TestTheComposerSendsATemplatesPrompt(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	dir, err := config.TemplatesDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"review.md": "---\ndescription: Review a file\nargs:\n  - {name: file, required: true}\n  - {name: depth, type: integer, default: 1}\n---\nReview {{file}} {{depth}} deep as {{agent}} in {{project}}.\n",
		"codex.md":  "---\nagents: [codex]\n---\nOnly for Codex\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	m := newModel(config.Default(), "")
	link := newFakeLink()
	conn, err := newTermConn("s", link, 80, 24, func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { close(link.in); conn.close() }()
	m.conns["s"] = conn
	m.Attached.Set([]string{"s"})
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "s", Agent: "claude", Project: "/tmp/p"}})
	state := m.chatState("s")
	p := ggui.ProbeBuilder(func() ggui.Widget { return composer(m, "s", state) }, ggui.Sz(600, 400))
	defer p.Close()
	p.Frame()
	field, ok := p.Semantics().Find(ggui.RoleTextField, T("Message"))
	if !ok {
		t.Fatal("no message field")
	}
	p.Click(field.Rect.Origin.Add(ggui.Pt(10, field.Rect.Size.H/2)))

	p.Text("//")
	p.Frame()
	got := ggui.Untrack(func() []string {
		var names []string
		for _, it := range m.menuItems("s", state) {
			names = append(names, it.name)
		}
		return names
	})
	if strings.Join(got, ",") != "//review,//"+prompttmpl.MakerName {
		t.Fatalf("listed %v, want only the template for Claude, and the built-in", got)
	}
	p.Type(ggui.Mods{}, ggui.KeyTab)
	p.Frame()
	if d := ggui.Peek(state.draft); d != "//review " {
		t.Fatalf("Tab completed to %q", d)
	}

	p.Text("a.go depth=x")
	p.Type(ggui.Mods{}, ggui.KeyEnter)
	p.Frame()
	if d := ggui.Peek(state.draft); d != "//review a.go depth=x" {
		t.Fatalf("a wrong call left the draft %q", d)
	}
	if f := ggui.Peek(state.templates.failed); !strings.Contains(f.why, "depth") {
		t.Fatalf("a wrong call says %q", f.why)
	}
	if typed := link.typed(); typed != "" {
		t.Fatalf("a wrong call sent %q", typed)
	}

	m.setComposerDraft("s", `//review "my file.go" 3`)
	p.Frame()
	p.Type(ggui.Mods{}, ggui.KeyEnter)
	if got, want := sentThrough(p, link), "[Mako template //review]\rReview my file.go 3 deep as claude in /tmp/p.\r"; got != want {
		t.Fatalf("sent %q, want %q", got, want)
	}

	// A draft naming no template is sent as it is written.
	m.setComposerDraft("s", "//nothing here")
	p.Frame()
	p.Type(ggui.Mods{}, ggui.KeyEnter)
	if got, want := sentThrough(p, link), "//nothing here\r"; got != want {
		t.Fatalf("sent %q, want %q", got, want)
	}
}

func TestComposerSelectArgumentAutocompleteAndSubmit(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	dir, err := config.TemplatesDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := `---
description: Deploy app
args:
  - name: env
    type: select
    required: true
    description: Target environment
    options: [dev, staging, prod]
  - name: dry_run
    type: boolean
    default: "false"
---
Deploy to {{env}} (dry={{dry_run}}).
`
	if err := os.WriteFile(filepath.Join(dir, "deploy.md"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	m := newModel(config.Default(), "")
	link := newFakeLink()
	conn, err := newTermConn("s", link, 80, 24, func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { close(link.in); conn.close() }()
	m.conns["s"] = conn
	m.Attached.Set([]string{"s"})
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "s", Agent: "claude", Project: "/tmp/p"}})
	state := m.chatState("s")
	p := ggui.ProbeBuilder(func() ggui.Widget { return composer(m, "s", state) }, ggui.Sz(700, 480))
	defer p.Close()
	p.Frame()

	field, ok := p.Semantics().Find(ggui.RoleTextField, T("Message"))
	if !ok {
		t.Fatal("no message field")
	}
	p.Click(field.Rect.Origin.Add(ggui.Pt(10, field.Rect.Size.H/2)))

	p.Text("//dep")
	p.Frame()
	p.Type(ggui.Mods{}, ggui.KeyTab)
	p.Frame()
	if d := ggui.Peek(state.draft); d != "//deploy " {
		t.Fatalf("template Tab completed to %q", d)
	}

	// Typing partial prefix narrows select options, and Tab completes to staging.
	p.Text("st")
	p.Frame()
	items := ggui.Untrack(func() []menuItem { return m.menuItems("s", state) })
	if len(items) == 0 || items[0].name != "staging" {
		t.Fatalf("expected staging option first, got %+v", items)
	}
	p.Type(ggui.Mods{}, ggui.KeyTab)
	p.Frame()
	if d := ggui.Peek(state.draft); d != "//deploy staging " {
		t.Fatalf("select Tab completed to %q", d)
	}

	// Now all required args are satisfied: typing Enter without navigating arrows submits immediately.
	p.Type(ggui.Mods{}, ggui.KeyEnter)
	if got, want := sentThrough(p, link), "[Mako template //deploy]\rDeploy to staging (dry=false).\r"; got != want {
		t.Fatalf("sent %q, want %q", got, want)
	}

	// Named arg completion: typing dry_ completes dry_run=, then Tab picks true.
	m.setComposerDraft("s", "//deploy prod dry_")
	p.Frame()
	p.Type(ggui.Mods{}, ggui.KeyTab)
	p.Frame()
	if d := ggui.Peek(state.draft); d != "//deploy prod dry_run=" {
		t.Fatalf("named arg completion = %q", d)
	}
	p.Type(ggui.Mods{}, ggui.KeyTab)
	p.Frame()
	if d := ggui.Peek(state.draft); d != "//deploy prod dry_run=true " {
		t.Fatalf("boolean completion after dry_run= = %q", d)
	}
}

func TestTemplateHostAgentsCatalogSnapshot(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "s", NodeId: "n1", Agent: "claude", Project: "/tmp/p"}})
	m.Nodes.Set([]*protocol.Node{{
		Id: "n1",
		Agents: []*protocol.AgentInfo{{
			Name: "claude",
			Yolo: true,
			ProfileDetails: []*protocol.AgentProfileInfo{{
				Name:        "reviewer",
				Description: "Reviews PRs",
				Model:       "claude-opus-4-6",
				Effort:      "high",
				Yolo:        true,
				Spawnable:   true,
			}},
		}},
	}})
	m.AgentCatalogs.Set(map[string][]*protocol.AgentModel{
		"claude": {{Id: "claude-opus-4-6", Name: "Opus", Efforts: []string{"medium", "high"}}},
	})

	host := m.templateHost("s")
	cat, err := host.Agents(t.Context(), "", false)
	if err != nil {
		t.Fatal(err)
	}
	if cat.NodeID != "n1" || len(cat.Agents) != 1 || !cat.Agents[0].Yolo || len(cat.Agents[0].Profiles) != 1 || !cat.Agents[0].Profiles[0].Spawnable || len(cat.Agents[0].Models) != 1 {
		t.Fatalf("unexpected catalog = %+v", cat)
	}
}

func TestTemplateHostRejectsDisallowedProfile(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	off := false
	on := true
	m := newModel(config.Default(), "")
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "s", NodeId: "n1", Agent: "claude", Project: "/tmp/p"}})
	m.Nodes.Set([]*protocol.Node{{
		Id: "n1",
		Agents: []*protocol.AgentInfo{{
			Name:          "claude",
			AllowAgent:    &off,
			AllowTemplate: &off,
			ProfileDetails: []*protocol.AgentProfileInfo{{
				Name:          "manual-only",
				AllowAgent:    &off,
				AllowTemplate: &off,
				Spawnable:     false,
			}, {
				Name:          "auto",
				AllowAgent:    &on,
				AllowTemplate: &on,
				Spawnable:     true,
			}},
		}},
	}})

	host := m.templateHost("s")
	cat, err := host.Agents(t.Context(), "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Agents) != 1 || len(cat.Agents[0].Profiles) != 2 || cat.Agents[0].AllowAgent || cat.Agents[0].AllowTemplate {
		t.Fatalf("unexpected catalog: %+v", cat)
	}
	if cat.Agents[0].Profiles[0].AllowAgent || cat.Agents[0].Profiles[0].AllowTemplate {
		t.Fatalf("manual-only flags should be false: %+v", cat.Agents[0].Profiles[0])
	}
	if _, err := host.Spawn(t.Context(), prompttmpl.SpawnRequest{Agent: "claude", Prompt: "run"}); err == nil || !strings.Contains(err.Error(), "not enabled for prompt templates") {
		t.Fatalf("Spawn allowed disallowed bare agent default: %v", err)
	}
	if _, err := host.Ask(t.Context(), prompttmpl.AskRequest{Agent: "claude", Prompt: "hi"}); err == nil || !strings.Contains(err.Error(), "not enabled for prompt templates") {
		t.Fatalf("Ask allowed disallowed bare agent default: %v", err)
	}
	if _, err := host.Spawn(t.Context(), prompttmpl.SpawnRequest{Agent: "claude", Profile: "manual-only", Prompt: "run"}); err == nil || !strings.Contains(err.Error(), "not enabled for prompt templates") {
		t.Fatalf("Spawn allowed disallowed profile: %v", err)
	}
	if _, err := host.Ask(t.Context(), prompttmpl.AskRequest{Agent: "claude", Profile: "manual-only", Prompt: "hi"}); err == nil || !strings.Contains(err.Error(), "not enabled for prompt templates") {
		t.Fatalf("Ask allowed disallowed profile: %v", err)
	}
}
