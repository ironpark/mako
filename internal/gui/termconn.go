package gui

import (
	"context"
	"errors"
	"io"
	"math"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/gostty"
	"github.com/ironpark/gostty/input"

	"github.com/ironpark/mako/internal/activity"
	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/protocol"
)

// termConn is one attached session as the GUI sees it. The daemon streams
// raw PTY bytes; a terminal here parses them again, so the screen is drawn
// and input encoded locally, from the same modes the program set.
//
// The daemon's own terminal answers the program's queries, so replies from
// this one are discarded rather than written back a second time.
type termConn struct {
	id  string
	att sessionLink

	mu           sync.Mutex // guards everything below, the terminal included
	vt           *gostty.Session
	state        *gostty.RenderState
	cells        []gostty.RenderCell
	clusters     []string
	dirtyRows    []uint16
	hasFrame     bool // cells and clusters hold a complete screen
	title        string
	titleKnown   bool
	titleChanged func() // immutable; posts UI work without acquiring mu
	exited       bool
	exitCode     int
	err          error
	cols         uint16
	rows         uint16
	closed       bool // vt and state are released; nothing may touch them
	opts         termOptions
	gesture      *gostty.Gesture // click counting and drag selection
	dragging     bool
	search       *gostty.Search // the find bar's, for searched, while it is open
	searched     string

	// The daemon is told of a size once a burst of them settles, as a
	// sliding panel resizes the terminal every frame, so the program is
	// not made to redraw for each one. sentAt is when it was last told;
	// settle sends the latest size later.
	sentAt time.Time
	settle *time.Timer

	// wake asks the UI for a frame. Output arrives in bursts, so requests
	// are coalesced until the frame that drew them has run.
	wake    func()
	pending atomic.Bool
	// repaint, once the window gives one, asks for the terminal's region
	// alone to be painted again; paintedAt is where a view last painted
	// it, nil when that could not be told.
	repaint   atomic.Pointer[func(ggui.Rect)]
	paintedAt atomic.Pointer[ggui.Rect]

	progress activity.Progress // what the program says of its work
	copied   *string           // what the program last put on the clipboard, until the view takes it
}

// dialTerm attaches to a session and starts reading its stream.
func dialTerm(ctx context.Context, c *client.Client, info *protocol.SessionInfo, wake func(), titleChanged func()) (*termConn, error) {
	att, err := c.AttachStream(ctx, &protocol.Attach{SessionId: info.GetId(), NodeId: info.GetNodeId(), Replay: true})
	if err != nil {
		return nil, err
	}
	t, err := newTermConn(info.GetId(), att, uint16(info.GetCols()), uint16(info.GetRows()), wake, titleChanged)
	if err != nil {
		_ = att.Close()
		return nil, err
	}
	return t, nil
}

// newTermConn starts reading an attached link into a fresh terminal.
func newTermConn(id string, link sessionLink, cols, rows uint16, wake func(), titleChanged ...func()) (*termConn, error) {
	cols, rows = max(cols, 2), max(rows, 2)
	vt, err := gostty.New(cols, rows,
		gostty.WithTerminalOptions(gostty.WithScrollbackMaxLines(10000)),
		gostty.WithStreamOptions(gostty.WithVersionReport("mako", "gui")),
	)
	if err != nil {
		return nil, err
	}
	state, err := gostty.NewRenderState()
	if err != nil {
		vt.Close()
		return nil, err
	}
	gesture := vt.Terminal().NewGesture()
	vt.AddGesture(gesture) // closed with the terminal, and before it
	_ = gesture.SetWordBoundaries(wordBoundaries)
	// One click places the anchor a drag grows from, two select a word,
	// three the line.
	gesture.SetBehaviors(gostty.GestureBehaviorCell, gostty.GestureBehaviorWord, gostty.GestureBehaviorLine)
	t := &termConn{id: id, att: link, vt: vt, state: state, gesture: gesture, cols: cols, rows: rows, wake: wake}
	if len(titleChanged) > 0 {
		t.titleChanged = titleChanged[0]
	}
	// A program that selects text itself, as claude does with the mouse,
	// copies it with OSC 52. The request is served on the UI thread, where
	// the clipboard is; one asking to read the clipboard is refused.
	vt.Stream().OnClipboardWriteRequest(func(req *gostty.ClipboardRequest) {
		if text, ok := clipboardText(req); ok {
			t.copied = &text // under mu, which feeding holds
		}
		_ = req.Allow(false)
	})
	t.mu.Lock()
	t.applyOptionsLocked(defaultTermOptions())
	t.mu.Unlock()
	go t.read()
	return t, nil
}

// termOptions are the settings a terminal takes from the window: its
// colours, how much it remembers, and the cursor shown until a program asks
// for another.
type termOptions struct {
	palette     palette
	scrollback  int
	cursorStyle gostty.CursorStyle
	cursorBlink bool
}

func defaultTermOptions() termOptions {
	return termOptionsFor(defaultPrefs(), darkPalette)
}

func termOptionsFor(p prefs, pal palette) termOptions {
	style := gostty.CursorStyleBlock
	switch p.CursorStyle {
	case "bar":
		style = gostty.CursorStyleBar
	case "underline":
		style = gostty.CursorStyleUnderline
	}
	return termOptions{palette: pal, scrollback: p.Scrollback, cursorStyle: style, cursorBlink: p.CursorBlink}
}

// configure applies options that changed since the last call. It runs on
// every paint, so it is cheap when nothing did.
func (t *termConn) configure(o termOptions) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || o == t.opts {
		return
	}
	t.applyOptionsLocked(o)
}

func (t *termConn) applyOptionsLocked(o termOptions) {
	vt := t.vt.Terminal()
	if o.palette != t.opts.palette {
		applyPalette(vt, o.palette)
		// A cell keeps the palette index it was written with, but nothing on
		// screen changed, so the old render state would hand back the colours
		// it resolved before. A fresh one resolves them again.
		if fresh, err := gostty.NewRenderState(); err == nil {
			_ = t.state.Close()
			t.state = fresh
			t.hasFrame = false
		}
	}
	vt.SetScrollbackMaxLines(uint(o.scrollback))
	vt.SetDefaultCursorStyle(o.cursorStyle)
	vt.SetDefaultCursorBlink(o.cursorBlink)
	t.opts = o
}

// wordBoundaries end a word for double-click selection.
var wordBoundaries = []rune{
	0, ' ', '\t', '\'', '"', '`', '|', ':', ';', ',',
	'(', ')', '[', ']', '{', '}', '<', '>', '$', '│',
}

// read feeds the stream into the terminal until the session ends or the
// attachment is closed.
func (t *termConn) read() {
	defer t.requestPaint()
	for {
		ev, err := t.att.Recv()
		if err != nil {
			t.mu.Lock()
			if !t.exited && t.err == nil {
				t.err = err
			}
			t.mu.Unlock()
			return
		}
		switch ev.Kind {
		case protocol.EventSnapshot:
			snap := ev.Snapshot
			t.mu.Lock()
			if t.closed {
				t.mu.Unlock()
				return
			}
			// A snapshot replaces whatever this terminal held; it is the
			// daemon's screen at the moment of attach. The screen text
			// follows as output frames.
			t.vt.Terminal().FullReset()
			opts := t.opts
			t.opts = termOptions{}
			t.applyOptionsLocked(opts)
			cols, rows := uint16(max(snap.GetCols(), 2)), uint16(max(snap.GetRows(), 2))
			_ = t.vt.Terminal().Resize(cols, rows)
			t.cols, t.rows = cols, rows
			t.setTitleLocked(snap.GetTitle())
			t.progress.Restore(snap.GetProgress(), time.Now())
			t.hasFrame = false
			t.mu.Unlock()
			t.wake()
		case protocol.EventOutput:
			t.mu.Lock()
			t.feedLocked(ev.Data)
			t.mu.Unlock()
		case protocol.EventExited:
			t.mu.Lock()
			t.exited, t.exitCode = true, ev.ExitCode
			t.mu.Unlock()
			return
		case protocol.EventError:
			t.mu.Lock()
			t.err = errors.New(ev.Err)
			t.mu.Unlock()
			return
		}
		t.requestPaint()
	}
}

func (t *termConn) feedLocked(p []byte) {
	if t.closed {
		return
	}
	_, _ = t.vt.Write(p)
	stream := t.vt.Stream()
	_ = stream.WriteReplies(io.Discard)
	now := time.Now()
	for ev, err := range stream.EventValues() {
		if err != nil {
			break
		}
		if ev.Kind == gostty.StreamEventTitleChanged {
			t.setTitleLocked(ev.Title)
		}
		t.progress.Report(ev, now)
		if ev.Kind == gostty.StreamEventProgressReport {
			// Hidden terminals may have an outstanding paint forever.
			t.wake()
		}
	}
}

// Title notifications are independent of paint coalescing: hidden tabs may
// never paint but must still update their labels.
func (t *termConn) setTitleLocked(title string) {
	changed := !t.titleKnown || t.title != title
	t.title, t.titleKnown = title, true
	if changed && t.titleChanged != nil {
		t.titleChanged()
	}
}

// requestPaint asks for the terminal to be painted again: where a view
// last painted it, and only there, when the window can do that; else the
// window whole.
func (t *termConn) requestPaint() {
	if !t.pending.CompareAndSwap(false, true) {
		return
	}
	if repaint, at := t.repaint.Load(), t.paintedAt.Load(); repaint != nil && at != nil {
		(*repaint)(*at)
		return
	}
	t.wake()
}

// painted notes where a view painted the terminal, in the window, or that
// it cannot be told.
func (t *termConn) painted(at ggui.Rect, known bool) {
	switch old := t.paintedAt.Load(); {
	case !known:
		t.paintedAt.Store(nil)
	case old == nil || *old != at:
		t.paintedAt.Store(&at)
	}
}

// frame copies the screen for one paint. It runs on the UI goroutine.
func (t *termConn) frame() (frameSnapshot, error) {
	t.pending.Store(false)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return frameSnapshot{}, errors.New("detached")
	}
	if err := t.state.Update(t.vt.Terminal()); err != nil {
		return frameSnapshot{}, err
	}
	cols, rows := int(t.state.Cols()), int(t.state.Rows())
	n := cols * rows
	if len(t.cells) != n {
		t.cells = slices.Grow(t.cells[:0], n)[:n]
		t.clusters = slices.Grow(t.clusters[:0], n)[:n]
		t.hasFrame = false
	}

	// Only what changed since the last paint is copied out of the native
	// state: a frame drawn for a cursor blink or an animation need not touch
	// it at all.
	dirty := t.state.Dirty()
	full := !t.hasFrame || dirty == gostty.RenderDirtyFull
	if !full && dirty == gostty.RenderDirtyPartial {
		if cap(t.dirtyRows) < rows {
			t.dirtyRows = make([]uint16, rows)
		}
		dst := t.dirtyRows[:rows]
		count, err := t.state.DirtyRows(dst)
		if err != nil {
			return frameSnapshot{}, err
		}
		changed := int(count)
		// Copying the whole screen is cheaper than many row copies.
		full = changed*2 >= rows
		if !full {
			for _, y := range dst[:changed] {
				if int(y) >= rows {
					continue
				}
				row := t.cells[int(y)*cols : (int(y)+1)*cols]
				if _, err := t.state.RowCells(y, row); err != nil {
					return frameSnapshot{}, err
				}
				t.refreshClustersLocked(int(y), int(y)+1, cols)
			}
		}
	}
	if full {
		if _, err := t.state.Cells(t.cells); err != nil {
			return frameSnapshot{}, err
		}
		t.refreshClustersLocked(0, rows, cols)
	}
	t.hasFrame = true
	t.state.Clean()
	colors, cursor := t.state.Colors(), t.state.Cursor()
	return frameSnapshot{
		cells:    t.cells,
		clusters: t.clusters,
		cols:     cols,
		rows:     rows,
		colors:   colors,
		cursor:   cursor,
		exited:   t.exited,
		exitCode: t.exitCode,
		err:      t.err,
	}, nil
}

// refreshClustersLocked rebuilds the grapheme clusters of rows [y0, y1) from
// the cells already copied for them.
func (t *termConn) refreshClustersLocked(y0, y1, cols int) {
	if cols <= 0 {
		return
	}
	var buf [32]rune
	for y := y0; y < y1; y++ {
		base := y * cols
		for x := 0; x < cols; x++ {
			i := base + x
			cell := t.cells[i]
			t.clusters[i] = ""
			if !cell.Flags.HasGrapheme || cell.Codepoint <= ' ' {
				continue
			}
			if k, err := t.state.Graphemes(uint16(x), uint16(y), buf[:]); err == nil && k > 1 {
				t.clusters[i] = string(buf[:min(k, uint(len(buf)))])
			}
		}
	}
}

// resize follows the widget's size: the local terminal at once, the
// session PTY by a message to the daemon.
func (t *termConn) resize(cols, rows uint16) {
	cols, rows = max(cols, 2), max(rows, 2)
	t.mu.Lock()
	if t.closed || (cols == t.cols && rows == t.rows) {
		t.mu.Unlock()
		return
	}
	t.cols, t.rows = cols, rows
	_ = t.vt.Terminal().Resize(cols, rows)
	live := !t.exited && t.err == nil
	now := time.Now()
	send := live && t.settle == nil && now.Sub(t.sentAt) >= resizeSettle
	if send {
		t.sentAt = now
	} else if live && t.settle == nil {
		t.settle = time.AfterFunc(resizeSettle, t.sendSize)
	}
	t.mu.Unlock()
	if send {
		_ = t.att.Resize(cols, rows)
	}
}

// resizeSettle is how long the size must hold before the daemon hears of
// it again.
const resizeSettle = 120 * time.Millisecond

// sendSize tells the daemon the size the terminal settled on.
func (t *termConn) sendSize() {
	t.mu.Lock()
	t.settle = nil
	t.sentAt = time.Now()
	cols, rows := t.cols, t.rows
	live := !t.closed && !t.exited && t.err == nil
	t.mu.Unlock()
	if live {
		_ = t.att.Resize(cols, rows)
	}
}

// key encodes one key event with the terminal's current modes and sends it.
func (t *termConn) key(ev input.KeyEvent, text string) {
	var buf frameBuffer
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	err := input.EncodeKey(&buf, t.vt.Terminal(), ev, text)
	if err == nil && len(buf) > 0 {
		// Typing snaps the viewport back to the bottom, as every terminal does.
		t.vt.Terminal().ScrollViewport(gostty.ScrollViewportBottom())
	}
	t.mu.Unlock()
	if err == nil && len(buf) > 0 {
		t.send(buf)
	}
}

// runsLines reports whether text pasted would run line by line, as if
// typed: it breaks lines, and the program did not ask for bracketed
// pastes, which a shell holds until Enter.
func (t *termConn) runsLines(text string) bool {
	if !strings.ContainsAny(text, "\r\n") {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return !t.closed && !t.vt.Terminal().ModeEnabled(gostty.ModeBracketedPaste)
}

// paste sends text the way the program asked for it: bracketed when it
// enabled bracketed paste, plain otherwise, its line breaks and all, as a
// paste from the clipboard or the composer's lines. Only a paste's own end
// marker, which would let the rest run as if typed, is refused.
func (t *termConn) paste(text string) {
	if text == "" || strings.Contains(text, "\x1b[201~") {
		return
	}
	var buf frameBuffer
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	err := input.EncodePaste(&buf, t.vt.Terminal(), []byte(text))
	t.mu.Unlock()
	if err == nil {
		t.send(buf)
	}
}

// sendText writes text followed by Enter, as the composer does, its line
// breaks in it.
func (t *termConn) sendText(text string) {
	t.paste(text)
	t.key(input.KeyEvent{Action: input.KeyActionPress, Key: input.KeyEnter}, "")
}

// wheel scrolls the scrollback, or on the alternate screen, where there is
// none, sends the arrows a pager understands.
func (t *termConn) wheel(lines int) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	alt := t.vt.Terminal().ActiveScreenKey() == gostty.ScreenKeyAlternate
	if !alt {
		t.vt.Terminal().ScrollViewport(gostty.ScrollViewportDelta(-lines))
	}
	t.mu.Unlock()
	if !alt {
		t.requestPaint()
		return
	}
	key := input.KeyArrowUp
	if lines < 0 {
		key, lines = input.KeyArrowDown, -lines
	}
	for range lines {
		t.key(input.KeyEvent{Action: input.KeyActionPress, Key: key}, "")
	}
}

func (t *termConn) send(p []byte) {
	if err := t.att.Input(p); err != nil {
		t.mu.Lock()
		if t.err == nil && !t.exited {
			t.err = err
		}
		t.mu.Unlock()
		t.requestPaint()
	}
}

// Selection -----------------------------------------------------------------
//
// The gesture counts clicks, applies the granularity, and works out what a
// drag covers across soft wraps, wide characters and the scrollback; this
// only feeds it the pointer and puts what it hands back on the screen.

// Double-click policy: how close in time and space two presses have to be.
const (
	multiClickInterval = 400 * time.Millisecond
	multiClickCells    = 2
)

// pointerCell is a pointer position in a terminal: the cell under it and
// the pixel position within the grid, which the gesture needs at the edges.
type pointerCell struct {
	col, row   int
	xpos, ypos float64 // xpos in wholeCells units
	cellW      float64 // whole pixels
	cols       int
	height     float64
}

// wholeCells maps x, from the grid's left edge, into cells of a whole
// width, which is what libghostty measures in: a cell is whole in device
// pixels, so on a display of two per point it may be 8.5 points wide.
// Where the pointer sits within a cell, which decides whether a drag takes
// it, survives the rounding.
func wholeCells(x, cellW float64) (xpos, width float64) {
	width = max(math.Round(cellW), 1)
	if cellW <= 0 {
		return x, width
	}
	return x / cellW * width, width
}

func (t *termConn) selectPress(p pointerCell) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	t.gesture.SetGeometry(gostty.GestureGeometry{
		Columns: uint32(p.cols), CellWidth: uint32(p.cellW), ScreenHeight: uint32(p.height),
	})
	sel, ok, err := t.gesture.Press(gostty.GesturePressEvent{
		X: uint16(p.col), Y: uint16(p.row), Xpos: p.xpos, Ypos: p.ypos,
		MaxDistance:      multiClickCells * p.cellW,
		RepeatIntervalNs: uint64(multiClickInterval),
		TimeNs:           time.Now().UnixNano(),
	})
	if err == nil {
		t.dragging = true
		t.applySelectionLocked(sel, ok)
	}
}

// selectDrag grows the selection to the pointer; alt selects the block
// between the corners rather than the flow of text.
func (t *termConn) selectDrag(p pointerCell, rectangle bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || !t.dragging {
		return
	}
	ev := gostty.GestureDragEvent{X: uint16(p.col), Y: uint16(p.row), Xpos: p.xpos, Ypos: p.ypos, Rectangle: rectangle}
	t.applySelectionLocked(t.gesture.Drag(ev))
	// Held past the top or bottom, the view scrolls and the selection grows
	// with it, a row a frame.
	if dir := t.gesture.Autoscroll(); dir != gostty.GestureAutoscrollDirectionNone {
		delta := 1
		if dir == gostty.GestureAutoscrollDirectionUp {
			delta = -1
		}
		t.vt.Terminal().ScrollViewport(gostty.ScrollViewportDelta(delta))
		t.applySelectionLocked(t.gesture.AutoscrollTick(ev))
	}
}

// selectRelease ends a drag and reports the selected text, for
// copy-on-select.
func (t *termConn) selectRelease(p pointerCell) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || !t.dragging {
		return ""
	}
	t.dragging = false
	t.gesture.Release(uint16(p.col), uint16(p.row))
	return t.selectionLocked()
}

func (t *termConn) applySelectionLocked(sel gostty.Selection, ok bool) {
	screen := t.vt.Terminal().ActiveScreen()
	if !ok {
		screen.ClearSelection()
		return
	}
	_, _ = screen.SetSelection(sel)
}

// selection is the selected text, or "" when nothing is selected.
func (t *termConn) selection() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return ""
	}
	return t.selectionLocked()
}

func (t *termConn) selectionLocked() string {
	text, ok, err := t.vt.Terminal().ActiveScreen().SelectionString()
	if err != nil || !ok {
		return ""
	}
	return text
}

func (t *termConn) clearSelection() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	t.vt.Terminal().ActiveScreen().ClearSelection()
	t.gesture.Reset()
	t.dragging = false
}

// selectAll selects the whole scrollback, not only what is on screen.
func (t *termConn) selectAll() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	_, _ = t.vt.Terminal().ActiveScreen().SelectAll()
}

// Find --------------------------------------------------------------------------

// find selects the match of needle in the scrollback before the one
// selected, further up, or the one after with back, wrapping at the ends,
// and scrolls it into view: the selection draws it. It reports which match that is, from 1, and how many
// there are; at is 0 when there are none.
func (t *termConn) find(needle string, back bool) (at, count int) {
	defer t.requestPaint()
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return 0, 0
	}
	if t.search == nil || t.searched != needle {
		t.endFindLocked()
		if needle == "" {
			t.vt.Terminal().ActiveScreen().ClearSelection()
			return 0, 0
		}
		s, err := t.vt.Terminal().NewSearch(needle)
		if err != nil {
			return 0, 0
		}
		t.search, t.searched = s, needle
	}
	// The whole scrollback, which mako keeps to Scrollback lines.
	t.search.All()
	dir := gostty.SearchDirectionNext
	if back {
		dir = gostty.SearchDirectionPrev
	}
	ok, err := t.search.Select(dir, gostty.SearchScrollIfNeeded)
	count = int(t.search.MatchCount())
	if err != nil || !ok {
		return 0, count
	}
	// The search keeps its match apart from the screen's selection; made
	// the selection, the match is drawn as one, and ⌘C copies it.
	if sel, ok := t.search.SelectedMatch(); ok {
		t.applySelectionLocked(sel, true)
	}
	i, _ := t.search.SelectedIndex()
	return int(i) + 1, count
}

// endFind closes the find, and the match it selected.
func (t *termConn) endFind() {
	defer t.requestPaint()
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.search == nil {
		return
	}
	t.endFindLocked()
	t.vt.Terminal().ActiveScreen().ClearSelection()
}

func (t *termConn) endFindLocked() {
	if t.search != nil {
		_ = t.search.Close()
		t.search, t.searched = nil, ""
	}
}

// Mouse reporting -------------------------------------------------------------

// mouseReported reports whether the program asked for the mouse (DECSET
// 1000 and friends), in which case presses, drags and the wheel are its.
func (t *termConn) mouseReported() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return false
	}
	return t.vt.Terminal().MouseTracking() != gostty.MouseTrackingNone
}

// mouse encodes one mouse event under the program's modes and sends it,
// reporting whether the modes produced anything.
func (t *termConn) mouse(ev input.MouseEvent, size input.RenderSize, anyPressed bool) bool {
	var buf frameBuffer
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return false
	}
	err := input.EncodeMouse(&buf, t.vt.Terminal(), ev, size, anyPressed)
	t.mu.Unlock()
	if err != nil || len(buf) == 0 {
		return false
	}
	t.send(buf)
	return true
}

// clipboardText is the plain text of a clipboard write, the standard
// clipboard's only.
func clipboardText(req *gostty.ClipboardRequest) (string, bool) {
	if loc, err := req.Location(); err != nil || loc != gostty.ClipboardLocationStandard {
		return "", false
	}
	n, err := req.ContentCount()
	if err != nil {
		return "", false
	}
	for i := range n {
		mime, err := req.ContentMime(i)
		if err != nil || !strings.HasPrefix(mime, "text/plain") {
			continue
		}
		if data, err := req.ContentData(i); err == nil {
			return string(data), true
		}
	}
	return "", false
}

// takeCopied hands over what the program put on the clipboard since the
// last call.
func (t *termConn) takeCopied() (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.copied == nil {
		return "", false
	}
	text := *t.copied
	t.copied = nil
	return text, true
}

// busy reports whether the program says it is at work.
func (t *termConn) busy() bool {
	busy, _ := t.busyState()
	return busy
}

// A received clear/error/pause overrides old polling data just as a busy
// report does. A disconnected link falls back to the daemon's metadata.
func (t *termConn) busyState() (bool, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.exited {
		return false, true
	}
	if t.closed || t.err != nil {
		return false, false
	}
	return t.progress.Busy(time.Now()), t.progress.Known()
}

func (t *termConn) windowTitle() (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.title, t.titleKnown
}

// close detaches and releases the terminal. The session keeps running.
func (t *termConn) close() {
	_ = t.att.Close()
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	t.closed = true
	if t.settle != nil {
		t.settle.Stop()
	}
	t.endFindLocked()
	_ = t.state.Close()
	_ = t.vt.Close()
}

// sessionLink is the daemon side of a termConn: a client.Attachment, or a
// recorder in tests.
type sessionLink interface {
	Recv() (*protocol.StreamEvent, error)
	Input(p []byte) error
	Resize(cols, rows uint16) error
	Close() error
}

// frameSnapshot is what one paint reads. Its slices are reused by the next
// frame, so they are valid only during the paint that asked for them.
type frameSnapshot struct {
	cells      []gostty.RenderCell
	clusters   []string
	cols, rows int
	colors     gostty.RenderColors
	cursor     gostty.RenderCursor
	exited     bool
	exitCode   int
	err        error
}

// frameBuffer collects encoder output for one write.
type frameBuffer []byte

func (b *frameBuffer) Write(p []byte) (int, error) { *b = append(*b, p...); return len(p), nil }

// textAt is a cell's text: its grapheme cluster when it has one, otherwise
// its codepoint.
func (f frameSnapshot) textAt(i int) string {
	if i < 0 || i >= len(f.cells) {
		return ""
	}
	if s := f.clusters[i]; s != "" {
		return s
	}
	return string(f.cells[i].Codepoint)
}
