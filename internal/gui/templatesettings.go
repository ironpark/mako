package gui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	uitheme "github.com/ironpark/ggui/ui/theme"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/atomicfile"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/prompttmpl"
)

// Settings ▸ Templates edits the prompt templates in the templates folder:
// each its name, what it is for, the agents it is offered to, the
// arguments it takes and their types, and its prompt. A template is saved
// to its file on its own, and checked as the composer would read it
// first. A file that does not read is listed with why, to fix by hand.

// templateDraft is a template as the page edits it.
type templateDraft struct {
	ID int
	// File is the name the template was saved under, "" until it is.
	File string
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
	Required                         bool
}

// templateTypeLabels name the types, in English for T.
var templateTypeLabels = map[string]string{
	"string": "Text, one line", "text": "Text, several lines", "integer": "Whole number",
	"number": "Number", "boolean": "True or false", "choice": "Choice",
}

// templatePage is the page's state while it is shown.
type templatePage struct {
	drafts *ggui.StateValue[[]templateDraft]
	broken *ggui.StateValue[[]prompttmpl.Broken]
	open   *ggui.StateValue[int] // the draft unfolded, 0 for none
	nextID int
}

func newTemplatePage() *templatePage {
	pg := &templatePage{
		drafts: ggui.State[[]templateDraft](nil),
		broken: ggui.State[[]prompttmpl.Broken](nil).WithEqual(deepEqual),
		open:   ggui.State(0),
	}
	pg.reload()
	return pg
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
	d := templateDraft{ID: pg.id(), File: t.Name, templateFields: templateFields{
		Name: t.Name, Description: t.Description, Agents: strings.Join(t.Agents, ", "), Body: t.Body,
	}}
	for _, a := range t.Args {
		ad := argDraft{ID: pg.id(), Name: a.Name, Type: a.Type, Options: strings.Join(a.Options, ", "),
			Describe: a.Description, Required: a.Required}
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
			Description: strings.TrimSpace(a.Describe), Required: a.Required}
		if arg.Type == prompttmpl.Choice {
			arg.Options = splitList(a.Options)
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
		err = atomicfile.Write(filepath.Join(dir, t.Name+".md"), data)
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
		d.Saved = d.templateFields
	})
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

// Page ------------------------------------------------------------------------

func templateSettings(m *model) ggui.Widget {
	pg := newTemplatePage()
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
		return b.String()
	})
	// Beside the list, on a wide page, the template open shows, or the
	// first while none is.
	shown := ggui.Derived(func() int {
		drafts, o := pg.drafts.Get(), pg.open.Get()
		if slices.ContainsFunc(drafts, func(d templateDraft) bool { return d.ID == o }) || len(drafts) == 0 {
			return o
		}
		return drafts[0].ID
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
		items := func(inline bool) ggui.Widget {
			if len(drafts) == 0 {
				return card(widget.Box(muted(widget.Text(T("No templates yet. Add one below.")).Size(13))).Pad(14, 16))
			}
			items := make([]ggui.Widget, len(drafts))
			for i, d := range drafts {
				items[i] = templateItem(m, pg, shown, d, inline)
			}
			return card(items...)
		}
		detail := ggui.Key(shown, func(id int) ggui.Widget {
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
	children := []ggui.Widget{
		widget.Row(
			widget.Expanded(compactField(T("Name"), T("Called as //name. Renaming it renames its file."),
				ui.TextField(pg.field(id, func(d *templateDraft) *string { return &d.Name })).Style(mono).Name(T("Template name")))),
			widget.Expanded(compactField(T("Agents"), T("The agents and profiles it is offered to; none checked for every agent."),
				templateAgents(m, pg.field(id, func(d *templateDraft) *string { return &d.Agents })))),
		).Gap(10),
		compactField(T("Description"), T("Shown beside its name in Chat's list."),
			ui.TextField(pg.field(id, func(d *templateDraft) *string { return &d.Description })).Name(T("Template description"))),
	}
	for _, a := range d.Args {
		children = append(children, argCard(pg, id, a, mono))
	}
	children = append(children,
		widget.Row(ui.ButtonOf(widget.Row(icon("plus").Size(14), widget.Text(T("Add argument")).Size(13)).Gap(6),
			func() { pg.addArg(id) }).Ghost().Pad(6, 10).Radius(7).Name(T("Add argument"))),
		compactField(T("Prompt"), T("What it sends. {{name}} stands for an argument's value or a built-in variable's; {#match name} picks text by it, or by a JavaScript expression, each arm begun by {:pattern} or {:else}, and {/match} closes it; type {{ or { to pick. A ```js block gives way to the value its JavaScript ends on, with args and vars to read; a ```js run block only runs."),
			templateBody(pg, id, mono)),
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
			m.askConfirm(confirmation{
				Title:  Tf("Delete //%s?", ggui.Untrack(func() string { return pg.draft(id).Name })),
				Body:   T("Its file is deleted from the templates folder."),
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
// whether it is required, its default, a choice's options and what it is.
func argCard(pg *templatePage, id int, a argDraft, mono ggui.TextStyle) ggui.Widget {
	arg := a.ID
	kind := argField(pg, id, arg, func(a *argDraft) *prompttmpl.Type { return &a.Type })
	required := argField(pg, id, arg, func(a *argDraft) *bool { return &a.Required })
	head := widget.Row(
		icon("type").Size(14).Color(skin.Muted),
		widget.Box(ui.TextField(argField(pg, id, arg, func(a *argDraft) *string { return &a.Name })).
			Style(mono).Name(T("Argument name"))).Width(150),
		widget.Box(ui.Select(kind).Options(prompttmpl.Types).Format(func(t prompttmpl.Type) string { return T(templateTypeLabels[string(t)]) }).
			Name(T("Argument type"))).Width(170),
		ui.Switch(required, T("Required")).Name(T("Required")),
		widget.Spacer(),
		ui.ButtonOf(icon("trash-2").Size(14), func() { pg.removeArg(id, arg) }).Ghost().Pad(6).Radius(6).
			Name(T("Remove argument")),
	).Gap(8).Align(widget.AlignCenter)
	isChoice := ggui.Map(kind, func(k prompttmpl.Type) bool { return k == prompttmpl.Choice })
	return widget.Box(widget.Column(
		head,
		ggui.If(isChoice, func() ggui.Widget {
			return compactField(T("Options"), T("The values it takes, with commas between."),
				ui.TextField(argField(pg, id, arg, func(a *argDraft) *string { return &a.Options })).
					Style(mono).Placeholder("kind, blunt").Name(T("Argument options")))
		}),
		widget.Row(
			widget.Expanded(compactField(T("Default"), T("Its value when a call leaves it out; a required argument has none."),
				ui.TextField(argField(pg, id, arg, func(a *argDraft) *string { return &a.Default })).
					Style(mono).Name(T("Argument default")).BindDisabled(required))),
			widget.Expanded(compactField(T("Description"), T("What it is, for you to remember."),
				ui.TextField(argField(pg, id, arg, func(a *argDraft) *string { return &a.Describe })).
					Name(T("Argument description")))),
		).Gap(10),
	).Gap(8).Stretch()).Border(1, skin.Border).Radius(8).Pad(10, 12)
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
