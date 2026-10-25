package gui

import (
	"bytes"
	"encoding/base64"
	"io"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ironpark/ggui"

	"github.com/ironpark/sanbo/internal/protocol"
)

// fakeLink stands in for a daemon attachment: frames pushed into in are
// received, input and resizes are recorded.
type fakeLink struct {
	in chan *protocol.Envelope

	mu      sync.Mutex
	input   bytes.Buffer
	resizes [][2]uint16
}

func newFakeLink() *fakeLink { return &fakeLink{in: make(chan *protocol.Envelope, 16)} }

func (f *fakeLink) Recv() (*protocol.Envelope, error) {
	env, ok := <-f.in
	if !ok {
		return nil, io.EOF
	}
	return env, nil
}

func (f *fakeLink) Input(p []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.input.Write(p)
	return nil
}

func (f *fakeLink) Resize(cols, rows uint16) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resizes = append(f.resizes, [2]uint16{cols, rows})
	return nil
}

func (f *fakeLink) Close() error { return nil }

func (f *fakeLink) typed() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.input.String()
	f.input.Reset()
	return s
}

func (f *fakeLink) output(t *testing.T, s string) {
	t.Helper()
	env, err := protocol.Encode(protocol.TypeOutput, "", protocol.Output{Data: []byte(s)})
	if err != nil {
		t.Fatal(err)
	}
	f.in <- env
}

func newTestTerminal(t *testing.T) (*fakeLink, *termConn, *ggui.Probe) {
	t.Helper()
	link := newFakeLink()
	conn, err := newTermConn("s1", link, 80, 24, func() {})
	if err != nil {
		t.Fatal(err)
	}
	p := ggui.NewProbe(ggui.Column(
		otherTarget(), // a second focus target, which Tab must not reach
		ggui.Expanded(newTerminalView(conn, defaultPrefs, false)),
	).Stretch(), ggui.Sz(800, 500))
	t.Cleanup(func() {
		p.Close()
		close(link.in)
		conn.close()
	})
	p.Click(ggui.Pt(400, 300))
	return link, conn, p
}

// otherTarget is a focusable widget above the terminal.
func otherTarget() ggui.Widget {
	return ggui.Focus(ggui.Box().Size(800, 40))
}

func TestTypingReachesThePTY(t *testing.T) {
	link, _, p := newTestTerminal(t)

	p.Text("ls")
	p.Type(ggui.Mods{}, ggui.KeyTab)
	p.Type(ggui.Mods{}, ggui.KeyEnter)
	if got := link.typed(); got != "ls\t\r" {
		t.Fatalf("typed %q, want %q", got, "ls\t\r")
	}
	if !p.Focused() {
		t.Fatal("Tab moved focus off the terminal")
	}

	// A printable key is sent once, as its text; the KeyPress that comes
	// with it is not described again.
	p.Type(ggui.Mods{}, ggui.KeyA)
	p.Text("a")
	if got := link.typed(); got != "a" {
		t.Fatalf("typed %q, want %q", got, "a")
	}

	p.Type(ggui.Mods{Ctrl: true}, ggui.KeyC)
	if got := link.typed(); got != "\x03" {
		t.Fatalf("Ctrl+C sent %q, want ETX", got)
	}

	p.Type(ggui.Mods{}, ggui.KeyArrowUp)
	if got := link.typed(); got != "\x1b[A" {
		t.Fatalf("Up sent %q", got)
	}

	// Where the IME may rewrite the last syllable, it is held until the
	// next key, which sends it first.
	p.Text("안녕")
	want, held := "안녕", ""
	if holdsSyllables {
		want, held = "안", "녕"
	}
	if got := link.typed(); got != want {
		t.Fatalf("Hangul sent %q, want %q", got, want)
	}

	p.Type(ggui.Mods{}, ggui.KeyEscape)
	if got := link.typed(); got != held+"\x1b" {
		t.Fatalf("Escape sent %q", got)
	}
}

// The encoder follows the modes the program set: application cursor keys
// change what the arrows send.
func TestKeysFollowTerminalModes(t *testing.T) {
	link, _, p := newTestTerminal(t)
	link.output(t, "\x1b[?1h") // DECCKM
	waitFor(t, func() bool {
		p.Type(ggui.Mods{}, ggui.KeyArrowUp)
		return link.typed() == "\x1bOA"
	})
}

func TestPasteIsBracketedWhenAsked(t *testing.T) {
	link, _, p := newTestTerminal(t)
	// Inside a frame the clipboard is the probe's own, not the system's.
	p.Post(func() { ggui.CurrentClipboard().Write("echo hi") })
	p.Frame()
	link.output(t, "\x1b[?2004h")
	mods := ggui.Mods{Meta: true}
	if runtime.GOOS != "darwin" {
		mods = ggui.Mods{Ctrl: true, Shift: true}
	}
	waitFor(t, func() bool {
		p.Type(mods, ggui.KeyV)
		return strings.Contains(link.typed(), "\x1b[200~echo hi\x1b[201~")
	})
}

func TestResizeFollowsTheWidget(t *testing.T) {
	link, _, p := newTestTerminal(t)
	p.Frame()
	link.mu.Lock()
	defer link.mu.Unlock()
	if len(link.resizes) == 0 {
		t.Fatal("no resize sent for the widget's size")
	}
	last := link.resizes[len(link.resizes)-1]
	if last[0] < 40 || last[1] < 10 {
		t.Fatalf("resized to %v, too small for an 800x460 view", last)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// gridPoint is the window position of a cell's centre in the test terminal:
// the view sits under a 40px focus target, its grid inset by the padding.
func gridPoint(p *ggui.Probe, view *terminalView, col, row int) ggui.Point {
	return ggui.Pt(view.origin.X+(float64(col)+0.5)*view.cellW, view.origin.Y+(float64(row)+0.5)*view.cellH)
}

func TestDragSelectsAndCopies(t *testing.T) {
	link := newFakeLink()
	conn, err := newTermConn("s1", link, 80, 24, func() {})
	if err != nil {
		t.Fatal(err)
	}
	view := newTerminalView(conn, defaultPrefs, false)
	p := ggui.NewProbe(view, ggui.Sz(800, 400))
	defer func() { p.Close(); close(link.in); conn.close() }()
	link.output(t, "hello world\r\n")
	waitFor(t, func() bool { p.Frame(); return strings.Contains(screenText(conn), "hello world") })

	// A drag takes the last cell once the pointer is past its middle, and
	// not before.
	at := func(col int, frac float64) ggui.Point {
		pt := gridPoint(p, view, col, 0)
		return ggui.Pt(view.origin.X+(float64(col)+frac)*view.cellW, pt.Y)
	}
	p.Press(at(0, 0.1))
	p.Move(at(4, 0.25))
	p.Release(at(4, 0.25))
	if got := conn.selection(); got != "hell" {
		t.Fatalf("selected %q short of the middle of the o, want %q", got, "hell")
	}
	conn.clearSelection() // a fresh press, not the second of a double-click
	p.Press(at(0, 0.1))
	p.Move(at(4, 0.75))
	p.Release(at(4, 0.75))
	if got := conn.selection(); got != "hello" {
		t.Fatalf("selected %q, want %q", got, "hello")
	}
	copyChord := ggui.Mods{Meta: true}
	if runtime.GOOS != "darwin" {
		copyChord = ggui.Mods{Ctrl: true, Shift: true}
	}
	p.Type(copyChord, ggui.KeyC)
	var clip string
	p.Post(func() { clip = ggui.CurrentClipboard().Read() })
	p.Frame()
	if clip != "hello" {
		t.Fatalf("clipboard %q after copy, want %q", clip, "hello")
	}
	if got := link.typed(); got != "" {
		t.Fatalf("copy sent %q to the program", got)
	}
	// Typing clears the selection.
	p.Text("x")
	if conn.selection() != "" {
		t.Fatal("selection survived typing")
	}
}

func TestMouseGoesToAProgramThatAsks(t *testing.T) {
	link := newFakeLink()
	conn, err := newTermConn("s1", link, 80, 24, func() {})
	if err != nil {
		t.Fatal(err)
	}
	view := newTerminalView(conn, defaultPrefs, false)
	p := ggui.NewProbe(view, ggui.Sz(800, 400))
	defer func() { p.Close(); close(link.in); conn.close() }()
	link.output(t, "\x1b[?1000h\x1b[?1006h") // button events, SGR encoding
	waitFor(t, func() bool { return conn.mouseReported() })
	p.Frame()

	p.Click(gridPoint(p, view, 2, 3))
	if got := link.typed(); got != "\x1b[<0;3;4M\x1b[<0;3;4m" {
		t.Fatalf("click sent %q, want an SGR press and release at column 3, row 4", got)
	}
	if conn.selection() != "" {
		t.Fatal("a click the program took also selected")
	}
}

// screenText is the terminal's screen as plain text.
func screenText(c *termConn) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, _ := c.vt.PlainText()
	return s
}

// A Hangul syllable held for the IME to rewrite is taken back by
// Backspace, which the program does not see.
func TestBackspaceTakesAHeldSyllableBack(t *testing.T) {
	if !holdsSyllables {
		t.Skip("syllables are not held here")
	}
	link, _, p := newTestTerminal(t)
	p.Text("가나")
	p.Type(ggui.Mods{}, ggui.KeyBackspace)
	p.Type(ggui.Mods{}, ggui.KeyEnter)
	if got := link.typed(); got != "가\r" {
		t.Fatalf("typed %q, want %q", got, "가\r")
	}
}

// A program is at work while it says so with a progress report.
func TestTerminalIsBusyWhileItReportsProgress(t *testing.T) {
	link, conn, _ := newTestTerminal(t)
	link.output(t, "printing on its own")
	waitFor := func(what string, want bool) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for conn.busy() != want {
			if time.Now().After(deadline) {
				t.Fatalf("busy is not %v %s", want, what)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	time.Sleep(50 * time.Millisecond)
	waitFor("with only output", false)
	link.output(t, "\x1b]9;4;3\x07")
	waitFor("after an indeterminate report", true)
	link.output(t, "\x1b]9;4;0\x07")
	waitFor("once it is removed", false)
}

// A program that took the mouse, as claude does, sees a drag with the
// button held, not as motion without one.
func TestDragReachesAProgramWithTheButtonHeld(t *testing.T) {
	link, conn, p := newTestTerminal(t)
	link.output(t, "\x1b[?1049h\x1b[?1002h\x1b[?1003h\x1b[?1006h")
	for !conn.mouseReported() {
		time.Sleep(5 * time.Millisecond)
	}
	link.typed()
	p.Press(ggui.Pt(100, 200))
	if got := link.typed(); !strings.HasSuffix(got, "\x1b[<0;9;9M") { // after the move there
		t.Fatalf("press sent %q", got)
	}
	p.Move(ggui.Pt(160, 200))
	if got := link.typed(); got != "\x1b[<32;14;9M" {
		t.Fatalf("drag sent %q, want motion with the left button", got)
	}
	p.Release(ggui.Pt(160, 200))
	if got := link.typed(); got != "\x1b[<0;14;9m" {
		t.Fatalf("release sent %q", got)
	}
}

// What a program copies with OSC 52 reaches the clipboard.
func TestProgramCopiesToTheClipboard(t *testing.T) {
	link, _, p := newTestTerminal(t)
	link.output(t, "\x1b]52;c;"+base64.StdEncoding.EncodeToString([]byte("copied by claude"))+"\x07")
	deadline := time.Now().Add(2 * time.Second)
	for ggui.CurrentClipboard().Read() != "copied by claude" {
		if time.Now().After(deadline) {
			t.Fatalf("clipboard holds %q", ggui.CurrentClipboard().Read())
		}
		p.Frame()
		time.Sleep(5 * time.Millisecond)
	}
}
