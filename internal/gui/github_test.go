package gui

import (
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

// A commit's runs sum up as the worst of them, pending while any runs.
func TestRunsState(t *testing.T) {
	run := func(state, conclusion string) *protocol.GitHubItem {
		return &protocol.GitHubItem{State: state, Checks: conclusion}
	}
	for _, c := range []struct {
		runs []*protocol.GitHubItem
		want string
	}{
		{nil, ""},
		{[]*protocol.GitHubItem{run("completed", "success"), run("completed", "skipped")}, "success"},
		{[]*protocol.GitHubItem{run("completed", "failure"), run("in_progress", "")}, "pending"},
		{[]*protocol.GitHubItem{run("completed", "success"), run("completed", "cancelled")}, "failure"},
	} {
		if got := runsState(c.runs); got != c.want {
			t.Errorf("runsState = %q, want %q", got, c.want)
		}
	}
}

// A review handed to an agent names each line comment where it is, and
// leaves the conversation out.
func TestReviewPrompt(t *testing.T) {
	got := reviewPrompt(&protocol.GitHubDetail{
		Item: &protocol.GitHubItem{Number: 7, Title: "Fix it", Url: "https://github.com/o/r/pull/7"},
		Comments: []*protocol.GitHubComment{
			{Author: "a", Body: "Thanks!"},
			{Author: "b", Body: "Please split this.", Review: "CHANGES_REQUESTED"},
			{Author: "c", Body: "Off by one.", Path: "x.go", Line: 12},
		},
	})
	for _, want := range []string{"#7: Fix it", "- CHANGES_REQUESTED (b): Please split this.", "- x.go:12 (c): Off by one."} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "Thanks!") {
		t.Errorf("the conversation is in\n%s", got)
	}
}
