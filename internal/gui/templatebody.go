package gui

import (
	"image/color"
	"slices"
	"strings"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	uitheme "github.com/ironpark/ggui/ui/theme"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/prompttmpl"
)

// A template's prompt is edited with its {{name}}s coloured, an
// argument's, a built-in variable's and a name that is neither apart, and
// while the caret is in a {{ not yet closed, a list under it of the
// arguments and built-in variables named as typed, to complete one: Up
// and Down choose, Enter or Tab take it, Escape puts the list away.

// builtinLabels say what each built-in variable is, in English for T.
var builtinLabels = map[string]string{
	"agent":   "The agent's name",
	"profile": "The agent's profile",
	"model":   "The model the agent uses",
	"project": "The session's folder",
	"branch":  "The branch checked out there",
	"repo":    "The repository's name",
	"date":    "Today's date",
}

// placeholderColor is how a {{name}} is coloured: an argument's in the
// accent, a built-in variable's in blue, and one of neither in red, as
// saving it would fail.
func placeholderColor(name string, args []argDraft) color.Color {
	switch {
	case slices.ContainsFunc(args, func(a argDraft) bool { return a.Name == name }):
		return skin.AccentBright
	case prompttmpl.IsBuiltin(name):
		return skin.DotDone
	}
	return uitheme.Destructive
}

// placeholderColors colours the {{name}}s of text, of a template whose
// arguments are args.
func placeholderColors(text string, args []argDraft) []widget.TextColor {
	found := prompttmpl.Placeholders(text)
	out := make([]widget.TextColor, len(found))
	for i, p := range found {
		out[i] = widget.TextColor{Start: p.Start, End: p.End, Color: placeholderColor(p.Name, args)}
	}
	return out
}

// placeholderOption is a name the list offers, coloured as the editor
// colours it, and what it is: an argument's type, or what a built-in
// variable stands for.
type placeholderOption struct {
	Name, About string
	Color       color.Color
}

// placeholderOptions are the arguments, then the built-in variables, named
// as typed, as the composer's menu matches names.
func placeholderOptions(args []argDraft, typed string) []placeholderOption {
	var all []placeholderOption
	for _, a := range args {
		if a.Name != "" {
			all = append(all, placeholderOption{Name: a.Name, About: T(templateTypeLabels[string(a.Type)]), Color: skin.AccentBright})
		}
	}
	for _, b := range prompttmpl.Builtins {
		if !slices.ContainsFunc(all, func(o placeholderOption) bool { return o.Name == b }) {
			all = append(all, placeholderOption{Name: b, About: T(builtinLabels[b]), Color: skin.DotDone})
		}
	}
	return matchByName(all, strings.ToLower(typed), func(o placeholderOption) string { return o.Name })
}

// placeholderQuery is the open placeholder the caret is in: where its
// name starts, where the caret is, and what is typed.
type placeholderQuery struct {
	Open         bool
	Start, Caret int
	Typed        string
}

// queryAt is the placeholder the caret is in, as st has the editor.
func queryAt(st widget.TextInputState) placeholderQuery {
	if !st.Focused || st.Composing || st.Anchor != st.Caret {
		return placeholderQuery{}
	}
	start, typed, ok := prompttmpl.OpenPlaceholder(st.Text[:st.Caret])
	if !ok {
		return placeholderQuery{}
	}
	return placeholderQuery{Open: true, Start: start, Caret: st.Caret, Typed: typed}
}

// templateBody is the prompt's editor of draft id: its field, and under
// it the list of names to complete the placeholder the caret is in with.
func templateBody(pg *templatePage, id int, mono ggui.TextStyle) ggui.Widget {
	query := ggui.State(placeholderQuery{})
	pick := ggui.State(0)
	dismissed := ggui.State(false) // Escape put the list away, until the caret is in another
	options := ggui.Derived(func() []placeholderOption {
		q := query.Get()
		if !q.Open || dismissed.Get() {
			return nil
		}
		return placeholderOptions(pg.draft(id).Args, q.Typed)
	})
	field := ui.TextField(pg.field(id, func(d *templateDraft) *string { return &d.Body })).
		Multiline().Lines(6).Style(mono).Placeholder("Review {{file}} as {{agent}}.").Name(T("Template prompt")).
		Colors(memoColors(func() []argDraft { return ggui.Untrack(func() []argDraft { return pg.draft(id).Args }) }))
	input := field.Input()
	take := func(o placeholderOption) {
		q := ggui.Peek(query)
		st := input.EditingState()
		// What was typed, and the }} after it, are put in as one edit,
		// which undo takes back whole.
		input.Select(q.Start, q.Caret+prompttmpl.Closing(st.Text[q.Caret:]))
		input.Replace(o.Name + "}}")
		query.Set(placeholderQuery{})
	}
	field = field.OnKey(func(ev ggui.KeyEvent) bool {
		opts := ggui.Peek(options)
		if len(opts) == 0 || !plainKey(ev) {
			return false
		}
		switch ev.Key {
		case ggui.KeyEnter, ggui.KeyNumpadEnter, ggui.KeyTab:
			take(opts[min(ggui.Peek(pick), len(opts)-1)])
		case ggui.KeyEscape:
			dismissed.Set(true)
		default:
			return stepPick(pick, len(opts), ev.Key)
		}
		return true
	}).ClaimKeys(func(ev ggui.KeyEvent) bool {
		return (ev.Key == ggui.KeyTab || ev.Key == ggui.KeyEscape) && len(ggui.Peek(options)) > 0
	})
	// What the caret is in is read after each paint: a click or an arrow
	// moves it with no edit to follow.
	follow := &afterPaint{fn: func(*ggui.Canvas) {
		q, last := queryAt(input.EditingState()), ggui.Peek(query)
		if q == last {
			return
		}
		if q.Start != last.Start || !q.Open {
			dismissed.Set(false)
		}
		pick.Set(0)
		query.Set(q)
	}}
	showing := ggui.Map(options, func(o []placeholderOption) bool { return len(o) > 0 })
	list := ggui.If(showing, func() ggui.Widget {
		return ggui.Reactive(func() ggui.Widget {
			opts := options.Get()
			at := min(pick.Get(), len(opts)-1)
			rows := make([]ggui.Widget, len(opts))
			for i, o := range opts {
				rows[i] = placeholderRow(o, i == at, func() { take(o) })
			}
			return menuPanel(ggui.Column(rows...).Gap(2).Stretch())
		})
	})
	return ggui.Stack(ggui.Column(field, list).Gap(4).Stretch(), follow)
}

// memoColors colours a prompt's {{name}}s, of a template whose arguments
// args gives, answering again what it last did while the text and the
// arguments' names are the same: a field asks on each paint, the caret's
// blink's too.
func memoColors(args func() []argDraft) func(string) []widget.TextColor {
	var text string
	var names []string
	var last []widget.TextColor
	asked := false
	return func(s string) []widget.TextColor {
		now := args()
		same := slices.EqualFunc(names, now, func(n string, a argDraft) bool { return n == a.Name })
		if !asked || s != text || !same {
			names = names[:0]
			for _, a := range now {
				names = append(names, a.Name)
			}
			text, last, asked = s, placeholderColors(s, now), true
		}
		return last
	}
}

// placeholderRow is a name the list offers: {{name}}, coloured as the
// editor colours it, and what it is.
func placeholderRow(o placeholderOption, chosen bool, take func()) ggui.Widget {
	return menuRowBox(ggui.Row(
		ggui.Text("{{"+o.Name+"}}").Font(ggui.DefaultMonoFont()).Size(12.5).Color(o.Color).NoWrap(),
		ggui.Expanded(muted(ggui.Text(o.About).Size(12).NoWrap().Ellipsis())),
	).Gap(10).Align(ggui.AlignCenter), chosen, take)
}
