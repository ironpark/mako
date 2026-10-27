package gui

import (
	"slices"
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

func TestCommitGraphBranchesAndMerges(t *testing.T) {
	c := func(hash string, parents ...string) *protocol.GitCommit {
		return &protocol.GitCommit{Hash: hash, Parents: parents}
	}
	// m merges b into a line from a; d is a second root's tip.
	rows := commitGraph([]*protocol.GitCommit{c("m", "a", "b"), c("d", "x"), c("b", "a"), c("a"), c("x")})
	want := []graphRow{
		{Col: 0, Lanes: 2, Lines: []graphLine{{0, 0, true}, {0, 1, true}}},
		{Col: 2, Lanes: 3, Lines: []graphLine{{0, 0, false}, {0, 0, true}, {1, 1, false}, {1, 1, true}, {2, 2, true}}},
		{Col: 1, Lanes: 3, Lines: []graphLine{{0, 0, false}, {0, 0, true}, {1, 1, false}, {2, 2, false}, {2, 2, true}, {1, 0, true}}},
		{Col: 0, Lanes: 3, Lines: []graphLine{{0, 0, false}, {2, 2, false}, {2, 2, true}}},
		{Col: 2, Lanes: 3, Lines: []graphLine{{2, 2, false}}},
	}
	for i := range want {
		if rows[i].Col != want[i].Col || rows[i].Lanes != want[i].Lanes || !slices.Equal(rows[i].Lines, want[i].Lines) {
			t.Errorf("row %d = %+v\nwant %+v", i, rows[i], want[i])
		}
	}
}

func TestCommitPathsNamesRenamesOnlyWhenPicking(t *testing.T) {
	files := []*protocol.GitFile{{Path: "a"}, {Path: "new", From: "old"}, {Path: "c"}}
	if got := commitPaths(files, nil); got != nil {
		t.Fatalf("all picked: %q", got)
	}
	if got := commitPaths(files, map[string]bool{"c": true}); !slices.Equal(got, []string{"a", "new", "old"}) {
		t.Fatalf("picked: %q", got)
	}
	if picked(files, map[string]bool{"a": true, "c": true}) != 1 {
		t.Fatal("picked count")
	}
}
