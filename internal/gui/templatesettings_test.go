package gui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/prompttmpl"
)

// The page saves a template to its file as the composer reads it, refuses
// one that would not read, renames the file with the template, and
// deletes it.
func TestTemplateSettingsSaveRenameAndDelete(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	dir, _ := config.TemplatesDir()
	m := newModel(config.Default(), "")
	pg := newTemplatePage()
	if err := pg.add("bad name"); err == nil {
		t.Fatal("a name with a space was taken")
	}
	if err := pg.add("review"); err != nil {
		t.Fatal(err)
	}
	if err := pg.add("review"); err == nil {
		t.Fatal("a name taken twice")
	}
	id := ggui.Peek(pg.open)
	pg.update(id, func(d *templateDraft) {
		d.Description = "Review a file"
		d.Agents = "claude, codex:fast"
		d.Body = "Review {{file}} in {{tone}} tone as {{agent}}."
	})
	pg.addArg(id)
	pg.addArg(id)
	pg.update(id, func(d *templateDraft) {
		d.Args[0].Name, d.Args[0].Required = "file", true
		d.Args[1].Name, d.Args[1].Type, d.Args[1].Options, d.Args[1].Default = "tone", "choice", "kind, blunt", "kind"
	})
	pg.save(id)
	if e := ggui.Peek(pg.drafts)[0].Err; e != "" {
		t.Fatalf("save: %s", e)
	}
	got, broken := prompttmpl.LoadDir(dir)
	if len(got) != 1 || len(broken) != 0 {
		t.Fatalf("read back %v, %v", got, broken)
	}
	tm := got[0]
	if tm.Name != "review" || strings.Join(tm.Agents, ",") != "claude,codex:fast" || tm.Usage() != "<file:string> [tone:kind|blunt=kind]" {
		t.Fatalf("saved %+v, usage %s", tm, tm.Usage())
	}
	if ggui.Peek(pg.drafts)[0].edited() {
		t.Fatal("a saved template reads as edited")
	}

	// A prompt naming what is neither an argument nor built in is not saved.
	pg.update(id, func(d *templateDraft) { d.Body = "{{nope}}" })
	pg.save(id)
	if e := ggui.Peek(pg.drafts)[0].Err; !strings.Contains(e, "nope") {
		t.Fatalf("saved a template that does not read: %q", e)
	}
	pg.revert(id)
	if d := ggui.Peek(pg.drafts)[0]; d.edited() || d.Body != "Review {{file}} in {{tone}} tone as {{agent}}." {
		t.Fatalf("revert left %+v", d)
	}

	pg.update(id, func(d *templateDraft) { d.Name = "check" })
	pg.save(id)
	if _, err := os.Stat(filepath.Join(dir, "review.md")); !os.IsNotExist(err) {
		t.Fatal("the old file stayed after a rename")
	}
	if _, err := os.Stat(filepath.Join(dir, "check.md")); err != nil {
		t.Fatal(err)
	}

	pg.remove(m, id)
	if got, _ := prompttmpl.LoadDir(dir); len(got) != 0 || len(ggui.Peek(pg.drafts)) != 0 {
		t.Fatal("a deleted template stayed")
	}
}

// The page lays out with a template unfolded, and lists a file that does
// not read.
func TestTemplateSettingsRenders(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	dir, _ := config.TemplatesDir()
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, "a.md"), []byte("---\nargs: [{name: x, type: choice, options: [p, q]}]\n---\n{{x}}\n"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "broken.md"), []byte("{{nope}}"), 0o600)
	m := newModel(config.Default(), "")
	p := ggui.ProbeBuilder(func() ggui.Widget { return settingsContent(m, "templates") }, ggui.Sz(760, 1400))
	defer p.Close()
	p.Frame()
	sem := p.Semantics().String()
	for _, want := range []string{Tf("%s template", "a"), "broken.md"} {
		if !strings.Contains(sem, want) {
			t.Fatalf("no %q on the page:\n%s", want, sem)
		}
	}
	item, ok := p.Semantics().Find(ggui.RoleButton, Tf("%s template", "a"))
	if !ok {
		t.Fatalf("no template item:\n%s", sem)
	}
	p.Click(item.Rect.Origin.Add(ggui.Pt(10, item.Rect.Size.H/2)))
	for range 30 {
		p.Frame()
	}
	if _, ok := p.Semantics().Find(ggui.RoleTextField, T("Template prompt")); !ok {
		t.Fatalf("the editor did not unfold:\n%s", p.Semantics().String())
	}
}

// On a wide page the list has the template chosen beside it, the first
// until another is tapped.
func TestTemplateSettingsBesideTheList(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	dir, _ := config.TemplatesDir()
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, "a.md"), []byte("{{date}}\n"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "b.md"), []byte("{{repo}}\n"), 0o600)
	m := newModel(config.Default(), "")
	p := ggui.ProbeBuilder(func() ggui.Widget { return settingsContent(m, "templates") }, ggui.Sz(1100, 1000))
	defer p.Close()
	p.Frame()
	name := func() string {
		f, ok := p.Semantics().Find(ggui.RoleTextField, T("Template name"))
		if !ok {
			t.Fatalf("no editor beside the list:\n%s", p.Semantics().String())
		}
		return f.Value
	}
	if got := name(); got != "a" {
		t.Fatalf("shown %q first, want a", got)
	}
	item, _ := p.Semantics().Find(ggui.RoleButton, Tf("%s template", "b"))
	p.Click(item.Rect.Origin.Add(ggui.Pt(10, item.Rect.Size.H/2)))
	for range 5 {
		p.Frame()
	}
	if got := name(); got != "b" {
		t.Fatalf("shown %q once b was tapped", got)
	}
}
