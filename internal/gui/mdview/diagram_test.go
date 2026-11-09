package mdview

import (
	"strings"
	"testing"

	"github.com/ironpark/ggfx"
	"github.com/ironpark/ggui"
)

// diagrams is a host that draws a mermaid block as img.
type diagrams struct {
	Base
	img *ggui.StateValue[Image]
}

func (d diagrams) Diagram(_, lang string) *ggui.StateValue[Image] {
	if lang != "mermaid" {
		return nil
	}
	return d.img
}

// A block in a diagram's language shows as the picture the host draws of
// it, and as its code while it cannot be drawn, or where the host draws
// none.
func TestADiagramBlockShowsAsItsPicture(t *testing.T) {
	src := "```mermaid\nflowchart LR\n  A --> B\n```\n"
	img := ggui.State(Image{})
	p := ggui.NewProbe(Page(diagrams{img: img}, src), ggui.Sz(900, 600))
	defer p.Close()
	p.Frame()
	if page := p.Semantics().String(); !strings.Contains(page, "flowchart LR") {
		t.Fatalf("a diagram that cannot be drawn does not show its code:\n%s", page)
	}
	pic := ggfx.NewImage(40, 20)
	defer pic.Deallocate()
	img.Set(Image{Img: pic, Size: ggui.Sz(40, 20)})
	p.Frame()
	page := p.Semantics().String()
	if strings.Contains(page, "flowchart LR") || !strings.Contains(page, "mermaid") {
		t.Fatalf("a drawn diagram shows its code, or not its picture:\n%s", page)
	}

	q := ggui.NewProbe(Page(Base{}, src), ggui.Sz(900, 600))
	defer q.Close()
	q.Frame()
	if page := q.Semantics().String(); !strings.Contains(page, "flowchart LR") {
		t.Fatalf("a host that draws no diagrams does not show the code:\n%s", page)
	}
}
