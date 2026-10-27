package gui

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ironpark/ggui"
	"github.com/ironpark/sanbo/internal/protocol"
)

// What reads as a link around a place in a line, and what does not.
func TestLinksAreFoundInText(t *testing.T) {
	for _, c := range []struct {
		line, at, want string // at is a piece of the line the pointer is on
	}{
		{"see https://example.com/a?b=1.", "example", "https://example.com/a?b=1"},
		{"(url=https://x.dev/p)", "x.dev", "https://x.dev/p"},
		{"wrote /tmp/sanbo-drops/a/shot.png", "shot", "/tmp/sanbo-drops/a/shot.png"},
		{"⏺ Updated internal/gui/view.go:120:4 and", "view", "internal/gui/view.go:120:4"},
		{"Read README.md, then", "README", "README.md"},
		{"open ~/notes/todo.md", "todo", "~/notes/todo.md"},
		{"│ ./run.sh │", "run", "./run.sh"},
		{"file:///Users/me/a%20b.png", "Users", "file:///Users/me/a%20b.png"},
		{"[Image: source: /var/T/sanbo-drops/x/스크린샷 2026-10-27 오후 4.41.22.png]", "오후", "/var/T/sanbo-drops/x/스크린샷 2026-10-27 오후 4.41.22.png"},
		{"[Image: source: /var/T/스크린샷 2026-10-27 오후 4.41.22.png]", "source", ""},
		{"cat '/tmp/my notes/a b.txt' | less", "notes", "/tmp/my notes/a b.txt"},
		{`open /tmp/a\ b.png now`, "b.png", `/tmp/a\ b.png`},
		{"cd /tmp/build then make", "then", ""},
		{"see /tmp/a.png and more.txt", "and", ""},
		{"e.g. this", "e.g", ""},
		{"v1.73.0 is out", "73", ""},
		{"a / b", "/", ""},
		{"--flag x", "flag", ""},
		{"plain words", "words", ""},
	} {
		line := []rune(c.line)
		i := len([]rune(c.line[:strings.Index(c.line, c.at)]))
		start, end, ok := linkIn(line, i)
		got := ""
		if ok {
			got = string(line[start:end])
		}
		if got != c.want {
			t.Errorf("%q at %q: link %q, want %q", c.line, c.at, got, c.want)
		}
	}
	for _, c := range []struct {
		target, path string
		line         int
	}{
		{"internal/gui/view.go:120:4", "internal/gui/view.go", 120},
		{"/a/b.md:7", "/a/b.md", 7},
		{"file:///Users/me/a%20b.png", "/Users/me/a b.png", 0},
		{"README.md", "README.md", 0},
		{`/tmp/a\ b.png`, "/tmp/a b.png", 0},
	} {
		if p, line := linkPath(c.target); p != c.path || line != c.line {
			t.Errorf("linkPath(%q) = %q, %d; want %q, %d", c.target, p, line, c.path, c.line)
		}
	}
}

// A program's output names files: a link marked with OSC 8 and a path
// written plainly are found where the pointer is, over the wide runes
// before them.
func TestTerminalFindsLinksUnderThePointer(t *testing.T) {
	link := newFakeLink()
	conn, err := newTermConn("s", link, 80, 6, func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { close(link.in); conn.close() }()
	link.output(t, "한글 ./shot.png done\r\n\x1b]8;;https://example.com/x\x1b\\docs\x1b]8;;\x1b\\ here")
	// Links are read from the screen as last painted.
	waitFor(t, func() bool {
		_, _ = conn.frame()
		l, ok := conn.linkAt(1, 1)
		return ok && l.target == "https://example.com/x" && strings.Contains(screenText(conn), "here")
	})
	// 한글 takes four cells, and a space: the path starts at the sixth.
	l, ok := conn.linkAt(8, 0)
	if !ok || l.target != "./shot.png" || l.col0 != 5 || l.col1 != 15 {
		t.Fatalf("path link %+v %v", l, ok)
	}
	if l, _ := conn.linkAt(1, 1); l.col0 != 0 || l.col1 != 4 {
		t.Fatalf("OSC 8 link covers %d..%d, want 0..4", l.col0, l.col1)
	}
	if _, ok := conn.linkAt(17, 0); ok {
		t.Fatal("a plain word is a link")
	}
}

// A path followed from a session opens on its node: an image in the
// preview, another file in a tab at its line, one relative to where the
// program is; one that is not there says so.
func TestFollowedPathsOpenOnTheSessionsNode(t *testing.T) {
	m, p, _, _ := docApp(t, "x")
	self := ggui.Peek(m.Conn).Self.GetId()
	dir := t.TempDir()
	shot := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(shot, []byte("\x89PNG\r\n\x1a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "s", NodeId: self, Agent: "claude", Project: "/elsewhere"}})

	m.DetailsOpen.Set(false)
	m.followLink("s", "file://"+shot, "")
	probeUntil(t, p, "the preview", func() bool { return ggui.Peek(m.Preview).Path == shot })
	if !ggui.Peek(m.DetailsOpen) || ggui.Peek(m.DetailTab) != filesTab {
		t.Fatal("the preview opened out of sight")
	}

	m.followLink("s", "main.go:3", dir)
	id := docTabID(self, filepath.Join(dir, "main.go"))
	probeUntil(t, p, "the file's tab", func() bool { d, ok := m.doc(id); return ok && !d.Loading })

	m.followLink("s", "missing.go", dir)
	probeUntil(t, p, "the error", func() bool {
		_, ok := p.Semantics().Find(ggui.RoleText, Tf("Could not open %s", "missing.go"))
		return ok
	})
}

// ⌘-click follows a link, before a program that asked for the mouse
// hears of it; a plain click is the program's.
func TestCommandClickFollowsALink(t *testing.T) {
	link := newFakeLink()
	conn, err := newTermConn("s1", link, 80, 24, func() {})
	if err != nil {
		t.Fatal(err)
	}
	view := newTerminalView(conn, defaultPrefs, false)
	var followed []string
	view.OnLink(func(target, cwd string) { followed = append(followed, target) })
	p := ggui.NewProbe(view, ggui.Sz(800, 400))
	defer func() { p.Close(); close(link.in); conn.close() }()
	link.output(t, "\x1b[?1000h\x1b[?1006hsaved out/plot.png")
	waitFor(t, func() bool { p.Frame(); return strings.Contains(screenText(conn), "plot.png") })

	follow := ggui.Mods{Meta: true}
	if runtime.GOOS != "darwin" {
		follow = ggui.Mods{Ctrl: true}
	}
	// Held over it, the keys underline the link.
	p.MoveWith(gridPoint(p, view, 10, 0), follow)
	p.Frame()
	if view.link == nil || view.link.col0 != 6 || view.link.col1 != 18 {
		t.Fatalf("hover link %+v, want cells 6 to 18", view.link)
	}
	p.MoveWith(gridPoint(p, view, 10, 0), follow) // the cursor follows the regions of the frame before
	if p.Cursor() != ggui.CursorShapePointer {
		t.Fatalf("cursor %v over a link, want the pointing hand", p.Cursor())
	}
	p.Move(gridPoint(p, view, 11, 0))
	if view.link != nil {
		t.Fatal("the link stayed underlined once the keys were let go")
	}
	p.ClickWith(gridPoint(p, view, 10, 0), follow)
	if len(followed) != 1 || followed[0] != "out/plot.png" {
		t.Fatalf("followed %q", followed)
	}
	if got := link.typed(); got != "" {
		t.Fatalf("the program heard %q of a followed link", got)
	}
	p.Click(gridPoint(p, view, 10, 0))
	if len(followed) != 1 || link.typed() == "" {
		t.Fatal("a plain click followed the link, or the program missed it")
	}
}
