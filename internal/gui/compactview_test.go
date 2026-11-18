package gui

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/layoutdoc"
)

// pagesOf are the pages a phone lists of the document the window keeps of
// the name, by block.
func pagesOf(t *testing.T, m *model, name string) ([]string, *layoutdoc.Doc) {
	t.Helper()
	body := m.spacesNow().WindowLayouts[name]
	d, err := layoutdoc.Parse(body, m.blocks)
	if err != nil {
		t.Fatalf("%s: %v", body, err)
	}
	var out []string
	for _, p := range layoutdoc.Pages(d) {
		out = append(out, p.Block)
	}
	return out, d
}

// Settings ▸ Spaces lists the pages a phone shows of the space's document;
// moving and hiding them writes the document's compact root, as the
// user's edit of the workspace, and the window's order again takes it
// away. The window's own root is left as it was.
func TestThePhonesPagesAreTheUsersToOrder(t *testing.T) {
	m, p := windowProbe(t, func(m *model) ggui.Widget { return compactSection(m) }, nil)
	sem := p.Semantics().String()
	for _, want := range []string{"Files", "Git", "Agents", "mako.files"} {
		if !strings.Contains(sem, want) {
			t.Fatalf("no %q in:\n%s", want, sem)
		}
	}
	d := layoutdoc.Default()
	folded := foldedPages(d)
	reversed := slices.Clone(folded)
	slices.Reverse(reversed)
	m.setCompactPages(layoutdoc.DefaultName, d, reversed[:len(reversed)-1])
	got, doc := pagesOf(t, m, layoutdoc.DefaultName)
	want := []string{}
	for _, pg := range reversed[:len(reversed)-1] {
		want = append(want, pg.Block)
	}
	if !slices.Equal(got, want) || doc.Compact == nil || doc.Written() != layoutdoc.VersionVariants {
		t.Fatalf("pages %v, want %v; compact %v", got, want, doc.Compact)
	}
	if !slices.EqualFunc(foldedPages(doc), folded, samePage) {
		t.Fatal("the window's own root changed")
	}
	for range 3 {
		p.Frame()
	}
	if sem := p.Semantics().String(); !strings.Contains(sem, "Use the window's order") || !strings.Contains(sem, "Show") {
		t.Fatalf("the section after:\n%s", sem)
	}
	// A compact root written by hand, its IDs its own, shows its blocks as
	// shown, not hidden.
	m.setWindowLayout(layoutdoc.DefaultName, []byte(`{"version":2,"root":`+mustJSON(t, d.Root)+`,"variants":{"compact":{"id":"phone","column":[{"id":"c1","block":"mako.git"}]}}}`))
	for range 3 {
		p.Frame()
	}
	if sem := p.Semantics().String(); strings.Count(sem, "Git") != 1 {
		t.Fatalf("the hand-written compact root's block listed twice:\n%s", sem)
	}
	m.setCompactPages(layoutdoc.DefaultName, doc, folded)
	if _, doc := pagesOf(t, m, layoutdoc.DefaultName); doc.Compact != nil {
		t.Fatal("the window's order kept a compact root")
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
