package gui

import (
	"slices"
	"strings"
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/sanbo/internal/protocol"
)

func TestGroupSessions(t *testing.T) {
	s := func(id, node, project string) protocol.SessionInfo {
		return protocol.SessionInfo{ID: id, NodeID: node, Project: project}
	}
	sessions := []protocol.SessionInfo{
		s("a", "n1", "/src/sanbo/internal"),
		s("b", "n1", "/src/sanbo"),
		s("c", "n2", "/src/sanbo"),
		s("d", "n1", "/src/sanbox"),
	}
	nodes := []protocol.Node{{ID: "n1", Name: "mac"}, {ID: "n2", Name: "box"}}

	byProject := groupSessions(sessions, groupProject, nodes, nil)
	got := map[string][]string{}
	for _, g := range byProject {
		for _, x := range g.Sessions {
			got[g.NodeID+":"+g.Project] = append(got[g.NodeID+":"+g.Project], x.ID)
		}
	}
	// A folder inside another session's project joins it, on its node
	// only, and a sibling sharing a prefix does not.
	if len(byProject) != 3 || len(got["n1:/src/sanbo"]) != 2 || len(got["n2:/src/sanbo"]) != 1 || len(got["n1:/src/sanbox"]) != 1 {
		t.Fatalf("by project: %v", got)
	}
	if rel := subfolder("/src/sanbo/internal", "/src/sanbo"); rel != "internal" {
		t.Fatalf("subfolder %q", rel)
	}

	byHost := groupSessions(sessions, groupHost, nodes, nil)
	if len(byHost) != 2 || nodeName(nodes, byHost[0].NodeID) != "box" || len(byHost[1].Sessions) != 3 {
		t.Fatalf("by host: %+v", byHost)
	}
	if flat := groupSessions(sessions, groupFlat, nodes, nil); len(flat) != 1 || len(flat[0].Sessions) != 4 {
		t.Fatalf("flat: %+v", flat)
	}
}

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

func TestStatusRowLeavesOutWhatDoesNotFit(t *testing.T) {
	item := func(w float64) ggui.Widget { return ggui.Box().Size(w, 10) }
	r := &statusRow{gap: 10, split: 2, items: []ggui.Widget{item(50), item(50), item(50), item(50), item(80)}, drop: []int{3, 2, 1, 0, 4}}
	p := ggui.ProbeBuilder(func() ggui.Widget { return r }, ggui.Sz(280, 30))
	defer p.Close()
	p.Frame()
	// 280 and the gaps is wider than 280; the first to drop goes.
	if want := []bool{true, true, true, false, true}; !slices.Equal(r.shown, want) {
		t.Fatalf("shown %v, want %v", r.shown, want)
	}
}
