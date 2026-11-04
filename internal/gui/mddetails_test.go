package gui

import (
	"strings"
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
)

// A details shows its summary and folds what it holds, Markdown between its
// tags or HTML inside them, until the summary is clicked; one marked open
// starts open.
func TestDetailsFoldUnderTheirSummary(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	src := "Before.\n\n<details>\n<summary>More <b>here</b></summary>\n\nHidden *Markdown*.\n\n</details>\n\n" +
		"<details open><summary>Shown</summary>Inline HTML body</details>\n\nAfter.\n"
	p := ggui.NewProbe(markdownView(m, "doc", src), ggui.Sz(900, 600))
	defer p.Close()
	p.Frame()
	page := p.Semantics().String()
	for _, want := range []string{"Before.", "More here", "Shown", "Inline HTML body", "After."} {
		if !strings.Contains(page, want) {
			t.Errorf("the page has no %q:\n%s", want, page)
		}
	}
	if strings.Contains(page, "Hidden") || strings.Contains(page, "<summary") || strings.Contains(page, "details>") {
		t.Fatalf("a closed details shows its body, or tags show:\n%s", page)
	}
	summary, ok := p.Semantics().Find(ggui.RoleDisclosure, "More here")
	if !ok {
		t.Fatalf("no disclosure:\n%s", page)
	}
	p.Click(summary.Rect.Center())
	for range 30 {
		p.Frame()
	}
	if page := p.Semantics().String(); !strings.Contains(page, "Hidden Markdown.") {
		t.Fatalf("an opened details hides its body:\n%s", page)
	}
}
