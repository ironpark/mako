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
// arguments are args, and its blocks' tags: in amber, or red for a match
// of a name that is neither, which no JavaScript is.
func placeholderColors(text string, args []argDraft) []widget.TextColor {
	var out []widget.TextColor
	for _, p := range prompttmpl.Placeholders(text) {
		out = append(out, widget.TextColor{Start: p.Start, End: p.End, Color: placeholderColor(p.Name, args)})
	}
	for _, t := range prompttmpl.Tags(text) {
		c := color.Color(skin.DotWaiting)
		if t.Kind == prompttmpl.TagMatch && prompttmpl.IsName(t.Arg) && placeholderColor(t.Arg, args) == uitheme.Destructive {
			c = uitheme.Destructive
		}
		out = append(out, widget.TextColor{Start: t.Start, End: t.End, Color: c})
	}
	// A script's fences, in amber too; its code as it is.
	for _, s := range prompttmpl.Scripts(text) {
		open := s.Start + strings.IndexByte(text[s.Start:], '\n')
		closeEnd := s.End
		if strings.HasSuffix(text[s.Start:s.End], "\n") {
			closeEnd--
		}
		closeStart := strings.LastIndexByte(text[:closeEnd], '\n') + 1
		out = append(out,
			widget.TextColor{Start: s.Start, End: open, Color: skin.DotWaiting},
			widget.TextColor{Start: closeStart, End: closeEnd, Color: skin.DotWaiting})
	}
	slices.SortFunc(out, func(a, b widget.TextColor) int { return a.Start - b.Start })
	return out
}

// placeholderOption is what the list offers, coloured as the editor
// colours it, and what it is: an argument's type, what a built-in
// variable stands for, or what a tag or an arm's value does.
type placeholderOption struct {
	Name, About, Shown string
	Color              color.Color
}

// placeholderOptions are what the list offers for q, of a template whose
// arguments are args, as typed, as the composer's menu matches names: for
// a {{name}} or a block's subject, the arguments then the built-in
// variables; for a tag begun, the tags that open and close a block; and
// for an arm, the values the block's subject takes, then else.
func placeholderOptions(args []argDraft, q placeholderQuery) []placeholderOption {
	var all []placeholderOption
	switch q.Kind {
	case prompttmpl.CompleteTag:
		all = []placeholderOption{
			{Name: "#match", About: T("Pick text by a value, arm by arm"), Color: skin.DotWaiting},
			{Name: "/match", About: T("Close a match block"), Color: skin.DotWaiting},
		}
	case prompttmpl.CompleteArm:
		for _, v := range armValues(args, q.Block) {
			all = append(all, placeholderOption{Name: v, About: T("An arm for this value"), Color: skin.DotWaiting})
		}
		all = append(all, placeholderOption{Name: "else", About: T("An arm for the rest"), Color: skin.DotWaiting})
	default:
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
	}
	out := matchByName(all, strings.ToLower(q.Typed), func(o placeholderOption) string { return o.Name })
	for i := range out {
		out[i].Shown = shownAs(q.Kind, out[i].Name)
	}
	return out
}

// shownAs is name written as what completes a query of kind.
func shownAs(kind, name string) string {
	switch kind {
	case prompttmpl.CompleteName:
		return "{{" + name + "}}"
	case prompttmpl.CompleteArm:
		return "{:" + name + "}"
	case prompttmpl.CompleteTag:
		return "{" + name + "}"
	}
	return name
}

// armValues are the values a block's subject takes, as its arms name
// them: a choice argument's options, a boolean's true and false; none for
// another, or a built-in variable or an expression.
func armValues(args []argDraft, subject string) []string {
	i := slices.IndexFunc(args, func(a argDraft) bool { return a.Name == subject })
	if i < 0 {
		return nil
	}
	switch args[i].Type {
	case prompttmpl.Choice:
		return splitList(args[i].Options)
	case prompttmpl.Boolean:
		return []string{"true", "false"}
	}
	return nil
}

// placeholderQuery is what the caret is in, not yet closed, and where the
// caret is.
type placeholderQuery struct {
	Open bool
	prompttmpl.Completion
	Caret int
}

// queryAt is what the caret is in, as st has the editor.
func queryAt(st widget.TextInputState) placeholderQuery {
	if !st.Focused || st.Composing || st.Anchor != st.Caret {
		return placeholderQuery{}
	}
	c, ok := prompttmpl.Complete(st.Text[:st.Caret])
	if !ok {
		return placeholderQuery{}
	}
	return placeholderQuery{Open: true, Completion: c, Caret: st.Caret}
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
		return placeholderOptions(pg.draft(id).Args, q)
	})
	field := ui.TextField(pg.field(id, func(d *templateDraft) *string { return &d.Body })).
		Multiline().Lines(6).Style(mono).Placeholder("Review {{file}} as {{agent}}.").Name(T("Template prompt")).
		Colors(memoColors(func() []argDraft { return ggui.Untrack(func() []argDraft { return pg.draft(id).Args }) }))
	input := field.Input()
	take := func(o placeholderOption) {
		q := ggui.Peek(query)
		after := input.EditingState().Text[q.Caret:]
		// What was typed, and the brace after it, are put in as one edit,
		// which undo takes back whole.
		switch q.Kind {
		case prompttmpl.CompleteName:
			input.Select(q.Start, q.Caret+prompttmpl.Closing(after))
			input.Replace(o.Name + "}}")
		case prompttmpl.CompleteTag:
			input.Select(q.Brace, q.Caret+prompttmpl.ClosingOne(after))
			if o.Name == "#match" {
				// The name it matches is picked next, from the list again.
				input.Replace("{#match ")
				return
			}
			input.Replace("{" + o.Name + "}")
		default: // a subject, an arm
			input.Select(q.Start, q.Caret+prompttmpl.ClosingOne(after))
			input.Replace(o.Name + "}")
		}
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
			return menuPanel(widget.Column(rows...).Gap(2).Stretch())
		})
	})
	return widget.Stack(widget.Column(field, list).Gap(4).Stretch(), follow)
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

// placeholderRow is what the list offers, as it is written in, coloured
// as the editor colours it, and what it is.
func placeholderRow(o placeholderOption, chosen bool, take func()) ggui.Widget {
	return menuRowBox(widget.Row(
		widget.Text(o.Shown).Font(ggui.DefaultMonoFont()).Size(12.5).Color(o.Color).NoWrap(),
		widget.Expanded(muted(widget.Text(o.About).Size(12).NoWrap().Ellipsis())),
	).Gap(10).Align(widget.AlignCenter), chosen, take)
}
