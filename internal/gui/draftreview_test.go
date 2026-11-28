package gui

import (
	"strings"
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

func TestFormatDraftReviewBatch(t *testing.T) {
	root := fileRoot{Node: "local", Path: "/repo"}
	comments := []draftReviewComment{
		{
			ID:        "rc-1",
			Root:      root,
			Path:      "a.go",
			LineRef:   "a.go:10-12",
			DiffBlock: "+foo := bar()",
			Note:      "Handle error return",
		},
		{
			ID:        "rc-2",
			Root:      root,
			Path:      "b.go",
			LineRef:   "b.go:4",
			DiffBlock: "-old\n+new",
			Note:      "Already fixed",
			Resolved:  true,
		},
		{
			ID:        "rc-3",
			Root:      root,
			Path:      "c.go",
			LineRef:   "c.go:22",
			DiffBlock: "+return nil",
			Note:      "Add unit test",
		},
	}

	got, headings := formatDraftReviewBatch(comments)
	if len(headings) != 2 || headings["rc-1"] != "### 1. a.go:10-12" || headings["rc-3"] != "### 2. c.go:22" {
		t.Fatalf("headings %v", headings)
	}
	if !strings.Contains(got, "2 code review comments across 2 file(s)") {
		t.Fatalf("unexpected header:\n%s", got)
	}
	if !strings.Contains(got, "### 1. a.go:10-12") || !strings.Contains(got, "Handle error return") {
		t.Fatalf("missing first comment:\n%s", got)
	}
	if strings.Contains(got, "Already fixed") {
		t.Fatalf("resolved comment must be omitted:\n%s", got)
	}
	if !strings.Contains(got, "### 2. c.go:22") || !strings.Contains(got, "Add unit test") {
		t.Fatalf("missing second comment:\n%s", got)
	}

	if empty, _ := formatDraftReviewBatch([]draftReviewComment{{Resolved: true}}); empty != "" {
		t.Fatalf("expected empty batch when all resolved, got %q", empty)
	}

	block := formatDiffBlock([]*protocol.DiffLine{
		{Op: "@", Text: "@@ -1,1 +1,1 @@"},
		{Op: "-", Text: "old"},
		{Op: "+", Text: "new"},
	})
	if block != "-old\n+new" {
		t.Fatalf("formatDiffBlock = %q", block)
	}
}

// A diff that holds a fence is fenced by a longer one.
func TestDraftReviewFencesWhatHoldsFences(t *testing.T) {
	block := "+s := `raw`\n+md := \"```go\"\n+x := \"````\""
	got, _ := formatDraftReviewBatch([]draftReviewComment{{ID: "a", Path: "a.go", LineRef: "a.go:1", DiffBlock: block}})
	if !strings.Contains(got, "\n`````diff\n"+block+"\n`````\n") && !strings.HasSuffix(got, "\n`````diff\n"+block+"\n`````") {
		t.Fatalf("fence:\n%s", got)
	}
	if f := codeFence("no ticks"); f != "```" {
		t.Fatalf("plain fence %q", f)
	}
}

// Comments put in a composer are resolved by its sending, of those its
// message still holds: not by being put there.
func TestDraftReviewResolvesOnSend(t *testing.T) {
	m := newModel(config.Default(), "")
	root := fileRoot{Node: "n", Path: "/repo"}
	m.DraftReview.Set([]draftReviewComment{
		{ID: "a", Root: root, LineRef: "a.go:1", SentTo: "s1", Heading: "### 1. a.go:1"},
		{ID: "b", Root: root, LineRef: "b.go:2", SentTo: "s1", Heading: "### 2. b.go:2"},
		{ID: "d", Root: root, LineRef: "a.go:1", SentTo: "s1", Heading: "### 3. a.go:1"},
		{ID: "c", Root: root, LineRef: "c.go:3", SentTo: "s2", Heading: "### 1. c.go:3"},
	})
	m.draftReviewSent("s1", "Please address\n\n### 1. a.go:1\nfix\n### 3. a.go:10-12\n")
	got := ggui.Peek(m.DraftReview)
	if !got[0].Resolved || got[1].Resolved || got[2].Resolved || got[0].SentTo != "" || got[1].SentTo != "" || got[2].SentTo != "" {
		t.Fatalf("after s1 sent: %+v", got)
	}
	if got[3].Resolved || got[3].SentTo != "s2" {
		t.Fatalf("another session's comment changed: %+v", got[3])
	}
}

func draftReviewProbe(t *testing.T) (*model, fileRoot, *ggui.StateValue[int], *ggui.Probe) {
	t.Helper()
	m := newModel(config.Default(), "")
	root := fileRoot{Node: "n", Path: "/repo"}
	m.addDraftReviewComment(root, "a.go", []*protocol.DiffLine{{Op: "+", Text: "x", New: 3}}, "why")
	// The diff tab builds the bar again as its lines change or are picked.
	rebuild := ggui.State(0)
	p := ggui.ProbeBuilder(func() ggui.Widget {
		return ggui.View(rebuild, func(int) ggui.Widget { return draftReviewBar(m, root) })
	}, ggui.Sz(900, 400))
	t.Cleanup(p.Close)
	p.Frame()
	return m, root, rebuild, p
}

func TestDraftReviewDrawerStaysOpenAcrossRebuilds(t *testing.T) {
	_, _, rebuild, p := draftReviewProbe(t)
	p.Tap(Tf("%d review comments · %d files", 1, 1))
	if _, ok := p.Find(T("Remove comment")); !ok {
		t.Fatal("the drawer did not open")
	}
	rebuild.Set(1)
	if _, ok := p.Find(T("Remove comment")); !ok {
		t.Fatal("the drawer closed as the diff tab built it again")
	}
}

func TestDraftReviewFollowsTheProjectsAgents(t *testing.T) {
	m, root, _, p := draftReviewProbe(t)
	if _, ok := p.Semantics().Find(ggui.RoleButton, T("No agent runs in this project")); !ok {
		t.Fatalf("no disabled send without agents:\n%s", p.Semantics().String())
	}
	s := &protocol.SessionInfo{Id: "s1", Agent: "claude", NodeId: root.Node, Project: root.Path, Caps: protocol.CapChat}
	m.Sessions.Set([]*protocol.SessionInfo{s})
	want := Tf("Send review (%d) to %s", 1, sessionTitle(s, ""))
	if _, ok := p.Find(want); !ok {
		t.Fatalf("no %q once an agent runs", want)
	}
	p.Tap(want)
	if got := ggui.Peek(m.DraftReview)[0]; got.Resolved || got.SentTo != "s1" {
		t.Fatalf("putting the review in the composer: %+v", got)
	}
}

// Lines no longer picked add nothing to the review.
func TestAddToReviewNeedsPickedLines(t *testing.T) {
	m := newModel(config.Default(), "")
	sel := ggui.State(lineSel{0, 0})
	lines := []*protocol.DiffLine{{Op: "+", Text: "x", New: 1}}
	p := ggui.ProbeBuilder(func() ggui.Widget {
		return selectionBar(m, fileRoot{Node: "n", Path: "/r"}, "a.go", lines, sel)
	}, ggui.Sz(900, 120))
	t.Cleanup(p.Close)
	p.Frame()
	sel.Set(noSel)
	p.Tap(T("Add to review"))
	if got := ggui.Peek(m.DraftReview); len(got) != 0 {
		t.Fatalf("added %+v", got)
	}
	sel.Set(lineSel{0, 0})
	p.Tap(T("Add to review"))
	if got := ggui.Peek(m.DraftReview); len(got) != 1 || got[0].LineRef != "a.go:1" {
		t.Fatalf("added %+v", got)
	}
}
