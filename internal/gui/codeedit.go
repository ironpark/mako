package gui

import (
	"image/color"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ironpark/ggui"
	uitheme "github.com/ironpark/ggui/ui/theme"
	"golang.org/x/text/width"
)

// codeEditor edits a file's text in a grid of monospaced cells: lines that
// do not wrap, numbered in a gutter, coloured by the spans its colors
// function gives, scrolling both ways.
//
// The text lives in a hidden TextInput, which does the editing: typing and
// the platform IME, deletes, word motion, the clipboard and undo. The
// editor draws it, places the caret from the pointer, and adds what a code
// editor does beyond a text field: Up and Down by column across tabs and
// wide characters, Page Up and Down, Enter keeping the line's indent, and
// Tab indenting the lines of a selection.
type codeEditor struct {
	input     *ggui.TextInputWidget
	readOnly  bool
	colors    func(text string, pal palette) []styleSpan // may be nil
	onPress   func()
	autoFocus bool
	tabSize   int
	comment   string // the line comment ⌘/ toggles, as "// "
	prefs     func() prefs
	// Caret is where the caret is, for a status bar: set after each key,
	// click and IME step that may have moved it.
	Caret *ggui.StateValue[caretPos]

	focused          bool
	scrollX, scrollY float64
	// lastText and lastCaret are what the view was last scrolled to show,
	// so it follows the caret only when an edit or a motion moved it.
	lastText  string
	lastCaret int
	goalCol   int // the column Up and Down keep to, or -1
	clicks    int
	lastClick time.Time
	lastPos   ggui.Point
	dragFrom  int       // the selection's anchor while dragging, or -1
	blinkFrom time.Time // the caret stays lit from here, after a key or a click

	lines     []int // where each line of linesText starts
	linesText string
	widest    int // the longest line's length in bytes, which bounds its cells

	// From the last paint, in logical pixels.
	pal          palette
	rect         ggui.Rect
	textX        float64 // where column 0 is, before scrolling
	cellW, lineH float64
	fontSize     float64
}

// caretPos is the caret's line and column, from 1 and counted in
// characters, how many characters are selected, and how many lines the
// text has.
type caretPos struct{ Line, Col, Selected, Lines int }

// styleSpan colours the bytes [Start, End) of a text.
type styleSpan struct {
	Start, End int
	Color      color.Color
}

const (
	editorPad  = 8  // above the first line and left of the text
	gutterGap  = 16 // between the line numbers and the text
	defaultTab = 4
	wheelLines = 3
)

func newCodeEditor(text *ggui.StateValue[string], prefs func() prefs) *codeEditor {
	e := &codeEditor{
		input: ggui.TextInput(text).NoWrap(),
		prefs: prefs, tabSize: defaultTab, goalCol: -1, dragFrom: -1, pal: darkPalette,
		Caret: ggui.State(caretPos{Line: 1, Col: 1, Lines: 1}),
	}
	// A file opens at its start, where a text field would put the caret
	// at the end.
	e.input.Select(0, 0)
	return e
}

// lineHeight is how far apart the editor's lines are.
func lineHeight(p prefs) float64 { return math.Ceil(p.FontSize * max(p.LineHeight, 1.2)) }

// publish sets Caret from the editing state.
func (e *codeEditor) publish() {
	st := e.input.EditingState()
	starts := e.lineStarts(st.Text)
	caret := min(st.Caret, len(st.Text))
	li := lineOf(starts, caret)
	lo, hi := min(st.Anchor, caret), max(st.Anchor, caret)
	e.Caret.Set(caretPos{
		Line:     li + 1,
		Col:      utf8.RuneCountInString(st.Text[starts[li]:caret]) + 1,
		Selected: utf8.RuneCountInString(st.Text[min(lo, len(st.Text)):min(hi, len(st.Text))]),
		Lines:    len(starts),
	})
}

// ReadOnly shows the text without taking edits; it still selects and
// copies.
func (e *codeEditor) ReadOnly(v bool) *codeEditor { e.readOnly = v; return e }

// Colors sets what colours the text.
func (e *codeEditor) Colors(fn func(text string, pal palette) []styleSpan) *codeEditor {
	e.colors = fn
	return e
}

// Comment sets the line comment ⌘/ puts before lines and takes away.
func (e *codeEditor) Comment(prefix string) *codeEditor { e.comment = prefix; return e }

// TabSize sets how many cells apart tab stops are.
func (e *codeEditor) TabSize(n int) *codeEditor {
	if n > 0 {
		e.tabSize = n
	}
	return e
}

// OnChange runs fn with the text after every edit.
func (e *codeEditor) OnChange(fn func(string)) *codeEditor { e.input.OnChange(fn); return e }

// OnPress runs fn when the editor is pressed.
func (e *codeEditor) OnPress(fn func()) *codeEditor { e.onPress = fn; return e }

// AutoFocus takes the keyboard on the first paint.
func (e *codeEditor) AutoFocus(v bool) *codeEditor { e.autoFocus = v; return e }

func (e *codeEditor) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	if p, ok := uitheme.From(env).Get(paletteKey); ok {
		e.pal = p
	}
	e.input.Style(ggui.TextStyle{Font: ggui.DefaultMonoFont(), Size: e.prefs().FontSize})
	// Painted only through PaintCustom, which wants it laid out first.
	e.input.Layout(ggui.Loose(c.Max()), env)
	return c.Max()
}

// lineStarts are where the lines of s start, worked out again only when
// the text changes.
func (e *codeEditor) lineStarts(s string) []int {
	if e.lines != nil && s == e.linesText {
		return e.lines
	}
	starts := make([]int, 1, strings.Count(s, "\n")+1)
	widest := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			widest = max(widest, i-starts[len(starts)-1])
			starts = append(starts, i+1)
		}
	}
	e.lines, e.linesText = starts, s
	e.widest = max(widest, len(s)-starts[len(starts)-1])
	return starts
}

// lineOf is the line holding byte offset at.
func lineOf(starts []int, at int) int {
	return max(sort.SearchInts(starts, at+1)-1, 0)
}

// lineEnd is where line i ends, before its break.
func lineEnd(s string, starts []int, i int) int {
	if i+1 < len(starts) {
		return starts[i+1] - 1
	}
	return len(s)
}

// runeCells is how many cells r takes: two for a wide character, none for
// a combining mark.
func runeCells(r rune) int {
	switch {
	case r < 0x300:
		return 1
	case unicode.Is(unicode.Mn, r) || r == 0x200d || (r >= 0xfe00 && r <= 0xfe0f):
		return 0
	}
	switch width.LookupRune(r).Kind() {
	case width.EastAsianWide, width.EastAsianFullwidth:
		return 2
	}
	if r >= 0x1f300 && r <= 0x1faff {
		return 2 // emoji
	}
	return 1
}

// column is the cell line[:at] ends at, tabs stopping every tabSize.
func (e *codeEditor) column(line string, at int) int {
	col := 0
	for _, r := range line[:min(at, len(line))] {
		if r == '\t' {
			col += e.tabSize - col%e.tabSize
			continue
		}
		col += runeCells(r)
	}
	return col
}

// offsetAtColumn is the byte offset in line nearest the cell boundary
// col, which a character takes only before or after itself, and a mark
// joined to one never apart from it.
func (e *codeEditor) offsetAtColumn(line string, col int) int {
	c := 0
	for i, r := range line {
		w := runeCells(r)
		if r == '\t' {
			w = e.tabSize - c%e.tabSize
		}
		if w == 0 {
			continue
		}
		if col <= c+w/2 {
			return i
		}
		c += w
	}
	return len(line)
}

func (e *codeEditor) Paint(dst *ggui.Canvas, r ggui.Rect) {
	p := e.prefs()
	e.rect = r
	dst.FillRect(r, e.pal.Surface0)
	dst.HitKey(r, e)
	dst.HitPointer(r, e)
	dst.HitCursor(r, ggui.CursorShapeText)
	if e.autoFocus {
		e.autoFocus = false
		dst.RequestFocus(e)
	}
	font := ggui.DefaultMonoFont()
	e.fontSize = p.FontSize
	e.cellW = cellWidth(dst.TextWidth("M", font, p.FontSize), dst.Scale())
	e.lineH = lineHeight(p)
	if e.cellW <= 0 {
		return
	}
	st := e.input.EditingState()
	text := st.Text
	starts := e.lineStarts(text)
	digits := max(len(strconv.Itoa(len(starts))), 3)
	gutterW := editorPad + float64(digits)*e.cellW + gutterGap
	e.textX = r.Origin.X + gutterW
	view := ggui.Sz(r.Size.W-gutterW-editorPad, r.Size.H-editorPad)

	caretLine := lineOf(starts, st.Caret)
	caretCol := e.column(text[starts[caretLine]:lineEnd(text, starts, caretLine)], st.Caret-starts[caretLine])
	if st.Caret != e.lastCaret || text != e.lastText {
		e.reveal(caretLine, caretCol, view)
		e.lastCaret, e.lastText = st.Caret, text
	}
	e.clampScroll(len(starts), caretCol, view)

	first := int(e.scrollY / e.lineH)
	last := min(int((e.scrollY+view.H)/e.lineH)+1, len(starts)-1)
	y0 := r.Origin.Y + editorPad - e.scrollY
	lineY := func(i int) float64 { return y0 + float64(i)*e.lineH }
	textTop := (e.lineH - p.FontSize*1.2) / 2 // the glyphs centred in the line

	clip := dst.Clip(r)
	body := clip.Clip(ggui.Rct(ggui.Pt(e.textX, r.Origin.Y), ggui.Sz(r.Size.W-gutterW, r.Size.H)))
	x0 := e.textX - e.scrollX
	colX := func(c int) float64 { return x0 + float64(c)*e.cellW }

	lo, hi := min(st.Anchor, st.Caret), max(st.Anchor, st.Caret)
	if st.Composing {
		lo, hi = st.Caret, st.Caret
	}
	if e.focused && lo == hi {
		body.FillRect(ggui.Rct(ggui.Pt(e.textX, lineY(caretLine)), ggui.Sz(r.Size.W, e.lineH)), e.pal.Surface1)
	}
	var spans []styleSpan
	if e.colors != nil {
		spans = e.colors(text, e.pal)
	}
	for i := max(first, 0); i <= last; i++ {
		start, end := starts[i], lineEnd(text, starts, i)
		line := text[start:end]
		y := lineY(i)
		// The selection, running past the line's end when it goes on.
		if lo < hi && lo <= end && hi >= start {
			a := e.column(line, max(lo, start)-start)
			b := e.column(line, min(hi, end)-start)
			if hi > end {
				b++
			}
			body.FillRect(ggui.Rct(ggui.Pt(colX(a), y), ggui.Sz(float64(b-a)*e.cellW, e.lineH)), e.pal.Selection)
		}
		e.paintLine(body, line, start, spans, x0, y+textTop)
		num := strconv.Itoa(i + 1)
		fg := e.pal.ExtraMuted
		if i == caretLine {
			fg = e.pal.Muted
		}
		clip.DrawTextCells(num, ggui.TextStyle{Font: font, Size: p.FontSize, Color: fg},
			ggui.Pt(e.textX-gutterGap-float64(len(num))*e.cellW, y+textTop), e.cellW)
	}
	caret := ggui.Rct(ggui.Pt(colX(caretCol), lineY(caretLine)), ggui.Sz(2, e.lineH))
	if st.Composing && st.CompStart >= starts[caretLine] {
		// The composition, which is in the text shown, is underlined.
		line := text[starts[caretLine]:lineEnd(text, starts, caretLine)]
		a := e.column(line, st.CompStart-starts[caretLine])
		b := e.column(line, st.CompEnd-starts[caretLine])
		body.FillRect(ggui.Rct(ggui.Pt(colX(a), lineY(caretLine)+e.lineH-2), ggui.Sz(float64(b-a)*e.cellW, 1)), e.pal.Fg)
	}
	if e.focused && !e.readOnly {
		now := ggui.FrameTime()
		phase := now.Sub(e.blinkFrom) / cursorRate
		if phase%2 == 0 || !p.CursorBlink {
			body.FillRect(caret, e.pal.Fg)
		}
		if p.CursorBlink {
			ggui.WakeAt(e.blinkFrom.Add((phase + 1) * cursorRate))
		}
	}
	e.input.PaintCustom(clip, r, func(*ggui.Canvas, ggui.Rect, ggui.TextInputState) ggui.Rect { return caret })
}

// paintLine draws one line from its text's offset start: runs of plain
// ASCII in one style together, and anything else a cell, or two, of its
// own, so wide characters and fallback fonts keep to the grid.
func (e *codeEditor) paintLine(dst *ggui.Canvas, line string, start int, spans []styleSpan, x0, y float64) {
	font := ggui.DefaultMonoFont()
	si := sort.Search(len(spans), func(i int) bool { return spans[i].End > start })
	colorAt := func(at int) color.Color {
		for si < len(spans) && spans[si].End <= at {
			si++
		}
		if si < len(spans) && spans[si].Start <= at {
			return spans[si].Color
		}
		return e.pal.Fg
	}
	// nextChange is the first offset after at where the colour may change.
	nextChange := func(at int) int {
		if si < len(spans) && spans[si].Start <= at {
			return spans[si].End
		}
		if si < len(spans) {
			return spans[si].Start
		}
		return math.MaxInt
	}
	right := e.rect.Origin.X + e.rect.Size.W
	col := 0
	for i := 0; i < len(line); {
		x := x0 + float64(col)*e.cellW
		if x > right {
			return
		}
		c := line[i]
		switch {
		case c == '\t':
			col += e.tabSize - col%e.tabSize
			i++
		case c == ' ':
			col++
			i++
		case c < utf8.RuneSelf:
			fg := colorAt(start + i)
			stop := min(len(line), nextChange(start+i)-start)
			j := i
			for j < stop && line[j] < utf8.RuneSelf && line[j] != '\t' {
				j++
			}
			dst.DrawTextCells(line[i:j], ggui.TextStyle{Font: font, Size: e.fontSize, Color: fg}, ggui.Pt(x, y), e.cellW)
			col += j - i
			i = j
		default:
			r, n := utf8.DecodeRuneInString(line[i:])
			j := i + n
			// Marks that join it go with it.
			for j < len(line) {
				r2, n2 := utf8.DecodeRuneInString(line[j:])
				if runeCells(r2) != 0 {
					break
				}
				j += n2
			}
			w := runeCells(r)
			dst.DrawTextStyle(line[i:j], ggui.TextStyle{Font: font, Size: e.fontSize, Color: colorAt(start + i)}, ggui.Pt(x, y))
			col += max(w, 1)
			i = j
		}
	}
}

// reveal scrolls the least that brings the caret into view.
func (e *codeEditor) reveal(line, col int, view ggui.Size) {
	y := float64(line) * e.lineH
	switch {
	case y < e.scrollY:
		e.scrollY = y
	case y+e.lineH > e.scrollY+view.H:
		e.scrollY = y + e.lineH - view.H
	}
	x := float64(col) * e.cellW
	margin := 4 * e.cellW
	switch {
	case x < e.scrollX+margin:
		e.scrollX = max(x-margin, 0)
	case x > e.scrollX+view.W-margin:
		e.scrollX = x - view.W + margin
	}
}

// clampScroll keeps the view on the text: the last line can scroll up to
// the top, as editors let it, and sideways to the widest line, or the
// caret where tabs put it further.
func (e *codeEditor) clampScroll(lines, caretCol int, view ggui.Size) {
	e.scrollY = min(max(e.scrollY, 0), max(float64(lines-1)*e.lineH, 0))
	right := float64(max(e.widest+e.tabSize, caretCol+4))*e.cellW - view.W
	e.scrollX = min(max(e.scrollX, 0), max(right, 0))
}

// Keyboard ----------------------------------------------------------------------

// HandleKey implements ggui.KeyHandler.
func (e *codeEditor) HandleKey(ev ggui.KeyEvent) {
	defer e.publish()
	switch ev.Kind {
	case ggui.KeyFocus, ggui.KeyBlur:
		e.focused = ev.Kind == ggui.KeyFocus
		e.blinkFrom = ggui.Now()
		e.input.HandleKey(ev)
	case ggui.KeyText:
		if !e.readOnly {
			e.input.HandleKey(ev)
		}
	case ggui.KeyPress:
		e.blinkFrom = ggui.Now()
		if e.input.EditingState().Composing {
			e.input.HandleKey(ev)
			return
		}
		vertical := ev.Key == ggui.KeyArrowUp || ev.Key == ggui.KeyArrowDown || ev.Key == ggui.KeyPageUp || ev.Key == ggui.KeyPageDown
		if !vertical {
			e.goalCol = -1
		}
		if e.command(ev) {
			return
		}
		if e.readOnly && !readOnlyKey(ev) {
			return
		}
		e.input.HandleKey(ev)
	}
}

// readOnlyKey reports whether a key only moves, selects or copies, which a
// read-only editor still does.
func readOnlyKey(ev ggui.KeyEvent) bool {
	switch ev.Key {
	case ggui.KeyArrowLeft, ggui.KeyArrowRight, ggui.KeyArrowUp, ggui.KeyArrowDown, ggui.KeyHome, ggui.KeyEnd, ggui.KeyEscape:
		return true
	case ggui.KeyA, ggui.KeyC:
		return ev.Mods.Cmd()
	}
	return false
}

// command handles the keys the editor does itself, and reports whether
// it did.
func (e *codeEditor) command(ev ggui.KeyEvent) bool {
	m := ev.Mods
	switch ev.Key {
	case ggui.KeyArrowUp, ggui.KeyArrowDown:
		if m.Meta || m.Alt || m.Ctrl {
			return false // the ends of the text, as the field has it
		}
		e.moveLines(pick(ev.Key == ggui.KeyArrowUp, -1, 1), m.Shift)
		return true
	case ggui.KeyPageUp, ggui.KeyPageDown:
		rows := max(int((e.rect.Size.H-editorPad)/max(e.lineH, 1))-1, 1)
		e.scrollY += float64(pick(ev.Key == ggui.KeyPageUp, -rows, rows)) * e.lineH
		e.moveLines(pick(ev.Key == ggui.KeyPageUp, -rows, rows), m.Shift)
		return true
	case ggui.KeyEnter, ggui.KeyNumpadEnter:
		if e.readOnly || m.Cmd() {
			return e.readOnly
		}
		st := e.input.EditingState()
		starts := e.lineStarts(st.Text)
		lo := min(st.Anchor, st.Caret)
		start := starts[lineOf(starts, lo)]
		e.input.Replace("\n" + leadingSpace(st.Text[start:lo]))
		return true
	case ggui.KeyTab:
		if e.readOnly || m.Cmd() || m.Ctrl || m.Alt {
			return e.readOnly
		}
		e.indent(m.Shift)
		return true
	case ggui.KeySlash:
		if !m.Cmd() || e.readOnly || e.comment == "" {
			return false
		}
		e.toggleComment()
		return true
	}
	return false
}

// toggleComment is ⌘/: the lines of the selection, or the caret's, are
// commented out, unless every one with text already is, when they are
// uncommented. The comment goes after the indent the lines share.
func (e *codeEditor) toggleComment() {
	st := e.input.EditingState()
	text := st.Text
	starts := e.lineStarts(text)
	lo, hi := min(st.Anchor, st.Caret), max(st.Anchor, st.Caret)
	first, last := lineOf(starts, lo), lineOf(starts, hi)
	if last > first && hi == starts[last] {
		last--
	}
	mark := strings.TrimRight(e.comment, " ")
	lines := make([]string, 0, last-first+1)
	commented, indent := true, -1
	for i := first; i <= last; i++ {
		line := text[starts[i]:lineEnd(text, starts, i)]
		lines = append(lines, line)
		body := strings.TrimLeft(line, " \t")
		if body == "" {
			continue
		}
		if !strings.HasPrefix(body, mark) {
			commented = false
		}
		if n := len(line) - len(body); indent < 0 || n < indent {
			indent = n
		}
	}
	if indent < 0 {
		return // only blank lines
	}
	for i, line := range lines {
		body := strings.TrimLeft(line, " \t")
		switch {
		case body == "":
		case commented:
			at := len(line) - len(body)
			rest := strings.TrimPrefix(body, mark)
			if strings.HasPrefix(rest, " ") && strings.HasSuffix(e.comment, " ") {
				rest = rest[1:]
			}
			lines[i] = line[:at] + rest
		default:
			lines[i] = line[:indent] + e.comment + line[indent:]
		}
	}
	from, to := starts[first], lineEnd(text, starts, last)
	joined := strings.Join(lines, "\n")
	e.input.Select(from, to)
	e.input.Replace(joined)
	if lo == hi {
		// The caret stays on its line, after the same text.
		at := min(max(lo+len(joined)-(to-from), from), from+len(joined))
		e.input.Select(at, at)
	} else {
		e.input.Select(from, from+len(joined))
	}
}

func pick[T any](cond bool, a, b T) T {
	if cond {
		return a
	}
	return b
}

// leadingSpace is the run of spaces and tabs s starts with.
func leadingSpace(s string) string {
	return s[:len(s)-len(strings.TrimLeft(s, " \t"))]
}

// indentUnit is what Tab inserts: a tab where the text indents with tabs,
// else spaces to the next stop.
func (e *codeEditor) indentUnit(text string, col int) string {
	if strings.Contains(text, "\n\t") || strings.HasPrefix(text, "\t") {
		return "\t"
	}
	return strings.Repeat(" ", e.tabSize-col%e.tabSize)
}

// indent is Tab: in a selection over more than one line it indents each,
// or with Shift takes an indent off each; otherwise it inserts an indent,
// or with Shift takes one off the caret's line.
func (e *codeEditor) indent(out bool) {
	st := e.input.EditingState()
	text := st.Text
	starts := e.lineStarts(text)
	lo, hi := min(st.Anchor, st.Caret), max(st.Anchor, st.Caret)
	first, last := lineOf(starts, lo), lineOf(starts, hi)
	if last > first && hi == starts[last] {
		last-- // a selection ending at a line's start leaves that line be
	}
	if !out && first == last && (lo == hi || lo > starts[first]) {
		line := text[starts[first]:lineEnd(text, starts, first)]
		e.input.Replace(e.indentUnit(text, e.column(line, lo-starts[first])))
		return
	}
	unit := e.indentUnit(text, 0)
	var b strings.Builder
	from, to := starts[first], lineEnd(text, starts, last)
	for i := first; i <= last; i++ {
		line := text[starts[i]:lineEnd(text, starts, i)]
		switch {
		case !out && line != "":
			b.WriteString(unit)
		case out && strings.HasPrefix(line, "\t"):
			line = line[1:]
		case out:
			n := len(line) - len(strings.TrimLeft(line, " "))
			line = line[min(n, e.tabSize):]
		}
		b.WriteString(line)
		if i < last {
			b.WriteByte('\n')
		}
	}
	if b.String() == text[from:to] {
		return
	}
	e.input.Select(from, to)
	e.input.Replace(b.String())
	e.input.Select(from, from+b.Len())
}

// moveLines moves the caret n lines up (n < 0) or down, keeping to the
// column it had when it started moving.
func (e *codeEditor) moveLines(n int, extend bool) {
	st := e.input.EditingState()
	text := st.Text
	starts := e.lineStarts(text)
	li := lineOf(starts, st.Caret)
	if e.goalCol < 0 {
		e.goalCol = e.column(text[starts[li]:lineEnd(text, starts, li)], st.Caret-starts[li])
	}
	to := li + n
	var at int
	switch {
	case to < 0:
		at = 0
	case to >= len(starts):
		at = len(text)
	default:
		line := text[starts[to]:lineEnd(text, starts, to)]
		at = starts[to] + e.offsetAtColumn(line, e.goalCol)
	}
	anchor := at
	if extend {
		anchor = st.Anchor
	}
	e.input.Select(anchor, at)
}

// ConsumesKey implements ggui.KeyConsumer: the editor keeps the keys,
// Tab among them, but lets Escape go unless the IME used it.
func (e *codeEditor) ConsumesKey(ev ggui.KeyEvent) bool {
	return ev.Kind == ggui.KeyPress && (ev.Key != ggui.KeyEscape || e.input.ConsumesKey(ev))
}

// ClaimsChord implements ggui.ChordClaimer.
func (e *codeEditor) ClaimsChord(ev ggui.KeyEvent) bool {
	if ev.Kind != ggui.KeyPress {
		return false
	}
	switch ev.Key {
	case ggui.KeyArrowUp, ggui.KeyArrowDown, ggui.KeyPageUp, ggui.KeyPageDown:
		return true
	case ggui.KeyTab:
		// Tab indents rather than moving the focus on, unless read-only.
		return !e.readOnly && !ev.Mods.Ctrl && !ev.Mods.Alt && !ev.Mods.Meta
	case ggui.KeySlash:
		return ev.Mods.Cmd() && !e.readOnly && e.comment != ""
	}
	return e.input.ClaimsChord(ev)
}

// HandleTick implements ggui.TickHandler: it runs the platform IME while
// focused.
func (e *codeEditor) HandleTick() bool {
	if e.readOnly {
		return false
	}
	more := e.input.HandleTick()
	if e.input.EditingState().Composing {
		e.publish()
	}
	return more
}

// Pointer -------------------------------------------------------------------------

// offsetAt is the text offset nearest a point.
func (e *codeEditor) offsetAt(p ggui.Point) int {
	text := e.input.EditingState().Text
	starts := e.lineStarts(text)
	li := int(math.Floor((p.Y - e.rect.Origin.Y - editorPad + e.scrollY) / max(e.lineH, 1)))
	switch {
	case li < 0:
		return 0
	case li >= len(starts):
		return len(text)
	}
	col := int(math.Round((p.X - e.textX + e.scrollX) / max(e.cellW, 1)))
	line := text[starts[li]:lineEnd(text, starts, li)]
	return starts[li] + e.offsetAtColumn(line, max(col, 0))
}

// HandlePointer implements ggui.PointerHandler: a click places the caret,
// a second selects a word and a third the line, a drag selects, Shift
// extends, and the wheel scrolls.
func (e *codeEditor) HandlePointer(ev ggui.PointerEvent) bool {
	if ev.Kind == ggui.PointerDown || ev.Kind == ggui.PointerDrag {
		defer e.publish()
	}
	switch ev.Kind {
	case ggui.PointerDown:
		if ev.Button != ggui.MouseButtonLeft {
			return false
		}
		if e.onPress != nil {
			e.onPress()
		}
		e.blinkFrom, e.goalCol = ggui.Now(), -1
		now := ggui.FrameTime()
		if now.Sub(e.lastClick) < 400*time.Millisecond && math.Abs(ev.Pos.X-e.lastPos.X) < 4 && math.Abs(ev.Pos.Y-e.lastPos.Y) < 4 {
			e.clicks++
		} else {
			e.clicks = 1
		}
		e.lastClick, e.lastPos = now, ev.Pos
		at := e.offsetAt(ev.Pos)
		st := e.input.EditingState()
		switch {
		case ev.Mods.Shift:
			e.clicks = 1
			e.input.Select(st.Anchor, at)
			e.dragFrom = st.Anchor
		case e.clicks == 1:
			e.input.Select(at, at)
			e.dragFrom = at
		case e.clicks == 2:
			a, b := wordAt(st.Text, at)
			e.input.Select(a, b)
			e.dragFrom = -1
		default:
			starts := e.lineStarts(st.Text)
			li := lineOf(starts, at)
			end := len(st.Text)
			if li+1 < len(starts) {
				end = starts[li+1]
			}
			e.input.Select(starts[li], end)
			e.dragFrom = -1
		}
		return true
	case ggui.PointerDrag:
		if e.dragFrom >= 0 {
			e.input.Select(e.dragFrom, e.offsetAt(ev.Pos))
		}
		return true
	case ggui.PointerUp:
		e.dragFrom = -1
		return true
	case ggui.PointerScroll:
		dx, dy := ev.Scroll.X, ev.Scroll.Y
		if !ev.ScrollPixels {
			dx, dy = dx*wheelLines*e.lineH, dy*wheelLines*e.lineH
		}
		if ev.Mods.Shift && dx == 0 {
			dx, dy = dy, 0
		}
		e.scrollX -= dx
		e.scrollY -= dy
		return true
	}
	return false
}

// wordAt is the word, or the run of spaces or of punctuation, around
// offset at.
func wordAt(s string, at int) (int, int) {
	class := func(r rune) int {
		switch {
		case r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
			return 0
		case r == ' ' || r == '\t':
			return 1
		case r == '\n':
			return 3
		}
		return 2
	}
	if at >= len(s) {
		return len(s), len(s)
	}
	r, _ := utf8.DecodeRuneInString(s[at:])
	k := class(r)
	if k == 3 {
		return at, at
	}
	a, b := at, at
	for a > 0 {
		r, n := utf8.DecodeLastRuneInString(s[:a])
		if class(r) != k {
			break
		}
		a -= n
	}
	for b < len(s) {
		r, n := utf8.DecodeRuneInString(s[b:])
		if class(r) != k {
			break
		}
		b += n
	}
	return a, b
}
