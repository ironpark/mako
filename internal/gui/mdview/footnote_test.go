package mdview

import (
	"strings"
	"testing"

	"github.com/ironpark/ggui"
)

// A footnote shows where it is referred to as its raised number, and at
// the page's end; a shortcode shows as its emoji.
func TestFootnotesAndEmoji(t *testing.T) {
	src := "Shipped :rocket: today.[^why]\n\n[^why]: Because it was *ready*.\n"
	p := ggui.NewProbe(Page(Base{}, src), ggui.Sz(900, 600))
	defer p.Close()
	p.Frame()
	page := p.Semantics().String()
	for _, want := range []string{"Shipped 🚀 today.¹", "1.", "Because it was ready."} {
		if !strings.Contains(page, want) {
			t.Errorf("the page has no %q:\n%s", want, page)
		}
	}
	if strings.Contains(page, "[^why]") || strings.Contains(page, ":rocket:") {
		t.Errorf("markup shows:\n%s", page)
	}
}
