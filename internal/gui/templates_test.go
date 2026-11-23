package gui

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/daemon"
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
	if strings.Join(got, ",") != "//"+prompttmpl.PluginMakerName+",//review,//"+prompttmpl.MakerName {
		t.Fatalf("listed %v, want only the template for Claude, and the built-ins", got)
	}
	p.Text("rev")
	p.Frame()
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

func TestReadTemplatesUsesMetadataAndStampCache(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	dir, err := config.TemplatesDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Broken JS in body must not prevent metadata listing (no Wasm startup on refresh),
	// and only surfaces when Enter runs the template.
	brokenJS := "---\ndescription: Broken JS\n---\n```js\nconst = ;\n```\n"
	if err := os.WriteFile(filepath.Join(dir, "brokenjs.md"), []byte(brokenJS), 0o644); err != nil {
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

	var names []string
	ggui.Untrack(func() int {
		for _, it := range m.menuItems("s", state) {
			names = append(names, it.name)
		}
		return 0
	})
	if !strings.Contains(strings.Join(names, ","), "//brokenjs") {
		t.Fatalf("brokenjs not listed in metadata autocomplete: %v", names)
	}
	loadsBefore := state.templates.loads
	if loadsBefore == 0 {
		t.Fatal("expected initial LoadAllMetadata load")
	}

	// Expire stale timer without changing dir: stamp check must skip LoadAllMetadata.
	state.templates.read = time.Now().Add(-templatesStale - time.Second)
	m.readTemplates(state, false)
	if state.templates.loads != loadsBefore {
		t.Fatalf("unchanged dir reloaded metadata: %d -> %d", loadsBefore, state.templates.loads)
	}

	// Adding a new file invalidates stamp and reloads.
	if err := os.WriteFile(filepath.Join(dir, "added.md"), []byte("Hello {{project}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	state.templates.read = time.Now().Add(-templatesStale - time.Second)
	m.readTemplates(state, false)
	if state.templates.loads != loadsBefore+1 {
		t.Fatalf("changed dir did not reload metadata: %d -> %d", loadsBefore, state.templates.loads)
	}

	// Submitting //brokenjs still evaluates JS and surfaces the syntax error.
	m.setComposerDraft("s", "//brokenjs")
	p.Frame()
	p.Type(ggui.Mods{}, ggui.KeyEnter)
	p.Frame()
	if f := ggui.Peek(state.templates.failed); f.why == "" {
		t.Fatal("expected JS syntax error on Enter for //brokenjs")
	}
}

func TestReadTemplatesAsyncPostAndInvalidation(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	dir, err := config.TemplatesDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "alpha.md"), []byte("Alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := newModel(config.Default(), "")
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "s", Agent: "claude", Project: "/tmp/p"}})
	state := m.chatState("s")
	p := ggui.ProbeBuilder(func() ggui.Widget { return composer(m, "s", state) }, ggui.Sz(600, 400))
	defer p.Close()
	m.post = p.Post
	p.Frame()

	hasTemplate := func(name string) bool {
		return ggui.Untrack(func() bool {
			for _, tm := range state.templates.templates.Get() {
				if tm.Name == name {
					return true
				}
			}
			return false
		})
	}

	m.setComposerDraft("s", "//")
	probeUntil(t, p, "async alpha template load", func() bool { return hasTemplate("alpha") })

	if err := os.WriteFile(filepath.Join(dir, "beta.md"), []byte("Beta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.invalidateSessionTemplates()
	m.readTemplates(state, false)
	probeUntil(t, p, "async beta template reload", func() bool { return hasTemplate("beta") })
}

func TestTemplateHintRespectsAgentAndProfile(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	dir, err := config.TemplatesDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"claudeonly.md":   "---\nagents: [claude]\ndescription: Claude only\nargs:\n  - {name: target, required: true}\n---\n{{target}}\n",
		"codexonly.md":    "---\nagents: [codex]\ndescription: Codex only\nargs:\n  - {name: target, required: true}\n---\n{{target}}\n",
		"revieweronly.md": "---\nagents: [claude:reviewer]\ndescription: Reviewer profile only\nargs:\n  - {name: target, required: true}\n---\n{{target}}\n",
		"universal.md":    "---\ndescription: All agents\nargs:\n  - {name: target, required: true}\n---\n{{target}}\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	m := newModel(config.Default(), "")
	m.Sessions.Set([]*protocol.SessionInfo{
		{Id: "s-claude-rev", Agent: "claude", Profile: "reviewer", Project: "/tmp/p"},
		{Id: "s-claude-writer", Agent: "claude", Profile: "writer", Project: "/tmp/p"},
		{Id: "s-codex", Agent: "codex", Project: "/tmp/p"},
	})

	loaded, broken := prompttmpl.LoadAllMetadata(dir)
	if len(broken) != 0 {
		t.Fatalf("unexpected broken templates: %+v", broken)
	}

	cases := []struct {
		sessionID string
		draft     string
		wantHint  bool
	}{
		{"s-claude-rev", "//claudeonly ", true},
		{"s-claude-rev", "//revieweronly ", true},
		{"s-claude-rev", "//codexonly ", false},
		{"s-claude-rev", "//universal ", true},

		{"s-claude-writer", "//claudeonly ", true},
		{"s-claude-writer", "//revieweronly ", false},
		{"s-claude-writer", "//codexonly ", false},
		{"s-claude-writer", "//universal ", true},

		{"s-codex", "//codexonly ", true},
		{"s-codex", "//claudeonly ", false},
		{"s-codex", "//revieweronly ", false},
		{"s-codex", "//universal ", true},

		{"s-missing", "//claudeonly ", false},
		{"s-missing", "//universal ", true},
	}

	for _, tc := range cases {
		st := m.chatState(tc.sessionID)
		st.templates.templates.Set(loaded)
		st.draft.Set(tc.draft)
		got := ggui.Untrack(func() ggui.Widget {
			return m.templateHint(tc.sessionID, st)
		})
		if (got != nil) != tc.wantHint {
			t.Errorf("session=%s draft=%q: templateHint != nil is %v, want %v", tc.sessionID, tc.draft, got != nil, tc.wantHint)
		}
	}

	// Verify rendered composerMenu hides mismatched agent/profile hints and shows matching ones.
	stWriter := m.chatState("s-claude-writer")
	stWriter.templates.templates.Set(loaded)
	p := ggui.ProbeBuilder(func() ggui.Widget { return composer(m, "s-claude-writer", stWriter) }, ggui.Sz(600, 400))
	defer p.Close()

	m.setComposerDraft("s-claude-writer", "//revieweronly x")
	p.Frame()
	if _, ok := p.Semantics().Find(ggui.RoleText, "//revieweronly"); ok {
		t.Fatal("composerMenu rendered //revieweronly hint for claude/writer session")
	}

	m.setComposerDraft("s-claude-writer", "//claudeonly x")
	p.Frame()
	if _, ok := p.Semantics().Find(ggui.RoleText, "//claudeonly"); !ok {
		t.Fatal("composerMenu did not render //claudeonly hint for claude/writer session")
	}
}

// Running a template host call (such as //agent-workflow's await mako.agents())
// uses the GUI's shared daemonLink client and must never close it; closing the
// shared client cancels its context and tears down every active terminal,
// watch stream, and poll in the window.
func TestTemplateHostDoesNotCloseSharedDaemonClient(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MAKO_HOME", home)

	cfg := config.Default()
	cfg.P2P.Disabled = true
	cfg.Discovery = config.Discovery{}
	cfg.Agents = map[string]agentspec.Agent{
		"claude": {Command: []string{"claude"}},
		"codex":  {Command: []string{"codex"}},
	}
	d := daemon.New(cfg, slog.New(slog.DiscardHandler))
	dctx, dcancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- d.Run(dctx) }()
	t.Cleanup(func() {
		dcancel()
		<-done
	})
	select {
	case <-d.Ready():
	case err := <-done:
		t.Fatalf("daemon exited before ready: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not become ready")
	}

	m := newModel(cfg, d.Socket())
	m.link.setEmbedded(d.Socket())
	m.link.setLinked(true)
	t.Cleanup(func() { m.link.hangUp() })

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	shared, err := m.link.client(ctx)
	if err != nil {
		t.Fatalf("initial link.client: %v", err)
	}
	if _, err := shared.Nodes(ctx); err != nil {
		t.Fatalf("initial Nodes: %v", err)
	}

	m.Sessions.Set([]*protocol.SessionInfo{{Id: "s", NodeId: cfg.NodeID, Agent: "claude", Project: home}})
	m.Nodes.Set([]*protocol.Node{d.Self()})
	// Empty AgentCatalogs forces guiTemplateHost.Agents to call ListAgentModels
	// through m.link.client for every offered agent.
	m.AgentCatalogs.Set(nil)

	host := m.templateHost("s")
	if _, err := host.Agents(ctx, "", false); err != nil {
		t.Fatalf("host.Agents: %v", err)
	}
	// Decide may return an error when no decision key is configured, but must
	// still leave the shared daemon client open.
	_, _ = host.Decide(ctx, []byte(`{}`))

	after, err := m.link.client(ctx)
	if err != nil {
		t.Fatalf("link.client after template host calls: %v", err)
	}
	if after != shared {
		t.Fatal("template host replaced the shared daemon client")
	}
	if _, err := shared.Nodes(ctx); err != nil {
		t.Fatalf("shared daemon client was closed by template host: %v", err)
	}
}
