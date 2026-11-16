package gui

import (
	"context"
	"errors"
	"io/fs"
	"slices"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	uitheme "github.com/ironpark/ggui/ui/theme"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/prompttmpl"
)

// Prompt templates: Markdown files in the templates folder beside
// gui.json (see internal/prompttmpl). A composer's draft that begins with
// two slashes, and has no space yet, lists the templates for the session's
// agent in the composer's menu (commands.go). Once a template's name is
// followed by a space, the menu's place shows the arguments it takes, and
// what is wrong with those given; Enter sends the prompt the template
// makes of them.

// templateMenu is the templates a composer's menu lists.
type templateMenu struct {
	templates *ggui.StateValue[[]*prompttmpl.Template]
	read      time.Time
	// failed is what was wrong with the last call Enter tried to send, and
	// the draft it was for; it goes once the draft changes.
	failed *ggui.StateValue[templateFailure]
	// running is set while a call's scripts run, off the UI goroutine.
	running bool
}

type templateFailure struct{ draft, why string }

func newTemplateMenu() *templateMenu {
	return &templateMenu{
		// Read again every while, and the same as often as not.
		templates: ggui.State[[]*prompttmpl.Template](nil).WithEqual(deepEqual),
		failed:    ggui.State(templateFailure{}),
	}
}

// templatesStale is how old the templates read are before a draft that
// calls one reads them again, for files written since.
const templatesStale = 2 * time.Second

// readTemplates reads the templates again, unless they were read lately
// and force is false. A file that does not read is left out.
func (m *model) readTemplates(state *chatState, force bool) {
	menu := state.templates
	if !force && time.Since(menu.read) < templatesStale {
		return
	}
	menu.read = time.Now()
	dir, err := config.TemplatesDir()
	if err != nil {
		return
	}
	got, _ := prompttmpl.LoadDir(dir)
	menu.templates.Set(got)
}

// templateItems are the templates for session id's agent whose names the
// draft, two slashes and no space yet, begins or holds. Reactive.
func (m *model) templateItems(id string, state *chatState, draft string) []menuItem {
	if strings.ContainsAny(draft, " \t\n") {
		return nil
	}
	call, _ := prompttmpl.ParseCall(draft)
	s, _ := sessionIn(m.Sessions.Get(), id)
	var ours []*prompttmpl.Template
	for _, t := range state.templates.templates.Get() {
		if t.For(s.GetAgent(), s.GetProfile()) {
			ours = append(ours, t)
		}
	}
	matches := matchByName(ours, strings.ToLower(call.Name), func(t *prompttmpl.Template) string { return t.Name })
	items := make([]menuItem, len(matches))
	for i, t := range matches {
		items[i] = menuItem{
			name: "//" + t.Name, usage: t.Usage(), description: t.Description, tag: T("template"),
			complete: "//" + t.Name + " ", take: func() { m.takeTemplate(id, t) },
		}
	}
	return items
}

// takeTemplate puts t in the composer: sent at once when it needs no
// argument given, and otherwise followed by a space to give them after.
func (m *model) takeTemplate(id string, t *prompttmpl.Template) {
	if slices.ContainsFunc(t.Args, func(a prompttmpl.Arg) bool { return a.Required }) {
		m.setComposerDraft(id, "//"+t.Name+" ")
		return
	}
	m.setComposerDraft(id, "//"+t.Name)
	m.sendComposer(id)
}

// expandDraft gives send what session id's draft sends: the prompt its
// template makes, when it calls one, read from its file as it is now;
// otherwise, and for a name no file has, the draft as it is. A template
// that runs JavaScript makes its prompt off the UI goroutine, and sends
// once it has. A wrong call sends nothing, saying why over the composer.
func (m *model) expandDraft(id string, state *chatState, draft string, send func(string)) {
	call, ok := prompttmpl.ParseCall(draft)
	if !ok || !prompttmpl.ValidName(call.Name) {
		send(draft)
		return
	}
	fail := func(why string) { state.templates.failed.Set(templateFailure{draft, why}) }
	dir, err := config.TemplatesDir()
	if err != nil {
		send(draft)
		return
	}
	t, err := prompttmpl.Load(dir, call.Name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		send(draft)
		return
	case err != nil:
		fail(err.Error())
		return
	}
	s, _ := m.session(id)
	if !t.For(s.GetAgent(), s.GetProfile()) {
		fail(Tf("//%s is not for %s", t.Name, s.GetAgent()))
		return
	}
	builtins := m.templateBuiltins(id, state)
	done := func(out string, err error) {
		if err != nil {
			fail(templateError(err))
			return
		}
		send(out)
	}
	if !t.HasScripts() {
		done(t.Expand(context.Background(), call.Args, builtins))
		return
	}
	if state.templates.running {
		return // Enter again while its scripts run
	}
	state.templates.running = true
	post := m.post
	go func() {
		out, err := t.Expand(context.Background(), call.Args, builtins)
		post(func() {
			state.templates.running = false
			done(out, err)
		})
	}()
}

// templateBuiltins are the built-in variables' values for session id.
func (m *model) templateBuiltins(id string, state *chatState) map[string]string {
	s, _ := m.session(id)
	return map[string]string{
		"agent":   s.GetAgent(),
		"profile": s.GetProfile(),
		"model":   ggui.Peek(state.result).Chat.GetUsage().GetModel(),
		"project": s.GetProject(),
		"branch":  s.GetBranch(),
		"repo":    s.GetRemote(),
		"date":    time.Now().Format(time.DateOnly),
	}
}

// templateError says what is wrong with a call's arguments.
func templateError(err error) string {
	var ae *prompttmpl.ArgError
	if !errors.As(err, &ae) {
		return err.Error()
	}
	switch ae.Kind {
	case prompttmpl.Missing:
		return Tf("%s is required", ae.Arg)
	case prompttmpl.Extra:
		return Tf("%q is one value more than the template takes", ae.Value)
	case prompttmpl.Twice:
		return Tf("%s is given twice", ae.Arg)
	case prompttmpl.BadValue:
		return Tf("%s takes %s, not %q", ae.Arg, typeWant(ae.Type, ae.Options), ae.Value)
	case prompttmpl.Unquoted:
		return T("A quote is not closed")
	}
	return err.Error()
}

// typeWant says what an argument of a type takes.
func typeWant(t prompttmpl.Type, options []string) string {
	switch t {
	case prompttmpl.String:
		return T("one line")
	case prompttmpl.Integer:
		return T("a whole number")
	case prompttmpl.Number:
		return T("a number")
	case prompttmpl.Boolean:
		return T("true or false")
	case prompttmpl.Choice:
		return Tf("one of %s", strings.Join(options, ", "))
	}
	return string(t)
}

// templateHint is, for a draft that calls a template with arguments after
// its name, the arguments it takes and what is wrong with them; nil for
// any other draft. Reactive.
func templateHint(state *chatState) ggui.Widget {
	draft := state.draft.Get()
	call, ok := prompttmpl.ParseCall(draft)
	if !ok || !strings.ContainsAny(draft, " \t\n") {
		return nil
	}
	i := slices.IndexFunc(state.templates.templates.Get(), func(t *prompttmpl.Template) bool { return t.Name == call.Name })
	if i < 0 {
		return nil
	}
	t := ggui.Peek(state.templates.templates)[i]
	lines := []ggui.Widget{widget.Row(
		widget.Text("//"+t.Name).Size(13).Weight(ggui.WeightMedium).NoWrap(),
		widget.Expanded(widget.Text(t.Usage()).Size(12).Color(skin.ExtraMuted).NoWrap().Ellipsis()),
	).Gap(6)}
	why := ""
	if f := state.templates.failed.Get(); f.draft == draft {
		why = f.why
	} else if _, err := t.Bind(call.Args); err != nil && strings.TrimSpace(call.Args) != "" {
		why = templateError(err)
	}
	if why != "" {
		lines = append(lines, widget.Text(why).Size(12).Color(uitheme.Destructive))
	} else if t.Description != "" {
		lines = append(lines, muted(widget.Text(t.Description).Size(12)))
	}
	return widget.Box(widget.Column(lines...).Gap(3).Stretch()).Pad(6, 10)
}
