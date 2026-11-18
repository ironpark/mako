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

// Settings ▸ Templates lists the built-ins, to read, marked built in. A
// built-in's name is not taken by a new template; Copy to edit makes a
// copy that, saved, stands in for it, rest argument and all, and once
// deleted gives it back.
func TestTemplateSettingsBuiltins(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	dir, _ := config.TemplatesDir()
	m := newModel(config.Default(), "")
	pg := newTemplatePage()
	shown := func() []string {
		var out []string
		for _, l := range ggui.Untrack(pg.shownBuiltins) {
			out = append(out, l.T.Name)
		}
		return out
	}
	if got := shown(); len(got) != 1 || got[0] != prompttmpl.MakerName {
		t.Fatalf("built-ins listed: %v", got)
	}
	if err := pg.add(prompttmpl.MakerName); err == nil {
		t.Fatal("a new template took a built-in's name")
	}

	pg.copyBuiltin(prompttmpl.BuiltinTemplate(prompttmpl.MakerName))
	if got := shown(); len(got) != 0 {
		t.Fatalf("a copy left the built-in listed: %v", got)
	}
	id := ggui.Peek(pg.open)
	if d := pg.draft(id); d.From != prompttmpl.MakerName || d.File != "" || !d.edited() || len(d.Args) != 1 || !d.Args[0].Rest {
		t.Fatalf("the copy: %+v", d)
	}
	pg.save(id)
	if e := pg.draft(id).Err; e != "" {
		t.Fatalf("save: %s", e)
	}
	saved, err := prompttmpl.Load(dir, prompttmpl.MakerName)
	if err != nil || saved.Builtin || !saved.Args[0].Rest || saved.Body != prompttmpl.BuiltinTemplate(prompttmpl.MakerName).Body {
		t.Fatalf("saved %+v, %v", saved, err)
	}

	// Read again, the file stands in for the built-in still.
	pg.reload()
	if got := shown(); len(got) != 0 {
		t.Fatalf("after a reload: %v", got)
	}
	id = ggui.Peek(pg.drafts)[0].ID
	pg.remove(m, id)
	if _, err := os.Stat(filepath.Join(dir, prompttmpl.MakerName+".md")); !os.IsNotExist(err) {
		t.Fatal("the copy's file stayed")
	}
	if got := shown(); len(got) != 1 {
		t.Fatalf("deleting the copy did not give the built-in back: %v", got)
	}
}

// The page shows a built-in's file and Copy to edit beside the list.
func TestTemplateSettingsShowsABuiltin(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	p := ggui.ProbeBuilder(func() ggui.Widget { return settingsContent(m, "templates") }, ggui.Sz(1200, 1400))
	defer p.Close()
	m.post = p.Post // the file's highlighting comes back through it
	p.Frame()
	sem := p.Semantics().String()
	for _, want := range []string{"//" + prompttmpl.MakerName, T("Built in"), T("Copy to edit"), "save_template"} {
		if !strings.Contains(sem, want) {
			t.Errorf("the page has no %q", want)
		}
	}
	p.Tap(T("Copy to edit"))
	p.Frame()
	if sem := p.Semantics().String(); !strings.Contains(sem, Tf("Stands in for the built-in //%s; deleting it brings the built-in back.", prompttmpl.MakerName)) {
		t.Fatalf("the copy's editor:\n%.2000s", sem)
	}
}
