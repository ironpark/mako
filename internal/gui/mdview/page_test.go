package mdview

import (
	"testing"

	"github.com/ironpark/ggui"
)

func TestHTMLAttributes(t *testing.T) {
	tag := `<img width=40 SRC='a b.png' alt="x &amp; y">`
	if got := attr(tag, "src"); got != "a b.png" {
		t.Errorf("src %q", got)
	}
	if got := attr(tag, "alt"); got != "x & y" {
		t.Errorf("alt %q", got)
	}
	if got := attr(tag, "width"); got != "40" {
		t.Errorf("width %q", got)
	}
}

// A Markdown file selects as one page: a drag runs from block to block,
// and a click in the view, on the space around the blocks or on one of
// them, drops all of the selection, not the clicked block's alone.
func TestAMarkdownPageSelectsAsOne(t *testing.T) {
	p := ggui.NewProbe(Page(Base{}, "First paragraph here.\n\nMiddle one.\n\nLast one.\n"), ggui.Sz(900, 600))
	defer p.Close()
	p.Frame()
	find := func(name string) ggui.SemNode {
		t.Helper()
		n, ok := p.Semantics().Find(ggui.RoleText, name)
		if !ok {
			t.Fatalf("no %q", name)
		}
		return n
	}
	first, middle, last := find("First paragraph here."), find("Middle one."), find("Last one.")
	selected := func() int {
		n := 0
		for _, name := range []string{"First paragraph here.", "Middle one.", "Last one."} {
			if s := find(name); s.SelEnd > s.SelStart {
				n++
			}
		}
		return n
	}
	drag := func() {
		t.Helper()
		p.Press(first.Rect.Origin.Add(ggui.Pt(2, 5)))
		p.Move(last.Rect.Origin.Add(ggui.Pt(20, 5)))
		p.Release(last.Rect.Origin.Add(ggui.Pt(20, 5)))
		p.Frame()
		if n := selected(); n != 3 {
			t.Fatalf("a drag across the page selected %d blocks", n)
		}
	}
	drag()
	p.Click(ggui.Pt(880, 500))
	p.Frame()
	if n := selected(); n != 0 {
		t.Fatalf("a click beside the page left %d blocks selected", n)
	}
	drag()
	p.Click(middle.Rect.Origin.Add(ggui.Pt(10, 5)))
	p.Frame()
	if n := selected(); n != 0 {
		t.Fatalf("a click on a block left %d blocks selected", n)
	}
}
