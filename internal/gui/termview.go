package gui

import (
	"image/color"
	"math"
	"runtime"
	"time"
	"unicode/utf8"

	"github.com/ironpark/ggui"
	ggruntime "github.com/ironpark/ggui/runtime"
	uitheme "github.com/ironpark/ggui/ui/theme"
	"github.com/ironpark/gostty"
	"github.com/ironpark/gostty/input"
)

// terminalView draws a termConn's screen and sends it keyboard, mouse and
// wheel input. It claims Tab and the Ctrl and Alt chords for the program, so
// a shell completes and Ctrl+C interrupts; ⌘ chords stay with the app.
//
// Text reaches it through a hidden TextInput, which drives the platform
// IME: a syllable is shown at the cursor while it is composed and sent
// once committed. The macOS Korean IME composes without marked text: it
// commits each jamo and rewrites the syllable before the caret as the next
// joins it, so the last Hangul syllable committed is held, shown as it is
// composed, until the next key shows it is done; see overHeldSyllable.
type terminalView struct {
	conn      *termConn
	prefs     func() prefs
	autoFocus bool   // take the keyboard on first paint, as a new tab does
	onPress   func() // a press anywhere in it, which makes its pane current
	// onDrop hands over files dropped from the desktop, by their paths
	// here, and types what it gives back; nil types the paths as they are.
	onDrop func(paths []string, typed func([]string))
	// onPasteImage hands over an image pasted, as a file to type the path of
	onPasteImage func(png []byte, typed func([]string))
	// onLink follows a link ⌘-clicked, as written, with where the program
	// said it was.
	onLink func(target, cwd string)
	// link is the link under the pointer while ⌘ is held, underlined;
	// probed is the cell it was last looked for at.
	link   *termLink
	probed [2]int

	focused  bool
	wheelAcc float64
	blink    time.Time // when the cursor last started a lit phase

	ime     *ggui.TextInputWidget
	imeText *ggui.StateValue[string]
	// held are keys pressed while the IME composes, which it may take for
	// itself; see settleHeld.
	held []input.KeyEvent
	// tickText is the editor's text before this frame's IME edits.
	tickText string
	// dropOver is whether files are dragged over the terminal, which
	// outlines itself to say it takes them.
	dropOver bool
	// sent is how much of the editor's text has gone to the program. What
	// was sent stays until nothing can be rewritten, as the IME finds the
	// syllable it rewrites by its place in the text.
	sent int

	// Mouse reporting: a press the program took owns the drag and the
	// release that follow it.
	grabbed   bool
	grabBtn   input.MouseButton
	lastCell  [2]int
	dragMods  ggui.Mods
	selecting bool
	lastDrag  pointerCell // where a selecting drag last was
	scrolled  time.Time   // when a held drag last scrolled the view

	// From the last layout and paint, in logical pixels.
	pal          palette
	origin       ggui.Point // the grid's top-left
	rect         ggui.Rect  // the whole widget
	cellW, cellH float64
	cols, rows   int
	runBuf       []byte // the text of a run of cells, kept between paints
}

const (
	termPad    = 10
	cursorRate = 530 * time.Millisecond
)

func newTerminalView(conn *termConn, prefs func() prefs, autoFocus bool) *terminalView {
	text := ggui.State("")
	return &terminalView{
		conn: conn, prefs: prefs, autoFocus: autoFocus,
		ime: ggui.TextInput(text), imeText: text,
		pal: darkPalette, lastCell: [2]int{-1, -1}, probed: [2]int{-1, -1},
	}
}

// OnPress runs fn when the terminal is pressed.
func (t *terminalView) OnPress(fn func()) *terminalView { t.onPress = fn; return t }

// OnPasteImage takes a paste of a clipboard holding an image and no text:
// fn hands the image over and calls typed with the path to type.
func (t *terminalView) OnPasteImage(fn func(png []byte, typed func([]string))) *terminalView {
	t.onPasteImage = fn
	return t
}

// OnDrop hands over the files dropped on the terminal from the desktop
// before their paths are typed, as to a session that cannot read them
// where they are.
func (t *terminalView) OnDrop(fn func(paths []string, typed func([]string))) *terminalView {
	t.onDrop = fn
	return t
}

// OnLink follows the links ⌘-clicked in the terminal.
func (t *terminalView) OnLink(fn func(target, cwd string)) *terminalView {
	t.onLink = fn
	return t
}

func (t *terminalView) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	if p, ok := uitheme.From(env).Get(paletteKey); ok {
		t.pal = p
	}
	// The editor is never painted as itself, but PaintCustom wants it laid
	// out first.
	t.ime.Layout(ggui.Loose(c.Max()), env)
	return c.Max()
}

func (t *terminalView) Paint(dst *ggui.Canvas, r ggui.Rect) {
	p := t.prefs()
	t.conn.configure(termOptionsFor(p, t.pal))
	t.rect = r
	dst.FillRect(r, t.pal.Surface0)
	dst.HitKey(r, t)
	dst.HitPointer(r, t)
	dst.HitCursor(r, ggui.CursorShapeText)
	if t.autoFocus {
		t.autoFocus = false
		dst.RequestFocus(t)
	}
	// What the program copied, as claude does a selection made in it.
	if text, ok := t.conn.takeCopied(); ok {
		ggui.CurrentClipboard().Write(text)
	}

	// The platform's mono font and its italic; both fall back to the system
	// fonts, which carry the symbols agents print (❯ ⏵ ⏺ ⎿).
	font := ggui.DefaultMonoFont()
	_, italic := ggui.SystemItalicFonts()
	t.cellW = cellWidth(dst.TextWidth("M", font, p.FontSize), dst.Scale())
	t.cellH = math.Ceil(p.FontSize * p.LineHeight)
	if t.cellW <= 0 {
		return
	}
	t.origin = ggui.Pt(r.Origin.X+termPad, r.Origin.Y+termPad/2)
	t.cols = int((r.Size.W - 2*termPad) / t.cellW)
	t.rows = int((r.Size.H - termPad) / t.cellH)
	if t.cols < 2 || t.rows < 2 {
		return
	}
	t.conn.resize(uint16(t.cols), uint16(t.rows))

	f, err := t.conn.frame()
	if err != nil && f.cols == 0 {
		return
	}
	g := grid{origin: t.origin, cellW: t.cellW, cellH: t.cellH, size: p.FontSize, font: font, italic: italic, pal: t.pal, scale: dst.Scale(), runBuf: &t.runBuf}
	clip := dst.Clip(r)
	g.paint(clip, f)

	caret := g.cellRect(int(f.cursor.X), int(f.cursor.Y), 1)
	if !f.exited {
		t.paintCursor(clip, g, f, p)
	}
	t.ime.PaintCustom(clip, r, func(dst *ggui.Canvas, _ ggui.Rect, st ggui.TextInputState) ggui.Rect {
		// A composition, or a syllable held for one; what was sent already
		// shows on the screen.
		if unsent := st.Text[min(t.sent, len(st.Text)):]; unsent != "" {
			g.paintPreedit(dst, caret.Origin, unsent)
		}
		return caret
	})
	if f.exited || f.err != nil {
		t.paintBanner(clip, r, f)
	}
	if t.link != nil {
		if pt, ok := dst.Pointer(); !ok || !r.Contains(pt) {
			t.link = nil
		} else {
			t.link.spans(f.cols, func(row, col0, col1 int) {
				at := g.cellRect(col0, row, col1-col0)
				clip.FillRect(ggui.Rct(ggui.Pt(at.Origin.X, at.Origin.Y+at.Size.H-1), ggui.Sz(at.Size.W, 1)), t.pal.AccentBright)
				dst.HitCursor(at, ggui.CursorShapePointer)
			})
		}
	}
	if t.dropOver {
		dst.StrokeRoundRect(ggui.Rct(r.Origin.Add(ggui.Pt(3, 3)), ggui.Sz(r.Size.W-6, r.Size.H-6)), 6, 2, t.pal.Ring)
	}
}

// paintCursor draws the cursor in the shape the program asked for, lit or
// dark in its blink. Unfocused, it is an outline that does not blink.
func (t *terminalView) paintCursor(dst *ggui.Canvas, g grid, f frameSnapshot, p prefs) {
	c := f.cursor
	if !c.Visible || !c.ViewportHasValue || int(c.X) >= f.cols || int(c.Y) >= f.rows {
		return
	}
	col, width := int(c.X), 1
	if c.WideTail {
		col, width = col-1, 2
	}
	idx := int(c.Y)*f.cols + col
	if idx >= 0 && idx < len(f.cells) && f.cells[idx].Flags.Wide == gostty.CellWidthWide {
		width = 2
	}
	r := g.cellRect(col, int(c.Y), width)
	if !t.focused {
		strokeRect(dst, r, t.pal.Fg)
		return
	}
	if c.Blinking && p.CursorBlink {
		now := ggui.FrameTime()
		if t.blink.IsZero() {
			t.blink = now
		}
		phase := now.Sub(t.blink) / cursorRate
		ggui.WakeAt(t.blink.Add((phase + 1) * cursorRate))
		if phase%2 == 1 {
			return
		}
	}
	switch c.Style {
	case gostty.CursorStyleBar:
		dst.FillRect(ggui.Rect{Origin: r.Origin, Size: ggui.Sz(2, r.Size.H)}, t.pal.Fg)
	case gostty.CursorStyleUnderline:
		dst.FillRect(ggui.Rect{Origin: ggui.Pt(r.Origin.X, r.Origin.Y+r.Size.H-2), Size: ggui.Sz(r.Size.W, 2)}, t.pal.Fg)
	case gostty.CursorStyleBlockHollow:
		strokeRect(dst, r, t.pal.Fg)
	default:
		dst.FillRect(r, t.pal.Fg)
		// The glyph under a block cursor is redrawn in the background colour,
		// except while the program reads a password.
		if idx >= 0 && idx < len(f.cells) && !c.PasswordInput && !skipGlyph(f.cells[idx]) {
			g.cellGlyph(dst, f.cells[idx], f.textAt(idx), r, t.pal.Surface0)
		}
	}
}

// paintBanner states why the screen has stopped changing.
func (t *terminalView) paintBanner(dst *ggui.Canvas, r ggui.Rect, f frameSnapshot) {
	msg := Tf("Process exited with code %d", f.exitCode)
	if !f.exited && f.err != nil {
		msg = Tf("Disconnected: %v", f.err)
	}
	h := t.cellH + 12
	band := ggui.Rect{Origin: ggui.Pt(r.Origin.X, r.Origin.Y+r.Size.H-h), Size: ggui.Sz(r.Size.W, h)}
	dst.FillRect(band, t.pal.Surface2)
	dst.FillRect(ggui.Rect{Origin: band.Origin, Size: ggui.Sz(band.Size.W, 1)}, t.pal.Border)
	dst.DrawText(msg, nil, 12, ggui.Pt(band.Origin.X+termPad, band.Origin.Y+(h-12*1.2)/2), t.pal.Muted)
}

// Keyboard --------------------------------------------------------------------

// HandleKey implements ggui.KeyHandler.
func (t *terminalView) HandleKey(ev ggui.KeyEvent) {
	switch ev.Kind {
	case ggui.KeyFocus, ggui.KeyBlur:
		t.focused = ev.Kind == ggui.KeyFocus
		t.blink = time.Time{}
		t.ime.HandleKey(ev)
		t.flushText(true)
		t.conn.requestPaint()
	case ggui.KeyText:
		// The editor commits typed text itself where there is no platform
		// IME, and leaves it to the IME where there is, so text is sent from
		// one place either way.
		t.ime.HandleKey(ev)
		t.flushText(false)
	case ggui.KeyPress:
		t.blink = time.Time{}
		if t.chord(ev) {
			return
		}
		if ev.Mods.Meta && runtime.GOOS == "darwin" {
			return // the other ⌘ chords are the app's
		}
		code, ok := terminalKey(ev.Key)
		if !ok || code.Modifier() {
			return
		}
		// A printable key arrives again as text, already through the layout,
		// dead keys and the IME; it is described as a key only when a
		// modifier took its text away.
		if code.Printable() && code != input.KeyTab && !ev.Mods.Ctrl && (!ev.Mods.Alt || altMakesText) {
			return
		}
		unshifted, _ := code.Codepoint()
		key := input.KeyEvent{
			Action:             input.KeyActionPress,
			Key:                code,
			Mods:               keyMods(ev.Mods),
			UnshiftedCodepoint: unshifted,
		}
		if t.ime.EditingState().Composing {
			t.held = append(t.held, key)
			return
		}
		if t.overHeldSyllable(key) {
			return
		}
		t.conn.clearSelection()
		t.conn.key(key, "")
	}
}

// settleHeld decides the keys pressed while the IME composed, once it has
// seen them. One that ended the composition with a commit, as Enter or an
// arrow does, goes to the program after the committed text, so the last
// syllable is not typed after the Enter that ended it. One the IME kept
// composing through, or that took the composition back to nothing, as
// Backspace does, was the IME's own and goes nowhere.
func (t *terminalView) settleHeld() {
	defer func() { t.held = t.held[:0] }()
	if len(t.held) == 0 || t.ime.EditingState().Composing {
		return
	}
	committed := ggui.Peek(t.imeText) != ""
	t.flushText(true)
	if committed {
		t.sendKeys(t.held)
	}
}

// overHeldSyllable handles a key pressed over a syllable held for the IME,
// and reports whether there was one. The key reaches here only when the
// IME passed it on. Backspace takes the syllable back, unless the IME
// rewrote it in the same keystroke; any other key sends the syllable
// first, so Enter runs the line it ends.
func (t *terminalView) overHeldSyllable(key input.KeyEvent) bool {
	rw := t.rewritable()
	if rw == "" {
		return false
	}
	if key.Key == input.KeyBackspace {
		if text := ggui.Peek(t.imeText); text == t.tickText {
			t.imeText.Set(text[:len(text)-len(rw)])
		}
		return true
	}
	t.flushText(true)
	t.sendKeys([]input.KeyEvent{key})
	return true
}

func (t *terminalView) sendKeys(keys []input.KeyEvent) {
	for _, key := range keys {
		t.conn.clearSelection()
		t.conn.key(key, "")
	}
}

// HandleTick implements ggui.TickHandler: it runs the platform IME while
// focused, and sends whatever the IME committed.
func (t *terminalView) HandleTick() bool {
	t.tickText = ggui.Peek(t.imeText)
	handled := t.ime.HandleTick()
	t.settleHeld()
	t.flushText(false)
	t.autoscroll()
	return handled
}

// autoscrollRate is how often a drag held above or below the grid scrolls
// the view by a row.
const autoscrollRate = 50 * time.Millisecond

// autoscroll keeps a selecting drag held past the top or bottom of the grid
// scrolling, as it would were the pointer moving.
func (t *terminalView) autoscroll() {
	pc := t.lastDrag
	if !t.selecting || (pc.ypos >= 0 && pc.ypos < pc.height) {
		return
	}
	now := ggui.FrameTime()
	if now.Sub(t.scrolled) >= autoscrollRate {
		t.scrolled = now
		t.conn.selectDrag(pc, t.dragMods.Alt)
		t.conn.requestPaint()
	}
	ggui.WakeAt(t.scrolled.Add(autoscrollRate))
}

// flushText sends text the editor holds once nothing is being composed,
// but for a syllable the IME may still rewrite unless all is set, and
// clears the editor once all of it is sent.
func (t *terminalView) flushText(all bool) {
	text := ggui.Peek(t.imeText)
	if t.ime.EditingState().Composing {
		return
	}
	t.sent = min(t.sent, len(text))
	end := len(text)
	if !all {
		end -= len(t.rewritable())
	}
	if end > t.sent {
		t.conn.clearSelection()
		t.conn.key(input.KeyEvent{Action: input.KeyActionPress, Key: input.KeyUnidentified}, text[t.sent:end])
		t.sent = end
	}
	if t.sent == len(text) && text != "" {
		t.imeText.Set("")
		t.sent = 0
	}
}

// holdsSyllables is whether the IME rewrites syllables it committed, as
// the macOS Korean one does; elsewhere they are composed in marked text.
var holdsSyllables = runtime.GOOS == "darwin"

// rewritable is the Hangul syllable or jamo the editor's text ends in,
// which the Korean IME may still rewrite as the next jamo joins it, or "".
func (t *terminalView) rewritable() string {
	if !holdsSyllables {
		return ""
	}
	text := ggui.Peek(t.imeText)
	r, n := utf8.DecodeLastRuneInString(text)
	if (r >= 0x1100 && r <= 0x11ff) || (r >= 0x3131 && r <= 0x318e) || (r >= 0xac00 && r <= 0xd7a3) {
		return text[len(text)-n:]
	}
	return ""
}

// HandleDrop implements ggui.DropHandler: files dropped on the terminal
// are typed as their paths, quoted for a shell, as a terminal does.
func (t *terminalView) HandleDrop(ev ggui.DropEvent) bool {
	t.setDropOver(false)
	paths := ev.Paths()
	if len(paths) == 0 {
		return false
	}
	if t.onDrop != nil {
		t.onDrop(paths, t.typePaths)
		return true
	}
	t.typePaths(paths)
	return true
}

// HandleDrag implements ggui.DragHandler.
func (t *terminalView) HandleDrag(ev ggui.DragEvent) bool {
	t.setDropOver(ev.Kind == ggui.DragOver)
	return true
}

func (t *terminalView) setDropOver(over bool) {
	if t.dropOver != over {
		t.dropOver = over
		t.conn.requestPaint()
	}
}

// typePaths types paths at the prompt, after what the IME still holds.
func (t *terminalView) typePaths(paths []string) {
	t.flushText(true)
	t.conn.clearSelection()
	t.conn.paste(shellPaths(paths))
}

// chord runs the terminal's own chords -- paste, copy, select all -- and
// reports whether ev was one.
func (t *terminalView) chord(ev ggui.KeyEvent) bool {
	switch {
	case isPaste(ev):
		t.conn.clearSelection()
		clip := ggui.CurrentClipboard()
		text := clip.Read()
		if png, ok := ggruntime.ReadImage(clip); text == "" && ok && t.onPasteImage != nil {
			t.onPasteImage(png, t.typePaths)
		} else {
			t.conn.paste(text)
		}
	case isCopy(ev):
		if text := t.conn.selection(); text != "" {
			ggui.CurrentClipboard().Write(text)
		}
	case isSelectAll(ev):
		t.conn.selectAll()
		t.conn.requestPaint()
	default:
		return false
	}
	return true
}

// ConsumesKey implements ggui.KeyConsumer: every bare key is the program's,
// Escape included, so a dialog behind the terminal does not close on it.
func (t *terminalView) ConsumesKey(ev ggui.KeyEvent) bool { return ev.Kind == ggui.KeyPress }

// ClaimsChord implements ggui.ChordClaimer: Tab, the Ctrl and Alt chords and
// the terminal's own chords reach it ahead of focus traversal and any
// shortcut, so a shell completes on Tab and Ctrl+C interrupts. Copy is
// claimed only while something is selected, so ⌘C otherwise stays the app's.
func (t *terminalView) ClaimsChord(ev ggui.KeyEvent) bool {
	switch {
	case ev.Key == ggui.KeyTab, isPaste(ev), isSelectAll(ev):
		return true
	case isCopy(ev):
		return t.conn.selection() != ""
	}
	if runtime.GOOS == "darwin" && ev.Mods.Meta {
		return false
	}
	return ev.Mods.Ctrl || ev.Mods.Alt
}

// Pointer ---------------------------------------------------------------------

// HandlePointer implements ggui.PointerHandler. A program that asked for the
// mouse gets presses, drags, motion and the wheel; Shift takes them back for
// selection, as in every terminal. Otherwise a drag selects, two clicks a
// word and three a line, and the wheel scrolls the scrollback.
func (t *terminalView) HandlePointer(ev ggui.PointerEvent) bool {
	pc := t.cellAt(ev.Pos)
	switch ev.Kind {
	case ggui.PointerDown:
		if t.onPress != nil {
			t.onPress()
		}
		t.dragMods = ev.Mods
		// A link is followed before the program hears of the click.
		if ev.Button == ggui.MouseButtonLeft && linkMods(ev.Mods) && t.onLink != nil {
			if l, ok := t.conn.linkAt(pc.col, pc.row); ok {
				t.onLink(l.target, t.conn.pwd())
				return true
			}
		}
		if !ev.Mods.Shift && t.conn.mouseReported() {
			btn := mouseButton(ev.Button)
			if t.reportMouse(input.MouseActionPress, btn, true, ev.Pos, ev.Mods, true) {
				t.grabbed, t.grabBtn = true, btn
				return true
			}
		}
		if ev.Button == ggui.MouseButtonLeft {
			t.selecting = true
			t.conn.selectPress(pc)
			t.conn.requestPaint()
		}
		return true
	case ggui.PointerDrag:
		if t.grabbed {
			if t.moved(pc) {
				t.reportMouse(input.MouseActionMotion, t.grabBtn, true, ev.Pos, t.dragMods, true)
			}
			return true
		}
		if t.selecting {
			t.lastDrag = pc
			t.conn.selectDrag(pc, t.dragMods.Alt)
			t.conn.requestPaint()
		}
		return true
	case ggui.PointerUp:
		if t.grabbed {
			t.grabbed = false
			t.reportMouse(input.MouseActionRelease, t.grabBtn, true, ev.Pos, ev.Mods, false)
			return true
		}
		if t.selecting {
			t.selecting = false
			if text := t.conn.selectRelease(pc); text != "" && t.prefs().CopyOnSelect {
				ggui.CurrentClipboard().Write(text)
			}
		}
		return true
	case ggui.PointerMove:
		t.hoverLink(pc, ev.Mods)
		// Motion without a button is reported only in any-event mode, which
		// the encoder decides from the program's modes. A drag's moves come
		// as PointerDrag too, which reports them with the button held, so
		// they are not reported here as well, buttonless and first.
		if !t.grabbed && !t.selecting && t.conn.mouseReported() && t.moved(pc) {
			t.reportMouse(input.MouseActionMotion, input.MouseButtonUnknown, false, ev.Pos, ggui.Mods{}, false)
		}
		return false
	case ggui.PointerScroll:
		dy := ev.Scroll.Y
		if ev.ScrollPixels && t.cellH > 0 {
			dy /= t.cellH
		} else {
			dy *= 3
		}
		t.wheelAcc += dy
		lines := int(t.wheelAcc)
		if lines == 0 {
			return true
		}
		t.wheelAcc -= float64(lines)
		if !ev.Mods.Shift && t.conn.mouseReported() {
			btn := input.MouseButtonFour // up
			n := lines
			if n < 0 {
				btn, n = input.MouseButtonFive, -n
			}
			for range n {
				t.reportMouse(input.MouseActionPress, btn, true, ev.Pos, ev.Mods, false)
			}
			return true
		}
		t.conn.wheel(lines)
		return true
	}
	return false
}

// hoverLink underlines the link under the pointer while the keys that
// follow it are held.
func (t *terminalView) hoverLink(pc pointerCell, mods ggui.Mods) {
	was := t.link
	switch {
	case t.onLink == nil || !linkMods(mods):
		t.link, t.probed = nil, [2]int{-1, -1}
	case was != nil && was.covers(pc.col, pc.row):
	case t.probed != [2]int{pc.col, pc.row}:
		t.probed = [2]int{pc.col, pc.row}
		t.link = nil
		if l, ok := t.conn.linkAt(pc.col, pc.row); ok {
			t.link = &l
		}
	}
	if (was == nil) != (t.link == nil) || was != nil && t.link != nil && *was != *t.link {
		t.conn.requestPaint()
	}
}

// moved reports whether the pointer entered another cell since the last
// report: the protocol has nothing finer.
func (t *terminalView) moved(pc pointerCell) bool {
	if pc.col == t.lastCell[0] && pc.row == t.lastCell[1] {
		return false
	}
	t.lastCell = [2]int{pc.col, pc.row}
	return true
}

func (t *terminalView) reportMouse(action input.MouseAction, btn input.MouseButton, hasButton bool, pos ggui.Point, mods ggui.Mods, anyPressed bool) bool {
	x, cw := wholeCells(pos.X-t.origin.X, t.cellW)
	ev := input.MouseEvent{
		Action: action, Button: btn, HasButton: hasButton, Mods: keyMods(mods),
		X: float32(x + termPad), Y: float32(pos.Y - t.rect.Origin.Y),
	}
	size := input.RenderSize{
		ScreenWidth: uint32(t.rect.Size.W), ScreenHeight: uint32(t.rect.Size.H),
		CellWidth: uint32(cw), CellHeight: uint32(t.cellH),
		PaddingLeft: termPad, PaddingTop: termPad / 2,
	}
	return t.conn.mouse(ev, size, anyPressed)
}

// cellAt maps a window position onto the grid, clamped to it.
func (t *terminalView) cellAt(p ggui.Point) pointerCell {
	x, y := p.X-t.origin.X, p.Y-t.origin.Y
	col, row := 0, 0
	if t.cellW > 0 && t.cellH > 0 {
		col = min(max(int(x/t.cellW), 0), max(t.cols-1, 0))
		row = min(max(int(y/t.cellH), 0), max(t.rows-1, 0))
	}
	xpos, cw := wholeCells(x, t.cellW)
	return pointerCell{col: col, row: row, xpos: xpos, ypos: y, cellW: cw, cols: t.cols, height: float64(t.rows) * t.cellH}
}

func mouseButton(b ggui.MouseButton) input.MouseButton {
	switch b {
	case ggui.MouseButtonRight:
		return input.MouseButtonRight
	case ggui.MouseButtonMiddle:
		return input.MouseButtonMiddle
	}
	return input.MouseButtonLeft
}

// Grid painting -----------------------------------------------------------------

// grid is the geometry and style one paint draws a screen with.
type grid struct {
	origin       ggui.Point
	cellW, cellH float64
	size         float64
	font, italic *ggui.Font // italic is nil where there is none
	pal          palette
	scale        float64 // device pixels per logical pixel
	runBuf       *[]byte
}

// cellWidth is a cell as wide as the font's advance, rounded to whole device
// pixels as Ghostty does, so that every column starts on a pixel and fills
// in neighbouring cells meet. Glyphs are placed on the cells, not at the
// font's own advance, so a row does not drift from the grid.
func cellWidth(advance, scale float64) float64 {
	if advance <= 0 || scale <= 0 {
		return advance
	}
	return max(math.Round(advance*scale), 1) / scale
}

// cellRect is the rect of width cells at (col, row), its edges on device
// pixels. A cell's right edge is its neighbour's left, rounded the same
// way, so fills in adjacent cells meet without a seam or an overlap.
func (g grid) cellRect(col, row, width int) ggui.Rect {
	x0, x1 := g.snap(g.origin.X+float64(col)*g.cellW), g.snap(g.origin.X+float64(col+width)*g.cellW)
	y0, y1 := g.snap(g.origin.Y+float64(row)*g.cellH), g.snap(g.origin.Y+float64(row+1)*g.cellH)
	return ggui.Rect{Origin: ggui.Pt(x0, y0), Size: ggui.Sz(x1-x0, y1-y0)}
}

func (g grid) snap(v float64) float64 { return snapTo(g.scale, v) }

// textTop is how far below a cell's top its text is drawn, centring the
// font's line in the cell.
func (g grid) textTop() float64 { return (g.cellH - g.size*1.2) / 2 }

func (g grid) paint(dst *ggui.Canvas, f frameSnapshot) {
	for row := 0; row < f.rows; row++ {
		base := row * f.cols
		if base+f.cols > len(f.cells) {
			break
		}
		line := f.cells[base : base+f.cols]

		// Backgrounds in runs: a row is usually one colour.
		for start := 0; start < f.cols; {
			bg, def := g.background(line[start], f.colors)
			end := start + 1
			for end < f.cols {
				if b, _ := g.background(line[end], f.colors); b != bg {
					break
				}
				end++
			}
			if !def {
				dst.FillRect(g.cellRect(start, row, end-start), bg)
			}
			start = end
		}

		// Glyphs in runs of one style. A run breaks at anything that is not
		// plain ASCII, which is drawn in its own cell so a wide character or
		// a fallback font's advance cannot push the rest of the row off grid.
		for start := 0; start < f.cols; {
			cell := line[start]
			if skipGlyph(cell) {
				start++
				continue
			}
			fg := g.foreground(cell, f.colors)
			rect := g.cellRect(start, row, 1)
			if f.clusters[base+start] != "" || cell.Codepoint > 0x7e {
				g.cellGlyph(dst, cell, f.textAt(base+start), rect, fg)
				g.decorate(dst, cell, start, row, fg)
				start++
				continue
			}
			end := start + 1
			for end < f.cols && sameRun(cell, line[end], f.clusters[base+end], fg, g.foreground(line[end], f.colors)) {
				end++
			}
			b := (*g.runBuf)[:0]
			for _, c := range line[start:end] {
				if c.Codepoint <= ' ' || c.Flags.Invisible {
					b = append(b, ' ')
				} else {
					b = append(b, byte(c.Codepoint)) // the run is ASCII
				}
			}
			*g.runBuf = b
			g.glyph(dst, string(b), rect.Origin, fg, cell.Flags.Bold, cell.Flags.Italic)
			for i := start; i < end; i++ {
				g.decorate(dst, line[i], i, row, fg)
			}
			start = end
		}
	}
}

// sameRun reports whether next can be drawn in the same call as the run
// that cell started: plain ASCII, or a blank, in the same style.
func sameRun(cell, next gostty.RenderCell, cluster string, fg, nextFg color.RGBA) bool {
	if next.Codepoint > 0x7e || cluster != "" || next.Flags.Invisible {
		return false
	}
	if next.Codepoint <= ' ' {
		return true // a blank draws nothing, whatever its colour
	}
	return next.Flags.Bold == cell.Flags.Bold && next.Flags.Italic == cell.Flags.Italic && nextFg == fg &&
		next.Flags.Underline == cell.Flags.Underline && next.Flags.Strikethrough == cell.Flags.Strikethrough
}

// background is a cell's fill, and whether it is the default the widget
// already painted. A selected cell takes the selection colour.
func (g grid) background(c gostty.RenderCell, colors gostty.RenderColors) (color.RGBA, bool) {
	if c.Flags.Selected {
		return g.pal.Selection, false
	}
	if c.Bg == colors.Background {
		return g.pal.Surface0, true
	}
	return color.RGBA{R: c.Bg.R(), G: c.Bg.G(), B: c.Bg.B(), A: 0xff}, false
}

func (g grid) foreground(c gostty.RenderCell, colors gostty.RenderColors) color.RGBA {
	fg := g.pal.Fg
	if c.Fg != colors.Foreground {
		fg = color.RGBA{R: c.Fg.R(), G: c.Fg.G(), B: c.Fg.B(), A: 0xff}
	}
	if c.Flags.Faint {
		bg := g.pal.Surface0
		fg = color.RGBA{R: uint8((int(fg.R) + int(bg.R)) / 2), G: uint8((int(fg.G) + int(bg.G)) / 2), B: uint8((int(fg.B) + int(bg.B)) / 2), A: 0xff}
	}
	return fg
}

// cellGlyph draws one cell's character: as shapes if it is a box or block
// character, else as text.
func (g grid) cellGlyph(dst *ggui.Canvas, c gostty.RenderCell, text string, rect ggui.Rect, fg color.RGBA) {
	if !drawBoxChar(dst, c.Codepoint, rect, fg) {
		g.glyph(dst, text, rect.Origin, fg, c.Flags.Bold, c.Flags.Italic)
	}
}

// glyph draws text from a cell's top-left in the mono face, a grapheme to a
// cell: bold along its weight axis, italic in the platform's italic mono
// where there is one.
func (g grid) glyph(dst *ggui.Canvas, s string, at ggui.Point, fg color.RGBA, bold, italic bool) {
	font := g.font
	if italic && g.italic != nil {
		font = g.italic
	}
	st := ggui.TextStyle{Font: font, Size: g.size, Color: fg}
	if bold {
		st.Weight = ggui.WeightBold
	}
	dst.DrawTextCells(s, st, ggui.Pt(at.X, at.Y+g.textTop()), g.cellW)
}

func (g grid) decorate(dst *ggui.Canvas, c gostty.RenderCell, col, row int, fg color.RGBA) {
	width := 1
	if c.Flags.Wide == gostty.CellWidthWide {
		width = 2
	}
	r := g.cellRect(col, row, width)
	if c.Flags.Underline != gostty.UnderlineNone {
		dst.FillRect(ggui.Rect{Origin: ggui.Pt(r.Origin.X, r.Origin.Y+r.Size.H-3), Size: ggui.Sz(r.Size.W, 1)}, fg)
	}
	if c.Flags.Strikethrough {
		dst.FillRect(ggui.Rect{Origin: ggui.Pt(r.Origin.X, r.Origin.Y+r.Size.H/2), Size: ggui.Sz(r.Size.W, 1)}, fg)
	}
}

// paintPreedit shows the IME's composition at the cursor: on a raised
// surface, underlined, so it reads as not yet sent.
func (g grid) paintPreedit(dst *ggui.Canvas, at ggui.Point, text string) {
	x := at.X
	for _, r := range text {
		w := g.cellW
		if n, err := gostty.CodepointWidth(r); err == nil && n == 2 {
			w *= 2
		}
		dst.FillRect(ggui.Rect{Origin: ggui.Pt(x, at.Y), Size: ggui.Sz(w, g.cellH)}, g.pal.Surface2)
		dst.DrawTextStyle(string(r), ggui.TextStyle{Font: g.font, Size: g.size, Color: g.pal.Fg}, ggui.Pt(x, at.Y+g.textTop()))
		dst.FillRect(ggui.Rect{Origin: ggui.Pt(x, at.Y+g.cellH-2), Size: ggui.Sz(w, 1)}, g.pal.Fg)
		x += w
	}
}

// skipGlyph reports a cell with nothing to draw: a blank, the spacer behind
// a wide character, or hidden text.
func skipGlyph(c gostty.RenderCell) bool {
	return c.Codepoint <= ' ' || c.Flags.Invisible ||
		c.Flags.Wide == gostty.CellWidthSpacerTail || c.Flags.Wide == gostty.CellWidthSpacerHead
}

func strokeRect(dst *ggui.Canvas, r ggui.Rect, c color.Color) {
	x, y, w, h := r.Origin.X, r.Origin.Y, r.Size.W, r.Size.H
	dst.FillRect(ggui.Rect{Origin: ggui.Pt(x, y), Size: ggui.Sz(w, 1)}, c)
	dst.FillRect(ggui.Rect{Origin: ggui.Pt(x, y+h-1), Size: ggui.Sz(w, 1)}, c)
	dst.FillRect(ggui.Rect{Origin: ggui.Pt(x, y), Size: ggui.Sz(1, h)}, c)
	dst.FillRect(ggui.Rect{Origin: ggui.Pt(x+w-1, y), Size: ggui.Sz(1, h)}, c)
}
