package gui

import (
	"strings"
	"testing"
)

func TestMarkdownRendersItsBlocks(t *testing.T) {
	src := "# Title\n\nSome *text* with `code` and a [link](other.md).\n\n" +
		"> [!NOTE]\n> Mind this.\n\n- [x] done\n- open\n\n" +
		"```go\nfunc f() {}\n```\n\n| a | b |\n| - | -: |\n| 1 | 2 |\n\n" +
		"<p align=\"center\"><img src=\"https://x/logo.svg\" alt=\"Logo\"> Hello &amp; welcome</p>\n"
	m, p, id, _ := docAppNamed(t, "README.md", src)
	m.docs[id].preview.Set(true)
	p.Frame()
	page := p.Semantics().String()
	for _, want := range []string{"Title", "Mind this.", "Hello & welcome", T("Note"), "link"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page has no %q:\n%s", want, page)
		}
	}
	if strings.Contains(page, "[!NOTE]") || strings.Contains(page, "<img") {
		t.Errorf("markup shows on the page:\n%s", page)
	}
}
