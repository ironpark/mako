package gui

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	uitheme "github.com/ironpark/ggui/ui/theme"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/prompttmpl"
	"github.com/ironpark/mako/pkg/fsutil"
)

// Settings ▸ Templates edits the prompt templates in the templates folder:
// each its name, what it is for, the agents it is offered to, the
// arguments it takes and their types, and its prompt. A template is saved
// to its file on its own, and checked as the composer would read it
// first. A file that does not read is listed with why, to fix by hand.
// The built-in templates are listed too, to read: Copy to edit makes a
// template of the user's of the name, which stands in for the built-in
// until it is deleted.

// templateDraft is a template as the page edits it.
type templateDraft struct {
	ID int
	// File is the name the template was saved under, "" until it is.
	File string
	// From is the built-in it stands in for, "" for none: one it was
	// copied from, or whose name its file has.
	From string
	templateFields
	// Saved is the template as it was last read or written, to tell
	// edits by.
	Saved templateFields
	Err   string // what was wrong when it was last saved
}

// templateFields are what the page edits of a template.
type templateFields struct {
	Name        string
	Description string
	Agents      string // by name, or agent:profile, commas or spaces between
	Body        string
	Args        []argDraft
}

// argDraft is an argument of a template as the page edits it.
type argDraft struct {
	ID                               int
	Name, Default, Options, Describe string
	Type                             prompttmpl.Type
	Required, Rest                   bool
}

// templateTypeLabels name the types, in English for T.
var templateTypeLabels = map[string]string{
	"string": "Text, one line", "text": "Text, several lines", "integer": "Whole number",
	"number": "Number", "boolean": "True or false", "choice": "Choice", "select": "Select",
}

// templatePage is the page's state while it is shown.
type templatePage struct {
	drafts *ggui.StateValue[[]templateDraft]
	// builtins are the built-in templates, each with the ID of its line.
	builtins []builtinLine
	broken   *ggui.StateValue[[]prompttmpl.Broken]
	open     *ggui.StateValue[int] // the draft unfolded, 0 for none
	onChange func()
	nextID   int
}

// builtinLine is a built-in template as the page lists it.
type builtinLine struct {
	ID int
	T  *prompttmpl.Template
}

func newTemplatePage() *templatePage {
	pg := &templatePage{
		drafts: ggui.State[[]templateDraft](nil),
		broken: ggui.State[[]prompttmpl.Broken](nil).WithEqual(deepEqual),
		open:   ggui.State(0),
	}
	for _, b := range prompttmpl.BuiltinTemplates() {
		pg.builtins = append(pg.builtins, builtinLine{ID: pg.id(), T: b})
	}
	pg.reload()
	return pg
}

// shownBuiltins are the built-in templates no template of the user's, nor
// a file that does not read, stands in for. Reactive.
func (pg *templatePage) shownBuiltins() []builtinLine {
	drafts, broken := pg.drafts.Get(), pg.broken.Get()
	var out []builtinLine
	for _, b := range pg.builtins {
		name := b.T.Name
		if !slices.ContainsFunc(drafts, func(d templateDraft) bool { return d.File == name || d.From == name }) &&
			!slices.ContainsFunc(broken, func(x prompttmpl.Broken) bool { return x.Name == name }) {
			out = append(out, b)
		}
	}
	return out
}

// builtin is the built-in template whose line is id, nil for none.
func (pg *templatePage) builtin(id int) *prompttmpl.Template {
	for _, b := range pg.builtins {
		if b.ID == id {
			return b.T
		}
	}
	return nil
}

// copyBuiltin starts a template of the user's as a copy of the built-in
// t, unsaved, standing in for it once saved, and unfolds it.
func (pg *templatePage) copyBuiltin(t *prompttmpl.Template) {
	d := pg.draftOf(t)
	d.File, d.From = "", t.Name
	pg.drafts.Set(append(slices.Clone(ggui.Peek(pg.drafts)), d))
	pg.open.Set(d.ID)
}

func (pg *templatePage) id() int { pg.nextID++; return pg.nextID }

// reload reads the templates from their folder again, dropping edits.
func (pg *templatePage) reload() {
	dir, err := config.TemplatesDir()
	if err != nil {
		return
	}
	got, broken := prompttmpl.LoadDir(dir)
	drafts := make([]templateDraft, len(got))
	for i, t := range got {
		drafts[i] = pg.draftOf(t)
	}
	pg.drafts.Set(drafts)
	pg.broken.Set(broken)
}

// draftOf is a template read as the page edits it.
func (pg *templatePage) draftOf(t *prompttmpl.Template) templateDraft {
	d := templateDraft{ID: pg.id(), File: t.Name,
		Name: t.Name, Description: t.Description, Agents: strings.Join(t.Agents, ", "), Body: t.Body}
	if prompttmpl.BuiltinTemplate(t.Name) != nil {
		d.From = t.Name
	}
	for _, a := range t.Args {
		ad := argDraft{ID: pg.id(), Name: a.Name, Type: a.Type, Options: strings.Join(a.Options, ", "),
			Describe: a.Description, Required: a.Required, Rest: a.Rest}
		if a.Default != nil {
			ad.Default = *a.Default
		}
		d.Args = append(d.Args, ad)
	}
	d.Saved = d.templateFields
	return d
}

// template is the draft as a template, unchecked.
func (d templateFields) template() *prompttmpl.Template {
	t := &prompttmpl.Template{
		Name: strings.TrimSpace(d.Name), Description: strings.TrimSpace(d.Description),
		Agents: splitList(d.Agents), Body: d.Body,
	}
	for _, a := range d.Args {
		arg := prompttmpl.Arg{Name: strings.TrimSpace(a.Name), Type: a.Type,
			Description: strings.TrimSpace(a.Describe), Required: a.Required,
			Rest: a.Rest && (a.Type == prompttmpl.String || a.Type == prompttmpl.Text || a.Type == "")}
		if arg.Type.HasOptions() {
			arg.Options = splitOptions(a.Options)
		}
		if v := a.Default; strings.TrimSpace(v) != "" && !a.Required {
			arg.Default = &v
		}
		t.Args = append(t.Args, arg)
	}
	return t
}

// edited reports whether the draft differs from its file, or has none.
func (d templateDraft) edited() bool {
	return d.File == "" || !deepEqual(d.templateFields, d.Saved)
}

// splitList splits a list written with commas or spaces between.
func splitList(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' })
}

// splitOptions splits select/choice options on commas or newlines so values
// with spaces stay intact.
func splitOptions(s string) []string {
	var out []string
	for part := range strings.FieldsFuncSeq(s, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' }) {
		if part = strings.TrimSpace(part); part != "" && !slices.Contains(out, part) {
			out = append(out, part)
		}
	}
	return out
}

// update changes the draft id with f.
func (pg *templatePage) update(id int, f func(*templateDraft)) {
	drafts := slices.Clone(ggui.Peek(pg.drafts))
	for i := range drafts {
		if drafts[i].ID == id {
			drafts[i].Args = slices.Clone(drafts[i].Args)
			f(&drafts[i])
		}
	}
	pg.drafts.Set(drafts)
}

// draft is the draft id, its zero value once gone. Reactive.
func (pg *templatePage) draft(id int) templateDraft {
	for _, d := range pg.drafts.Get() {
		if d.ID == id {
			return d
		}
	}
	return templateDraft{}
}

// field binds one field of the draft id.
func (pg *templatePage) field(id int, f func(*templateDraft) *string) ggui.Binding[string] {
	return ggui.Bind(func() string { d := pg.draft(id); return *f(&d) },
		func(v string) { pg.update(id, func(d *templateDraft) { *f(d) = v }) })
}

// argField binds one field of the argument arg of the draft id.
func argField[T any](pg *templatePage, id, arg int, f func(*argDraft) *T) ggui.Binding[T] {
	return ggui.Bind(func() T {
		var zero T
		for _, a := range pg.draft(id).Args {
			if a.ID == arg {
				return *f(&a)
			}
		}
		return zero
	}, func(v T) {
		pg.update(id, func(d *templateDraft) {
			for i := range d.Args {
				if d.Args[i].ID == arg {
					*f(&d.Args[i]) = v
				}
			}
		})
	})
}

// add starts a template named name, unsaved, and unfolds it.
func (pg *templatePage) add(name string) error {
	name = strings.TrimSpace(name)
	if err := pg.checkName(0, name); err != nil {
		return err
	}
	d := templateDraft{ID: pg.id(), Name: name}
	pg.drafts.Set(append(slices.Clone(ggui.Peek(pg.drafts)), d))
	pg.open.Set(d.ID)
	return nil
}

// checkName finds what is wrong with name for the draft id.
func (pg *templatePage) checkName(id int, name string) error {
	if !prompttmpl.ValidName(name) {
		return errors.New(T("A template's name has no spaces, slashes or colons"))
	}
	// A built-in's name is taken but by a copy of it, made to stand in.
	if prompttmpl.BuiltinTemplate(name) != nil && !slices.ContainsFunc(ggui.Peek(pg.drafts), func(d templateDraft) bool { return d.ID == id && d.From == name }) {
		return fmt.Errorf(T("There is a built-in template named %s; copy it to edit it"), name)
	}
	for _, d := range ggui.Peek(pg.drafts) {
		if d.ID != id && (strings.TrimSpace(d.Name) == name || d.File == name) {
			return fmt.Errorf(T("There is already a template named %s"), name)
		}
	}
	return nil
}

// save checks the draft id as the composer would read it, and writes it
// to its file, under its new name when it was renamed.
func (pg *templatePage) save(id int) {
	d := ggui.Untrack(func() templateDraft { return pg.draft(id) })
	t := d.template()
	fail := func(err error) { pg.update(id, func(d *templateDraft) { d.Err = err.Error() }) }
	if err := pg.checkName(id, t.Name); err != nil {
		fail(err)
		return
	}
	data := prompttmpl.Format(t)
	if _, err := prompttmpl.Parse(t.Name, data); err != nil {
		fail(err)
		return
	}
	dir, err := config.TemplatesDir()
	if err == nil {
		err = os.MkdirAll(dir, 0o700)
	}
	if err == nil {
		err = fsutil.Write(filepath.Join(dir, t.Name+".md"), data)
	}
	if err == nil && d.File != "" && d.File != t.Name {
		err = os.Remove(filepath.Join(dir, d.File+".md"))
		if os.IsNotExist(err) {
			err = nil
		}
	}
	if err != nil {
		fail(err)
		return
	}
	pg.update(id, func(d *templateDraft) {
		d.File, d.Name, d.Err = t.Name, t.Name, ""
		if d.From != t.Name {
			d.From = "" // renamed, it stands in for none
		}
		d.Saved = d.templateFields
	})
	if pg.onChange != nil {
		pg.onChange()
	}
}

// revert puts the draft id back as its file has it.
func (pg *templatePage) revert(id int) {
	pg.update(id, func(d *templateDraft) {
		d.templateFields = d.Saved
		d.Err = ""
	})
}

// remove deletes the draft id, and its file once it has one.
func (pg *templatePage) remove(m *model, id int) {
	d := ggui.Untrack(func() templateDraft { return pg.draft(id) })
	if d.File != "" {
		dir, err := config.TemplatesDir()
		if err == nil {
			err = os.Remove(filepath.Join(dir, d.File+".md"))
		}
		if err != nil && !os.IsNotExist(err) {
			m.toaster.Push(ui.Toast(T("Could not delete the template"), err.Error()).Destructive())
			return
		}
	}
	pg.drafts.Set(slices.DeleteFunc(slices.Clone(ggui.Peek(pg.drafts)), func(x templateDraft) bool { return x.ID == id }))
	if m != nil {
		m.invalidateSessionTemplates()
	}
	if pg.onChange != nil {
		pg.onChange()
	}
}

// addArg adds an argument to the draft id, named so it does not clash.
func (pg *templatePage) addArg(id int) {
	pg.update(id, func(d *templateDraft) {
		name := "arg"
		for n := 2; slices.ContainsFunc(d.Args, func(a argDraft) bool { return a.Name == name }); n++ {
			name = "arg" + strconv.Itoa(n)
		}
		d.Args = append(d.Args, argDraft{ID: pg.id(), Name: name, Type: prompttmpl.String})
	})
}

func (pg *templatePage) removeArg(id, arg int) {
	pg.update(id, func(d *templateDraft) {
		d.Args = slices.DeleteFunc(d.Args, func(a argDraft) bool { return a.ID == arg })
	})
}

func (pg *templatePage) moveArg(id, arg, delta int) {
	pg.update(id, func(d *templateDraft) {
		i := slices.IndexFunc(d.Args, func(a argDraft) bool { return a.ID == arg })
		j := i + delta
		if i >= 0 && j >= 0 && j < len(d.Args) {
			d.Args[i], d.Args[j] = d.Args[j], d.Args[i]
		}
	})
}

// Page ------------------------------------------------------------------------

func templateSettings(m *model) ggui.Widget {
	pg := newTemplatePage()
	if m != nil {
		pg.onChange = m.invalidateSessionTemplates
	}
	// The editors are rebuilt when templates or arguments come or go, not
	// as their text is typed, so a field keeps its focus.
	shape := ggui.Derived(func() string {
		var b strings.Builder
		for _, d := range pg.drafts.Get() {
			fmt.Fprintf(&b, "%d(", d.ID)
			for _, a := range d.Args {
				fmt.Fprintf(&b, "%d,", a.ID)
			}
			b.WriteString(")")
		}
		for _, l := range pg.shownBuiltins() {
			fmt.Fprintf(&b, "b%d", l.ID)
		}
		return b.String()
	})
	// Beside the list, on a wide page, the template open shows, or the
	// first while none is.
	shown := ggui.Derived(func() int {
		drafts, builtins, o := pg.drafts.Get(), pg.shownBuiltins(), pg.open.Get()
		switch {
		case slices.ContainsFunc(drafts, func(d templateDraft) bool { return d.ID == o }),
			slices.ContainsFunc(builtins, func(l builtinLine) bool { return l.ID == o }):
			return o
		case len(drafts) > 0:
			return drafts[0].ID
		case len(builtins) > 0:
			return builtins[0].ID
		}
		return o
	})
	broken := ggui.View(pg.broken, func(broken []prompttmpl.Broken) ggui.Widget {
		if len(broken) == 0 {
			return widget.Box()
		}
		rows := make([]ggui.Widget, len(broken))
		for i, b := range broken {
			rows[i] = row(b.Name+".md", b.Err.Error(), widget.Box())
		}
		return section(T("Could not read"), card(rows...))
	})

	name := ggui.State("")
	note := ggui.State("")
	add := func() {
		if err := pg.add(ggui.Peek(name)); err != nil {
			note.Set(err.Error())
			return
		}
		name.Set("")
		note.Set("")
	}
	addCard := func(compact bool) ggui.Widget {
		files := widget.Row(
			ui.Button(T("Open folder"), openFolder(config.TemplatesDir)).Ghost(),
			ui.Button(T("Reload"), pg.reload).Ghost(),
		).Gap(6)
		hint := T("Each template is a Markdown file in the templates folder, which you can also edit by hand.")
		var folder ggui.Widget = stackedRow(T("Template files"), hint, files)
		if compact {
			folder = widget.Box(ui.Tooltip(files, hint)).Pad(6, 8)
		}
		return card(
			addField(T("Add a template"), T("Called in Chat by its name after two slashes."), "review", T("New template name"), name, note, add, compact),
			folder,
		)
	}
	list := ggui.Key(shape, func(string) ggui.Widget {
		drafts := ggui.Peek(pg.drafts)
		builtins := ggui.Untrack(pg.shownBuiltins)
		items := func(inline bool) ggui.Widget {
			var items []ggui.Widget
			if len(drafts) == 0 {
				items = append(items, widget.Box(muted(widget.Text(T("No templates of yours yet. Add one below.")).Size(13))).Pad(14, 16))
			}
			for _, d := range drafts {
				items = append(items, templateItem(m, pg, shown, d, inline))
			}
			for _, l := range builtins {
				items = append(items, builtinItem(m, pg, shown, l, inline))
			}
			return card(items...)
		}
		detail := ggui.Key(shown, func(id int) ggui.Widget {
			if b := pg.builtin(id); b != nil {
				return builtinDetail(m, pg, b)
			}
			if id == 0 {
				return widget.Box()
			}
			return templateDetail(m, pg, pg.draft(id))
		})
		return listBeside(
			widget.Column(items(false), broken, addCard(true)).Gap(20).Stretch(),
			detail,
			widget.Column(items(true), broken, addCard(false)).Gap(pageGap).Stretch(),
		)
	})
	return listPage(T("Templates"), T("Prompts to send in Chat by typing // and a template's name, with arguments of the types you set. {{name}} in a prompt stands for an argument, or for a built-in variable: agent, profile, model, project, branch, repo or date."),
		list,
	)
}

// templateItem is a template's line in the list: its name and what it is
// for. Inline, tapped, it unfolds its editor; otherwise it is the one
// shown beside the list.
func templateItem(m *model, pg *templatePage, shown ggui.Readable[int], d templateDraft, inline bool) ggui.Widget {
	id := d.ID
	summary := ggui.Reactive(func() ggui.Widget {
		cur := pg.draft(id)
		text := cur.template().Usage()
		if cur.Description != "" {
			text = strings.TrimSpace(cur.Description + "  " + text)
		}
		if cur.edited() {
			text = T("Not saved") + " · " + text
		}
		return widget.Text(text).Size(11.5).Color(skin.ExtraMuted).NoWrap().Ellipsis()
	})
	title := ggui.Reactive(func() ggui.Widget {
		return widget.Text("//" + pg.draft(id).Name).Size(13.5).Weight(ggui.WeightMedium).NoWrap()
	})
	label := []ggui.Widget{icon("file-text").Size(15).Color(skin.Muted), title, widget.Expanded(summary)}
	if !inline {
		chosen := ggui.Map(shown, func(s int) bool { return s == id })
		return widget.Box(listItem(widget.Row(label...).Gap(10).Align(widget.AlignCenter), chosen,
			func() { pg.open.Set(id) }, Tf("%s template", d.Name))).Pad(4, 8)
	}
	isOpen := ggui.Map(pg.open, func(o int) bool { return o == id })
	head := ui.ButtonOf(widget.Row(append(label, chevron(isOpen, 13, skin.ExtraMuted))...).Gap(10).Align(widget.AlignCenter),
		func() { pg.open.Set(pick(ggui.Peek(pg.open) == id, 0, id)) }).
		Ghost().Pad(8, 10).Radius(7).Name(Tf("%s template", d.Name))
	return widget.Box(widget.Column(
		head,
		reveal(isOpen, func() ggui.Widget { return widget.Padding(templateEditor(m, pg, d), 6, 10, 10, 35) }),
	).Stretch()).Pad(4, 8)
}

// templateDetail is the template shown beside the list: its name over
// its editor.
func templateDetail(m *model, pg *templatePage, d templateDraft) ggui.Widget {
	id := d.ID
	return detailCard(widget.Row(
		icon("file-text").Size(18).Color(skin.Muted),
		ggui.Reactive(func() ggui.Widget {
			return widget.Text("//" + pg.draft(id).Name).Size(16).Weight(ggui.WeightSemibold).NoWrap()
		}),
	).Gap(10).Align(widget.AlignCenter), templateEditor(m, pg, d))
}

// templateEditor is an unfolded template's fields, its arguments, and
// what saves, reverts or deletes it.
func templateEditor(m *model, pg *templatePage, d templateDraft) ggui.Widget {
	id := d.ID
	mono := ggui.TextStyle{Font: ggui.DefaultMonoFont()}
	previewOpen := ggui.State(false)
	metaBar := ggui.Reactive(func() ggui.Widget {
		cur := pg.draft(id)
		usage := strings.TrimSpace("//" + cur.Name + " " + cur.template().Usage())
		badges := []ggui.Widget{
			widget.Expanded(widget.Text(usage).Size(12).Font(ggui.DefaultMonoFont()).Color(skin.Muted).NoWrap().Ellipsis()),
		}
		if cur.Body != "" && (strings.Contains(cur.Body, "```js") || strings.Contains(cur.Body, "{#") || strings.Contains(cur.Body, "{@")) {
			badges = append(badges, pill(T("Script"), skin.DotDone))
		}
		if strings.Contains(cur.Body, "mako.") {
			badges = append(badges, ui.Button(T("API keys…"), func() { m.Section.Set("apikeys") }).Ghost().Pad(4, 8).Name(T("Open API keys settings")))
		}
		return widget.Box(widget.Row(badges...).Gap(8).Align(widget.AlignCenter)).
			Fill(skin.Surface0).Border(1, skin.Border).Radius(7).Pad(6, 10)
	})
	children := []ggui.Widget{
		metaBar,
		widget.Row(
			widget.Expanded(compactField(T("Name"), T("Called as //name. Renaming it renames its file."),
				ui.TextField(pg.field(id, func(d *templateDraft) *string { return &d.Name })).Style(mono).Name(T("Template name")))),
			widget.Expanded(compactField(T("Agents"), T("The agents and profiles it is offered to; none checked for every agent."),
				templateAgents(m, pg.field(id, func(d *templateDraft) *string { return &d.Agents })))),
		).Gap(10),
		compactField(T("Description"), T("Shown beside its name in Chat's list."),
			ui.TextField(pg.field(id, func(d *templateDraft) *string { return &d.Description })).Name(T("Template description"))),
	}
	if d.From != "" {
		children = append(children, muted(widget.Text(Tf("Stands in for the built-in //%s; deleting it brings the built-in back.", d.From)).Size(12)))
	}
	for i, a := range d.Args {
		children = append(children, argCard(pg, id, a, i == 0, i == len(d.Args)-1, mono))
	}
	children = append(children,
		widget.Row(ui.ButtonOf(widget.Row(icon("plus").Size(14), widget.Text(T("Add argument")).Size(13)).Gap(6),
			func() { pg.addArg(id) }).Ghost().Pad(6, 10).Radius(7).Name(T("Add argument"))),
		compactField(T("Prompt"), T("What it sends, written as Svelte writes markup: {{name}} or {{expression}} for a value, and the blocks {#if}, {#each}, {#match} and {#snippet}, with {@const} and {@render}; type {{ or { to pick. A ```js block gives way to the value its JavaScript ends on; a ```js run block only runs, defining what later pieces read."),
			templateBody(pg, id, mono)),
		templatePreviewCard(pg, id, previewOpen, mono),
		ggui.Reactive(func() ggui.Widget {
			if e := pg.draft(id).Err; e != "" {
				return widget.Text(e).Size(12).Color(uitheme.Destructive)
			}
			return widget.Box()
		}),
	)
	edited := ggui.Derived(func() bool { return pg.draft(id).edited() })
	revertable := ggui.Derived(func() bool { return edited.Get() && pg.draft(id).File != "" })
	children = append(children, widget.Row(
		ui.Button(T("Save"), func() { pg.save(id) }).BindDisabled(not(edited)),
		ui.Button(T("Revert"), func() { pg.revert(id) }).Ghost().BindDisabled(not(revertable)),
		widget.Spacer(),
		ui.Button(T("Delete template"), func() {
			body := T("Its file is deleted from the templates folder.")
			if from := ggui.Untrack(func() string { return pg.draft(id).From }); from != "" {
				body = Tf("Its file is deleted from the templates folder, and the built-in //%s is used again.", from)
			}
			m.askConfirm(confirmation{
				Title:  Tf("Delete //%s?", ggui.Untrack(func() string { return pg.draft(id).Name })),
				Body:   body,
				Action: T("Delete"),
				Run:    func() { pg.remove(m, id) },
			})
		}).Ghost(),
	).Gap(8).Align(widget.AlignCenter))
	return widget.Column(children...).Gap(10).Stretch()
}

// not is b turned over.
func not(b ggui.Readable[bool]) ggui.Readable[bool] {
	return ggui.Map(b, func(v bool) bool { return !v })
}

// argCard is an argument of an unfolded template: its name, type,
// whether it is required, its default, a select/choice's options and what it is;
// the last, if it is text, whether it takes the rest of a call.
func argCard(pg *templatePage, id int, a argDraft, first, last bool, mono ggui.TextStyle) ggui.Widget {
	arg := a.ID
	kind := argField(pg, id, arg, func(a *argDraft) *prompttmpl.Type { return &a.Type })
	required := argField(pg, id, arg, func(a *argDraft) *bool { return &a.Required })
	optionsBind := argField(pg, id, arg, func(a *argDraft) *string { return &a.Options })
	defaultBind := argField(pg, id, arg, func(a *argDraft) *string { return &a.Default })

	typeChoices := ggui.Map(kind, func(k prompttmpl.Type) []prompttmpl.Type {
		if k == prompttmpl.Choice {
			return append(slices.Clone(prompttmpl.Types), prompttmpl.Choice)
		}
		return prompttmpl.Types
	})
	typeSelect := ggui.View(typeChoices, func(opts []prompttmpl.Type) ggui.Widget {
		return widget.Box(ui.Select(kind).Options(opts).Format(func(t prompttmpl.Type) string { return T(templateTypeLabels[string(t)]) }).
			Name(T("Argument type"))).Width(170)
	})

	head := widget.Row(
		icon("type").Size(14).Color(skin.Muted),
		widget.Box(ui.TextField(argField(pg, id, arg, func(a *argDraft) *string { return &a.Name })).
			Style(mono).Name(T("Argument name"))).Width(150),
		typeSelect,
		ui.Switch(required, T("Required")).Name(T("Required")),
		widget.Spacer(),
		ui.Tooltip(ui.ButtonOf(icon("chevron-up").Size(14), func() { pg.moveArg(id, arg, -1) }).
			Ghost().Pad(4).Radius(6).BindDisabled(ggui.Const(first)).Name(T("Move argument up")), T("Move up")),
		ui.Tooltip(ui.ButtonOf(icon("chevron-down").Size(14), func() { pg.moveArg(id, arg, 1) }).
			Ghost().Pad(4).Radius(6).BindDisabled(ggui.Const(last)).Name(T("Move argument down")), T("Move down")),
		ui.ButtonOf(icon("trash-2").Size(14), func() { pg.removeArg(id, arg) }).Ghost().Pad(6).Radius(6).
			Name(T("Remove argument")),
	).Gap(6).Align(widget.AlignCenter)

	hasOptions := ggui.Map(kind, func(k prompttmpl.Type) bool { return k.HasOptions() })
	isText := ggui.Map(kind, func(k prompttmpl.Type) bool { return last && (k == prompttmpl.String || k == prompttmpl.Text) })

	optionChips := ggui.Reactive(func() ggui.Widget {
		opts := splitOptions(optionsBind.Get())
		if len(opts) == 0 {
			return widget.Box()
		}
		chips := make([]ggui.Widget, 0, len(opts))
		for _, opt := range opts {
			val := opt
			rm := ui.ButtonOf(icon("x").Size(11).Color(skin.Muted), func() {
				next := slices.DeleteFunc(splitOptions(ggui.Peek(optionsBind)), func(x string) bool { return x == val })
				optionsBind.Set(strings.Join(next, ", "))
			}).Ghost().Pad(2).Radius(4).Name(Tf("Remove option %s", val))
			chips = append(chips, widget.Box(widget.Row(
				widget.Text(val).Size(12).Font(mono.Font).NoWrap(),
				rm,
			).Gap(4).Align(widget.AlignCenter)).Pad(2, 6).Radius(5).Fill(skin.Surface2).Border(1, skin.Border))
		}
		return widget.Wrap(chips...).Gap(6).RunGap(6)
	})

	defaultControl := ggui.Reactive(func() ggui.Widget {
		k := kind.Get()
		req := required.Get()
		switch {
		case k.HasOptions():
			opts := append([]string{""}, splitOptions(optionsBind.Get())...)
			if cur := defaultBind.Get(); cur != "" && !slices.Contains(opts, cur) {
				opts = append(opts, cur)
			}
			return ui.Select(defaultBind).Options(opts).Format(func(v string) string {
				if v == "" {
					return T("None")
				}
				return v
			}).BindDisabled(ggui.Const(req)).Name(T("Argument default"))
		case k == prompttmpl.Boolean:
			return ui.Select(defaultBind).Options([]string{"", "true", "false"}).Format(func(v string) string {
				if v == "" {
					return T("None")
				}
				return v
			}).BindDisabled(ggui.Const(req)).Name(T("Argument default"))
		default:
			return ui.TextField(defaultBind).Style(mono).Name(T("Argument default")).BindDisabled(ggui.Const(req))
		}
	})

	return widget.Box(widget.Column(
		head,
		ggui.If(isText, func() ggui.Widget {
			return ui.Switch(argField(pg, id, arg, func(a *argDraft) *bool { return &a.Rest }),
				T("Takes the rest of the call as typed, unquoted")).Name(T("Takes the rest"))
		}),
		ggui.If(hasOptions, func() ggui.Widget {
			return compactField(T("Options"), T("Predefined choices separated by commas."),
				widget.Column(
					optionChips,
					ui.TextField(optionsBind).Style(mono).Placeholder("dev, staging, prod").Name(T("Argument options")),
				).Gap(6).Stretch())
		}),
		widget.Row(
			widget.Expanded(compactField(T("Default"), T("Its value when a call leaves it out; a required argument has none."), defaultControl)),
			widget.Expanded(compactField(T("Description"), T("What it is, for you to remember."),
				ui.TextField(argField(pg, id, arg, func(a *argDraft) *string { return &a.Describe })).
					Name(T("Argument description")))),
		).Gap(10),
	).Gap(8).Stretch()).Border(1, skin.Border).Radius(8).Pad(10, 12)
}

func templatePreviewCard(pg *templatePage, id int, open *ggui.StateValue[bool], mono ggui.TextStyle) ggui.Widget {
	toggle := ui.ButtonOf(
		widget.Row(
			icon("flask-conical").Size(14).Color(skin.Muted),
			widget.Expanded(widget.Text(T("Preview")).Size(13).Weight(ggui.WeightMedium)),
			chevron(open, 14, skin.Muted),
		).Gap(8).Align(widget.AlignCenter),
		func() { open.Set(!ggui.Peek(open)) },
	).Ghost().Pad(6, 10).Radius(7).Name(T("Toggle template preview"))

	body := reveal(open, func() ggui.Widget {
		return ggui.Reactive(func() ggui.Widget {
			cur := pg.draft(id)
			t := cur.template()
			data := prompttmpl.Format(t)
			parsed, err := prompttmpl.Parse(cmp.Or(t.Name, "preview"), data)
			if err != nil {
				return widget.Box(widget.Text(err.Error()).Size(12).Color(uitheme.Destructive)).
					Pad(10, 12).Radius(7).Fill(skin.Surface0).Border(1, skin.Border)
			}
			if strings.Contains(cur.Body, "mako.") {
				return widget.Box(muted(widget.Text(T("This template calls live session scripts (mako.*), which execute when sent in Chat.")).Size(12))).
					Pad(10, 12).Radius(7).Fill(skin.Surface0).Border(1, skin.Border)
			}
			vals := map[string]string{}
			for _, a := range parsed.Args {
				switch {
				case a.Default != nil:
					vals[a.Name] = *a.Default
				case a.Type.HasOptions() && len(a.Options) > 0:
					vals[a.Name] = a.Options[0]
				case a.Type == prompttmpl.Boolean:
					vals[a.Name] = "false"
				case a.Type == prompttmpl.Integer, a.Type == prompttmpl.Number:
					vals[a.Name] = "1"
				default:
					vals[a.Name] = "<" + a.Name + ">"
				}
			}
			builtins := map[string]string{
				"agent": "claude", "profile": "default", "model": "sonnet",
				"project": "/path/to/project", "branch": "main", "repo": "mako",
				"date": time.Now().Format(time.DateOnly),
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			out, err := parsed.Fill(ctx, vals, builtins)
			if err != nil {
				return widget.Box(widget.Text(err.Error()).Size(12).Color(uitheme.Destructive)).
					Pad(10, 12).Radius(7).Fill(skin.Surface0).Border(1, skin.Border)
			}
			return widget.Box(widget.Text(out).Size(12).Style(mono)).
				Pad(10, 12).Radius(7).Fill(skin.Surface0).Border(1, skin.Border)
		})
	})

	return widget.Column(toggle, body).Gap(4).Stretch()
}

// templateAgents picks the agents a template is offered to, checked in a
// menu of those this node runs and their profiles, kept in agents as the
// list the template's file has, commas between. One the file names that
// this node does not run stays, checked, until it is unchecked.
func templateAgents(m *model, agents ggui.Binding[string]) ggui.Widget {
	// Read as saving reads it, each once.
	picked := func() []string {
		var out []string
		for _, a := range splitList(agents.Get()) {
			if !slices.Contains(out, a) {
				out = append(out, a)
			}
		}
		return out
	}
	set := func(list []string) { agents.Set(strings.Join(list, ", ")) }
	face := widget.Row(
		widget.Expanded(ggui.Reactive(func() ggui.Widget {
			list := picked()
			if len(list) == 0 {
				return muted(widget.Text(T("every agent")).Size(13).NoWrap())
			}
			return widget.Text(strings.Join(list, ", ")).Size(13).NoWrap().Ellipsis()
		})),
		icon("chevron-down").Size(14).Color(skin.Muted),
	).Gap(8).Align(widget.AlignCenter)
	// The menu is made again as the node's agents change, not as they are
	// checked, which would close it.
	return ggui.View(m.Agents, func(targets []string) ggui.Widget {
		targets = slices.Clone(targets)
		for _, a := range picked() {
			if !slices.Contains(targets, a) {
				targets = append(targets, a)
			}
		}
		every := ggui.Bind(func() bool { return len(picked()) == 0 }, func(bool) { set(nil) })
		items := []ggui.Widget{ui.MenuCheckbox(T("Every agent"), every), ui.MenuDivider()}
		for _, target := range targets {
			on := ggui.Bind(func() bool { return slices.Contains(picked(), target) }, func(v bool) {
				list := slices.DeleteFunc(picked(), func(a string) bool { return a == target })
				if v {
					list = append(list, target)
				}
				set(list)
			})
			items = append(items, ui.MenuCheckbox(targetLabel(target), on))
		}
		return ui.MenuOf(face, items...).Name(T("Template agents"))
	})
}
