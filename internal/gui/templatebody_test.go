package gui

import (
	"slices"
	"strings"
	"testing"

	"github.com/ironpark/ggui"
	uitheme "github.com/ironpark/ggui/ui/theme"

	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/prompttmpl"
)

// A {{name}} is coloured as an argument's, a built-in variable's, or one
// of neither; and coloured again once the arguments' names change, though
// the text does not.
func TestPlaceholderColors(t *testing.T) {
	args := []argDraft{{Name: "file"}}
	colors := memoColors(func() []argDraft { return args })
	got := colors("{{file}} as {{ agent }} in {{tone}}")
	if len(got) != 3 || got[0].Start != 0 || got[0].End != 8 {
		t.Fatalf("spans %+v", got)
	}
	if got[0].Color != skin.AccentBright || got[1].Color != skin.DotDone || got[2].Color != uitheme.Destructive {
		t.Fatalf("colours %+v", got)
	}
	args = []argDraft{{Name: "file"}, {Name: "tone"}}
	if got := colors("{{file}} as {{ agent }} in {{tone}}"); got[2].Color != skin.AccentBright {
		t.Fatalf("an argument added is still coloured %v", got[2].Color)
	}
}

// The list offers the arguments, then the built-in variables, those whose
// names begin as typed before those that hold it, and says what each
// built-in variable is; a block's tags for a tag begun; and for an arm, the
// values of the block's subject, then else.
func TestPlaceholderOptions(t *testing.T) {
	query := func(kind, typed, block string) placeholderQuery {
		return placeholderQuery{Open: true, Completion: prompttmpl.Completion{Kind: kind, Typed: typed, Block: block}}
	}
	if opts := placeholderOptions(nil, query(prompttmpl.CompleteTag, "#ma", "")); len(opts) != 1 || opts[0].Shown != "{#match}" {
		t.Fatalf("the block's tag not offered: %+v", opts)
	}
	tone := []argDraft{{Name: "tone", Type: prompttmpl.Choice, Options: "kind, blunt"}}
	var arms []string
	for _, o := range placeholderOptions(tone, query(prompttmpl.CompleteArm, "", "tone")) {
		arms = append(arms, o.Shown)
	}
	if !slices.Equal(arms, []string{"{:kind}", "{:blunt}", "{:else}"}) {
		t.Fatalf("arms offered %v", arms)
	}
	opts := placeholderOptions([]argDraft{{Name: "file", Type: prompttmpl.String}, {Name: "date"}}, query(prompttmpl.CompleteSubject, "D", ""))
	var names []string
	for _, o := range opts {
		names = append(names, o.Name)
	}
	if !slices.Equal(names, []string{"date", "model"}) || opts[0].Color != skin.AccentBright || opts[1].Color != skin.DotDone {
		t.Fatalf("options %+v", opts)
	}
	for _, b := range prompttmpl.Builtins {
		if builtinLabels[b] == "" {
			t.Errorf("built-in variable %s says nothing of itself", b)
		}
	}
}

// Typed {{ and the start of a name, the list offers what begins so, and
// Enter puts in the name closed.
func TestPlaceholderCompletesInTheEditor(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	pg := newTemplatePage()
	if err := pg.add("review"); err != nil {
		t.Fatal(err)
	}
	id := ggui.Peek(pg.open)
	pg.addArg(id)
	pg.update(id, func(d *templateDraft) { d.Args[0].Name = "file" })
	mono := ggui.TextStyle{Font: ggui.DefaultMonoFont()}
	p := ggui.ProbeBuilder(func() ggui.Widget { return templateBody(pg, id, mono) }, ggui.Sz(500, 400))
	defer p.Close()
	p.Frame()
	field, ok := p.Semantics().Find(ggui.RoleTextField, T("Template prompt"))
	if !ok {
		t.Fatalf("no prompt:\n%s", p.Semantics().String())
	}
	p.Click(field.Rect.Origin.Add(ggui.Pt(10, 10)))
	p.Frame()
	p.Text("Review {{f")
	for range 3 {
		p.Frame()
	}
	if sem := p.Semantics().String(); !strings.Contains(sem, "{{file}}") {
		t.Fatalf("no option offered:\n%s", sem)
	}
	p.Key("enter")
	for range 3 {
		p.Frame()
	}
	if got := pg.draft(id).Body; got != "Review {{file}}" {
		t.Fatalf("the prompt is %q", got)
	}
	if sem := p.Semantics().String(); strings.Contains(sem, T(templateTypeLabels["string"])) {
		t.Fatalf("the list stayed:\n%s", sem)
	}
	// A block is typed tag by tag: its tag, the name it matches, an arm.
	pg.addArg(id)
	pg.update(id, func(d *templateDraft) {
		d.Args[1].Name, d.Args[1].Type, d.Args[1].Options = "tone", prompttmpl.Choice, "kind, blunt"
	})
	for _, step := range []struct{ typed, want string }{
		{"\n{#ma", "Review {{file}}\n{#match "},
		{"to", "Review {{file}}\n{#match tone}"},
		{"\n{:", "Review {{file}}\n{#match tone}\n{:kind}"},
	} {
		p.Text(step.typed)
		for range 3 {
			p.Frame()
		}
		p.Key("enter")
		for range 3 {
			p.Frame()
		}
		if got := pg.draft(id).Body; got != step.want {
			t.Fatalf("after %q the prompt is %q", step.typed, got)
		}
	}
}

// A script's fences are coloured as a block's tags are; a {{name}} in its
// code is no placeholder.
func TestScriptColours(t *testing.T) {
	text := "a\n```js run\nvar x = \"{{nope}}\"\n```\nb"
	got := placeholderColors(text, nil)
	if len(got) != 2 || text[got[0].Start:got[0].End] != "```js run" || text[got[1].Start:got[1].End] != "```" {
		t.Fatalf("colours %+v", got)
	}
}
