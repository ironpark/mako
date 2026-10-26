package gui

import (
	"image/color"
	"strings"
	"unicode"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
)

// A picker is a search over a list, as Zed's command palette and file
// finder are: a field on top, the rows that match under it, best first,
// with the letters typed marked, and a footer naming what Enter does. The
// arrows move through the rows and Enter runs the one picked; its source
// ranks the rows for what is typed.

// pickRow is a row a picker lists.
type pickRow struct {
	Label      string
	Hits       []int  // the runes of Label the query matched
	Detail     string // muted, after the label
	DetailHits []int
	Icon       ggui.Widget // before the label, if any
	Shortcut   string      // a chord, as "cmd+shift+p"
	Recent     bool        // marked as recently run
	Disabled   bool
	Run        func()
}

// pickRowHeight is a row's height, the same for every row so the list
// can bring the one picked into view.
const pickRowHeight = 34

// pickerTop is how far below the window's top a picker sits, as Zed's
// do, so it stays put as its rows come and go.
const pickerTop = 72

// picker is a picker over the window, opened and closed through open.
// Its rows are its source's for the query, asked again when the query
// changes or refresh bumps gen.
type picker struct {
	open   *ggui.StateValue[bool]
	query  *ggui.StateValue[string]
	gen    *ggui.StateValue[int]
	rows   ggui.Readable[[]pickRow]
	sel    *ggui.StateValue[int]
	offset *ggui.StateValue[float64]

	Placeholder, Empty, Action, Name string
	Width, Height                    float64
}

func newPicker(open *ggui.StateValue[bool], query *ggui.StateValue[string], source func(string) []pickRow) *picker {
	p := &picker{open: open, query: query, gen: ggui.State(0), sel: ggui.State(0), offset: ggui.State(0.0), Width: 620, Height: 400}
	// Rows hold functions, which do not compare: each asking is new.
	p.rows = ggui.Derived(func() []pickRow {
		p.gen.Get()
		return source(p.query.Get())
	}).WithEqual(func(a, b []pickRow) bool { return false })
	return p
}

// refresh asks the source for the rows again.
func (p *picker) refresh() { p.gen.Set(ggui.Peek(p.gen) + 1) }

// step is the enabled row d rows on from i, wrapping; -1 for none.
func (p *picker) step(rows []pickRow, i, d int) int {
	for range rows {
		i = (i + d + len(rows)) % len(rows)
		if !rows[i].Disabled {
			return i
		}
	}
	return -1
}

// move picks the row d rows on and brings it into view.
func (p *picker) move(d int) {
	rows := ggui.Peek(p.rows)
	i := p.step(rows, ggui.Peek(p.sel), d)
	if i < 0 {
		return
	}
	p.sel.Set(i)
	top, bottom := float64(i)*pickRowHeight, float64(i+1)*pickRowHeight
	view := min(p.Height, float64(len(rows))*pickRowHeight)
	switch off := ggui.Peek(p.offset); {
	case top < off:
		p.offset.Set(top)
	case bottom > off+view:
		p.offset.Set(bottom - view)
	}
}

// run closes the picker and runs row i.
func (p *picker) run(i int) {
	rows := ggui.Peek(p.rows)
	if i < 0 || i >= len(rows) || rows[i].Disabled {
		return
	}
	p.open.Set(false)
	if rows[i].Run != nil {
		rows[i].Run()
	}
}

func (p *picker) key(ev ggui.KeyEvent) bool {
	if ev.Mods != (ggui.Mods{}) {
		return false
	}
	switch ev.Key {
	case ggui.KeyArrowDown:
		p.move(1)
	case ggui.KeyArrowUp:
		p.move(-1)
	case ggui.KeyPageDown:
		p.move(8)
	case ggui.KeyPageUp:
		p.move(-8)
	case ggui.KeyEnter, ggui.KeyNumpadEnter:
		p.run(ggui.Peek(p.sel))
	default:
		return false
	}
	return true
}

// dialog is the picker over the window.
func (p *picker) dialog() ggui.Widget {
	// New rows pick their first and show from the top.
	ggui.Effect(func() ggui.Cleanup {
		rows := p.rows.Get()
		ggui.Untrack(func() struct{} {
			p.sel.Set(p.step(rows, -1, 1))
			p.offset.Set(0)
			return struct{}{}
		})
		return nil
	})
	input := ggui.TextInput(p.query).Placeholder(p.Placeholder).Name(p.Name).
		Style(ggui.TextStyle{Size: 15}).OnKey(p.key)
	// The rows are built when they change; moving the pick only fills
	// another.
	list := ggui.View(p.rows, func(rows []pickRow) ggui.Widget {
		if len(rows) == 0 {
			return ggui.Box(muted(ggui.Text(p.Empty).Size(13))).Pad(14, 16)
		}
		out := make([]ggui.Widget, len(rows))
		for i, r := range rows {
			out[i] = p.row(i, r)
		}
		return ggui.Column(out...).Gap(0).Stretch()
	})
	scroll := ggui.Scroll(ggui.Padding(list, 6)).BindOffset(p.offset).Bar(tokSurface3)
	footer := ggui.Row(ggui.Spacer(),
		ggui.Text(p.Action).Size(12.5).Color(tokFg).NoWrap(),
		ggui.Text("↵").Size(12.5).Color(tokMuted).NoWrap(),
	).Gap(6).Align(ggui.AlignCenter)
	body := ggui.Column(
		ggui.Box(input).Pad(14, 16),
		hline(),
		ggui.Box(scroll).BindHeight(ggui.Map(p.rows, func(rows []pickRow) float64 {
			return min(p.Height, float64(max(len(rows), 1))*pickRowHeight) + 12
		})),
		hline(),
		ggui.Box(footer).Pad(8, 16),
	).Gap(0).Stretch()
	return ui.Dialog(p.open, body).Compact().Top(pickerTop).Name(p.Name).Width(p.Width)
}

// row is row i, filled while it is the one picked.
func (p *picker) row(i int, r pickRow) ggui.Widget {
	var fg color.Color = tokFg
	if r.Disabled {
		fg = tokExtraMuted
	}
	line := []ggui.Widget{}
	if r.Icon != nil {
		line = append(line, r.Icon)
	}
	text := []ggui.Widget{hitText(r.Label, r.Hits, fg)}
	if r.Detail != "" {
		text = append(text, ggui.Expanded(hitText(r.Detail, r.DetailHits, tokMuted)))
	} else {
		text = append(text, ggui.Spacer())
	}
	line = append(line, ggui.Expanded(ggui.Row(text...).Gap(10).Align(ggui.AlignCenter)))
	if r.Shortcut != "" {
		line = append(line, keyCaps(chordLabel(r.Shortcut)))
	}
	if r.Recent {
		line = append(line, icon("history").Size(14).Color(tokExtraMuted))
	}
	content := ggui.Box(ggui.Row(line...).Gap(10).Align(ggui.AlignCenter)).Height(pickRowHeight).Pad(0, 10)
	btn := ui.ButtonOf(content, func() { p.run(i) }).Ghost().Pad(0).Radius(6).Disabled(r.Disabled).Name(r.Label)
	picked := ggui.Map(p.sel, func(s int) bool { return s == i })
	return ggui.View(picked, func(on bool) ggui.Widget {
		return ggui.Box(btn).Fill(pick[color.Color](on, tokSurface3, color.Transparent)).Radius(6)
	})
}

// hitText is s with the runes at hits marked as matched.
func hitText(s string, hits []int, fg color.Color) ggui.Widget {
	style := func(t ggui.TextSpan, hit bool) ggui.TextSpan {
		t = t.Size(13.5)
		if hit {
			return t.Color(tokAccentBright).Weight(ggui.WeightSemibold)
		}
		return t.Color(fg)
	}
	// Runs of runes alike, marked or not, are a span each; hits are in
	// order, so one cursor walks them.
	var spans []ggui.TextSpan
	var run strings.Builder
	runHit, next := false, 0
	for i, r := range []rune(s) {
		hit := next < len(hits) && hits[next] == i
		if hit {
			next++
		}
		if hit != runHit && run.Len() > 0 {
			spans = append(spans, style(ggui.Span(run.String()), runHit))
			run.Reset()
		}
		runHit = hit
		run.WriteRune(r)
	}
	spans = append(spans, style(ggui.Span(run.String()), runHit))
	return ggui.RichText(spans...).NoWrap().Ellipsis()
}

// chordLabel is a chord such as "cmd+shift+p" as the platform writes it,
// or the text as it is when it is not one.
func chordLabel(chord string) string {
	if ch, err := ggui.ParseChord(chord); err == nil {
		return ch.Label()
	}
	return chord
}

// keyCaps is a chord's label as Zed shows one: its keys spaced, muted.
func keyCaps(label string) ggui.Widget {
	var keys []ggui.Widget
	for _, r := range label {
		keys = append(keys, ggui.Text(string(r)).Size(12.5).Color(tokMuted).NoWrap())
	}
	return ggui.Row(keys...).Gap(3).Align(ggui.AlignCenter)
}

// fuzzyMatch reports whether s holds q's runes in order, ignoring case,
// with a score that is higher for runes together and at the starts of
// words, and the runes of s matched. Each rune of q takes the first of s
// it can, after the last.
func fuzzyMatch(s, q string) (score int, hits []int, ok bool) {
	q = strings.ToLower(strings.ReplaceAll(q, " ", ""))
	if q == "" {
		return 0, nil, true
	}
	runes := []rune(s)
	want := []rune(q)
	prev := -2
	j := 0
	for i, r := range runes {
		if j == len(want) {
			break
		}
		if unicode.ToLower(r) != want[j] {
			continue
		}
		switch {
		case i == prev+1:
			score += 6 // together
		case i == 0 || wordStart(runes, i):
			score += 4
		}
		hits = append(hits, i)
		prev = i
		j++
	}
	if j < len(want) {
		return 0, nil, false
	}
	return score, hits, true
}

// wordStart reports whether runes[i] starts a word: after a separator, or
// an upper-case letter after one that is not.
func wordStart(runes []rune, i int) bool {
	before := runes[i-1]
	return strings.ContainsRune("/_-.: ", before) || unicode.IsUpper(runes[i]) && !unicode.IsUpper(before)
}

// splitHits splits the hits of a string at rune n: those before it, and
// those after, counted from n.
func splitHits(hits []int, n int) (before, after []int) {
	for _, h := range hits {
		if h < n {
			before = append(before, h)
		} else {
			after = append(after, h-n)
		}
	}
	return before, after
}
