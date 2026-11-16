package gui

import (
	"strings"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/widget"
)

// codeText is src as selectable lines of code, highlighted in l when has
// is true, its tabs stopping as in the editor.
func codeText(m *model, src string, l language, has bool, size float64) *widget.RichTextWidget {
	tab := defaultTab
	var hl *docSyntax
	ready := ggui.State(0) // counts the highlights the worker has found
	if has {
		hl = m.syntaxFor(l)
		hl.wake = func() { m.post(func() { ready.Set(ggui.Peek(ready) + 1) }) }
		tab = pick(l.TabSize > 0, l.TabSize, tab)
	}
	src = strings.TrimSuffix(expandTabs(src, tab), "\n")
	spans := ggui.Derived(func() []widget.TextSpan {
		ready.Get()
		if hl == nil {
			return []widget.TextSpan{widget.Span(src)}
		}
		return colorSpans(src, hl.colors(src, m.themePalette()))
	})
	return widget.RichText().BindSpans(spans).NoWrap().Selectable().
		Font(ggui.DefaultMonoFont()).Size(size).LineHeight(max(ggui.Peek(m.Prefs).LineHeight, 1.2))
}

// colorSpans is src as spans coloured by styles, in order, and the text
// between them in the paragraph's colour.
func colorSpans(src string, styles []styleSpan) []widget.TextSpan {
	var out []widget.TextSpan
	at := 0
	for _, s := range styles {
		start, end := max(s.Start, at), min(s.End, len(src))
		if start >= end {
			continue
		}
		if start > at {
			out = append(out, widget.Span(src[at:start]))
		}
		out = append(out, widget.Span(src[start:end]).Color(s.Color))
		at = end
	}
	if at < len(src) {
		out = append(out, widget.Span(src[at:]))
	}
	return out
}

// expandTabs puts spaces in place of tabs, stopping every tab columns.
func expandTabs(s string, tab int) string {
	if !strings.Contains(s, "\t") {
		return s
	}
	var b strings.Builder
	col := 0
	for _, r := range s {
		switch r {
		case '\t':
			n := tab - col%tab
			b.WriteString(strings.Repeat(" ", n))
			col += n
		case '\n':
			b.WriteRune(r)
			col = 0
		default:
			b.WriteRune(r)
			col += runeCells(r)
		}
	}
	return b.String()
}
