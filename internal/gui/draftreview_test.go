package gui

import (
	"strings"
	"testing"

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

	got := formatDraftReviewBatch(comments)
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

	if empty := formatDraftReviewBatch([]draftReviewComment{{Resolved: true}}); empty != "" {
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
