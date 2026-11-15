package gui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
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
	if strings.Join(got, ",") != "//review" {
		t.Fatalf("listed %v, want only the template for Claude", got)
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
	if got, want := sentThrough(p, link), "Review my file.go 3 deep as claude in /tmp/p.\r"; got != want {
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
