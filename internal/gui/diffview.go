package gui

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/sanbo/internal/protocol"
)

// A diff tab draws its file's hunks in one column, or side by side, its
// long lines cut or wrapped. Within a changed line paired with the line it
// replaced, the words that differ are marked. Lines are picked by clicking
// them, Shift extending the pick, and the picked lines go to an agent with
// a note; a hunk can be undone.

// diffMode is how a diff is drawn.
type diffMode struct {
	Split bool // old and new side by side
	Wrap  bool // long lines wrap rather than run on
}

// lineSel is the lines picked, as indexes into the diff's lines, From
// before To; none when To < From.
type lineSel struct{ From, To int }

func (s lineSel) empty() bool { return s.To < s.From }

func (s lineSel) has(i int) bool { return !s.empty() && i >= s.From && i <= s.To }

var noSel = lineSel{0, -1}

// untab is a line as the diff draws it.
func untab(s string) string { return strings.ReplaceAll(s, "\t", "    ") }

// wordSpan is the part of a line that differs from the line it pairs with,
// as byte offsets; none when Lo == Hi.
type wordSpan struct{ Lo, Hi int }

// changedWords pairs each removed line with the added line in its place,
// a run of removals followed by a run of additions taken in order, and
// marks where each differs from the other.
func changedWords(lines []*protocol.DiffLine) map[int]wordSpan {
	out := map[int]wordSpan{}
	for i := 0; i < len(lines); {
		if lines[i].GetOp() != "-" {
			i++
			continue
		}
		start := i
		for i < len(lines) && lines[i].GetOp() == "-" {
			i++
		}
		mid := i
		for i < len(lines) && lines[i].GetOp() == "+" {
			i++
		}
		for k := 0; start+k < mid && mid+k < i; k++ {
			a, b := untab(lines[start+k].GetText()), untab(lines[mid+k].GetText())
			sa, sb := differing(a, b)
			out[start+k], out[mid+k] = sa, sb
		}
	}
	return out
}

// differing is the middle of a and of b that the two do not share,
// widened to whole words, once their common start and end are taken off.
// Lines with little in common are left unmarked: the whole line differs.
func differing(a, b string) (wordSpan, wordSpan) {
	p := 0
	for p < len(a) && p < len(b) && a[p] == b[p] {
		p++
	}
	s := 0
	for s < len(a)-p && s < len(b)-p && a[len(a)-1-s] == b[len(b)-1-s] {
		s++
	}
	// A change that starts or ends inside a word marks the whole word.
	wordAt := func(t string, i int) bool { return i >= 0 && i < len(t) && isWord(t[i]) }
	for p > 0 && isWord(a[p-1]) && (wordAt(a, p) || wordAt(b, p)) {
		p--
	}
	for s > 0 && isWord(a[len(a)-s]) && (wordAt(a, len(a)-s-1) || wordAt(b, len(b)-s-1)) {
		s--
	}
	sa, sb := wordSpan{p, len(a) - s}, wordSpan{p, len(b) - s}
	if sa.Lo > sa.Hi || sb.Lo > sb.Hi {
		return wordSpan{}, wordSpan{}
	}
	// Mostly changed: marking it says nothing the line's tint does not.
	if 2*(p+s) < max(len(a), len(b)) {
		return wordSpan{}, wordSpan{}
	}
	return sa, sb
}

func isWord(c byte) bool {
	return c == '_' || c >= 0x80 || unicode.IsLetter(rune(c)) || unicode.IsDigit(rune(c))
}

// diffText is a line's text, the differing words in its side's colour.
func diffText(l *protocol.DiffLine, span wordSpan, mode diffMode, mono ggui.TextStyle, prefix string) ggui.Widget {
	text := untab(l.GetText())
	// Side by side, a long line is cut at its half; in one column it runs
	// on, and the diff scrolls sideways.
	cut := mode.Split && !mode.Wrap
	if span.Lo == span.Hi {
		t := ggui.Text(prefix + text).Style(mono)
		if !mode.Wrap {
			t = t.NoWrap()
		}
		if cut {
			t = t.Ellipsis()
		}
		return t
	}
	mark := tokDiffAdded
	if l.GetOp() == "-" {
		mark = tokDiffRemoved
	}
	t := ggui.RichText(
		ggui.Span(prefix+text[:span.Lo]),
		ggui.Span(text[span.Lo:span.Hi]).Color(mark).Weight(ggui.WeightSemibold),
		ggui.Span(text[span.Hi:]),
	).Style(mono)
	if !mode.Wrap {
		t = t.NoWrap()
	}
	if cut {
		t = t.Ellipsis()
	}
	return t
}

// diffRows draws a diff's lines, as many as the pane shows, in one column
// or side by side. pick is called with a line clicked and whether Shift
// was held; hunk with a hunk header's index, for its actions.
func diffRows(d *protocol.Diff, mode diffMode, sel lineSel, pick func(i int, extend bool), hunk func(i int) ggui.Widget) ggui.Widget {
	mono := ggui.TextStyle{Font: ggui.DefaultMonoFont(), Size: 11.5}
	lines := d.GetLines()[:min(len(d.GetLines()), maxShownDiffLines)]
	words := changedWords(lines)
	num := func(n int) ggui.Widget {
		s := ""
		if n > 0 {
			s = fmt.Sprint(n)
		}
		return ggui.Box(ggui.Text(s).Style(mono).Color(tokExtraMuted).NoWrap()).Width(34)
	}
	tint := func(op string, picked bool, w *ggui.BoxWidget) ggui.Widget {
		switch {
		case picked:
			return w.Fill(tokAccentBright.Alpha(0.22))
		case op == "+":
			return w.Fill(tokDiffAddedBg)
		case op == "-":
			return w.Fill(tokDiffRemovedBg)
		}
		return w
	}
	clickable := func(w ggui.Widget, i int) ggui.Widget {
		return ggui.Pointer(w).OnDown(func(ev ggui.PointerEvent) { pick(i, ev.Mods.Shift) })
	}
	var rows []ggui.Widget
	if !mode.Split {
		for i, l := range lines {
			if l.GetOp() == "@" {
				rows = append(rows, hunkRow(l, mono, hunk(i)))
				continue
			}
			row := ggui.Box(ggui.Row(num(int(l.GetOld())), num(int(l.GetNew())), ggui.Expanded(diffText(l, words[i], mode, mono, l.GetOp()+" "))).Gap(4)).Pad(1, 8)
			rows = append(rows, clickable(tint(l.GetOp(), sel.has(i), row), i))
		}
	} else {
		// A side of a split row: the line's number and text, or an empty
		// cell where the other side has a line and this one none.
		side := func(i int, newSide bool) ggui.Widget {
			if i < 0 {
				return ggui.Box(ggui.Row(num(0))).Pad(1, 8).Fill(tokSurface2)
			}
			l := lines[i]
			n := l.GetOld()
			if newSide {
				n = l.GetNew()
			}
			cell := ggui.Box(ggui.Row(num(int(n)), ggui.Expanded(diffText(l, words[i], mode, mono, ""))).Gap(4)).Pad(1, 8)
			return clickable(tint(l.GetOp(), sel.has(i), cell), i)
		}
		for i := 0; i < len(lines); {
			l := lines[i]
			switch l.GetOp() {
			case "@":
				rows = append(rows, hunkRow(l, mono, hunk(i)))
				i++
			case " ":
				rows = append(rows, ggui.Row(ggui.Expanded(side(i, false)), vline(), ggui.Expanded(side(i, true))).Stretch())
				i++
			default:
				var removed, added []int
				for i < len(lines) && lines[i].GetOp() == "-" {
					removed = append(removed, i)
					i++
				}
				for i < len(lines) && lines[i].GetOp() == "+" {
					added = append(added, i)
					i++
				}
				for k := range max(len(removed), len(added)) {
					left, right := -1, -1
					if k < len(removed) {
						left = removed[k]
					}
					if k < len(added) {
						right = added[k]
					}
					rows = append(rows, ggui.Row(ggui.Expanded(side(left, false)), vline(), ggui.Expanded(side(right, true))).Stretch())
				}
			}
		}
	}
	if len(lines) < len(d.GetLines()) || d.GetTruncated() {
		rows = append(rows, ggui.Box(muted(ggui.Text(T("The rest of the diff is not shown.")).Size(11.5))).Pad(8, 12))
	}
	col := ggui.Column(rows...).Stretch()
	if mode.Wrap || mode.Split {
		return ggui.Scroll(col).Bar(tokSurface3)
	}
	// Long lines run on, and the whole diff scrolls sideways to them.
	return ggui.Scroll(ggui.Scroll(col).Horizontal().Bar(tokSurface3)).Bar(tokSurface3)
}

// hunkRow is a hunk's header line, with its actions on hover.
func hunkRow(l *protocol.DiffLine, mono ggui.TextStyle, actions ggui.Widget) ggui.Widget {
	hover := ggui.State(false)
	row := ggui.Box(ggui.Row(
		ggui.Expanded(ggui.Text(l.GetText()).Style(mono).Color(tokMuted).NoWrap().Ellipsis()),
		ggui.Box(ggui.If(hover, func() ggui.Widget { return actions })).Height(22),
	).Gap(8).Align(ggui.AlignCenter)).Pad(2, 8, 2, 10).Fill(tokSurface2)
	return ggui.Pointer(row).OnHover(hover.Set)
}

// hunkAt is the hunk line i is in: its header's index and the lines after
// it, up to the next.
func hunkAt(lines []*protocol.DiffLine, i int) (int, []*protocol.DiffLine) {
	start := i
	for start > 0 && lines[start].GetOp() != "@" {
		start--
	}
	end := start + 1
	for end < len(lines) && lines[end].GetOp() != "@" {
		end++
	}
	return start, lines[start+1 : end]
}

// lineRef names lines of a file as an agent reads them: path:from-to, by
// their new numbers, or old ones for lines only the old side has.
func lineRef(path string, lines []*protocol.DiffLine) string {
	first, last := 0, 0
	for _, l := range lines {
		n := l.GetNew()
		if n == 0 {
			n = l.GetOld()
		}
		if n == 0 {
			continue
		}
		if first == 0 {
			first = int(n)
		}
		last = int(n)
	}
	switch {
	case first == 0:
		return path
	case first == last:
		return fmt.Sprintf("%s:%d", path, first)
	}
	return fmt.Sprintf("%s:%d-%d", path, first, last)
}

// agentNote is what picked lines send an agent: where they are, the lines
// as a diff, and the user's note on them.
func agentNote(path string, lines []*protocol.DiffLine, note string) string {
	var b strings.Builder
	b.WriteString(lineRef(path, lines))
	b.WriteString("\n```diff\n")
	for _, l := range lines {
		if l.GetOp() == "@" {
			continue
		}
		b.WriteString(l.GetOp())
		b.WriteString(l.GetText())
		b.WriteByte('\n')
	}
	b.WriteString("```")
	if note = strings.TrimSpace(note); note != "" {
		b.WriteString("\n")
		b.WriteString(note)
	}
	return b.String()
}

// agentsFor are the sessions in a project an agent's chat can take a note
// in, the one shown last first.
func (m *model) agentsFor(root fileRoot) []*protocol.SessionInfo {
	var out []*protocol.SessionInfo
	for _, s := range ggui.Peek(m.Sessions) {
		if s.GetNodeId() != root.Node || !(within(s.GetProject(), root.Path) || within(root.Path, s.GetProject())) || !m.chatCapable(s.GetId()) {
			continue
		}
		if s.GetId() == m.lastSession {
			out = append([]*protocol.SessionInfo{s}, out...)
		} else {
			out = append(out, s)
		}
	}
	return out
}

// askAgent puts a note in a session's composer and shows its chat, for the
// user to finish and send.
func (m *model) askAgent(id, text string) {
	draft := strings.TrimRight(m.composerDraft(id), "\n")
	if draft != "" {
		draft += "\n\n"
	}
	m.setComposerDraft(id, draft+text)
	m.setChatMode(id, true)
	m.showTab(id)
}

// selectionBar is the bar under a diff while lines are picked: how many,
// a note, and the agents to send them to.
func selectionBar(m *model, root fileRoot, path string, lines []*protocol.DiffLine, sel *ggui.StateValue[lineSel]) ggui.Widget {
	note := ggui.State("")
	agents := m.agentsFor(root)
	send := func(id string) func() {
		return func() {
			s := ggui.Peek(sel)
			m.askAgent(id, agentNote(path, lines[s.From:s.To+1], ggui.Peek(note)))
			sel.Set(noSel)
		}
	}
	var target ggui.Widget
	switch len(agents) {
	case 0:
		target = ui.Button(T("Ask agent"), func() {}).Disabled(true).Name(T("No agent runs in this project"))
	case 1:
		target = ui.Button(Tf("Ask %s", sessionTitle(agents[0], ggui.Peek(m.Titles)[agents[0].GetId()])), send(agents[0].GetId()))
	default:
		items := make([]ggui.Widget, len(agents))
		for i, s := range agents {
			items[i] = ui.MenuItem(sessionTitle(s, ggui.Peek(m.Titles)[s.GetId()]), send(s.GetId()))
		}
		target = ui.MenuOf(ggui.Text(T("Ask agent")), items...).Width(240).Name(T("Ask agent"))
	}
	count := ggui.Map(sel, func(s lineSel) string {
		return lineRef(baseName(path), lines[s.From:s.To+1])
	})
	return ggui.Column(hline(), ggui.Box(ggui.Row(
		icon("send-horizontal").Size(14).Color(tokMuted),
		ggui.TextOf(count).Size(12.5).Font(ggui.DefaultMonoFont()).NoWrap(),
		ggui.Expanded(ui.TextField(note).Placeholder(T("Note for the agent (optional)")).Name(T("Note for the agent")).
			OnSubmit(func(string) {
				if len(agents) > 0 {
					send(agents[0].GetId())()
				}
			})),
		target,
		iconButton("x", T("Clear selection"), func() { sel.Set(noSel) }),
	).Gap(10).Align(ggui.AlignCenter)).Pad(8, 12).Fill(tokSurface1)).Stretch()
}
