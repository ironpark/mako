package gui

import (
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

// A commit's runs sum up as the worst of them, pending while any runs.
func TestRunsState(t *testing.T) {
	run := func(state, conclusion string) *protocol.ForgeItem {
		return &protocol.ForgeItem{State: state, Checks: conclusion}
	}
	for _, c := range []struct {
		runs []*protocol.ForgeItem
		want string
	}{
		{nil, ""},
		{[]*protocol.ForgeItem{run("completed", "success"), run("completed", "skipped")}, "success"},
		{[]*protocol.ForgeItem{run("completed", "failure"), run("in_progress", "")}, "pending"},
		{[]*protocol.ForgeItem{run("completed", "success"), run("completed", "cancelled")}, "failure"},
	} {
		if got := runsState(c.runs); got != c.want {
			t.Errorf("runsState = %q, want %q", got, c.want)
		}
	}
}

// A review handed to an agent names each line comment where it is, and
// leaves the conversation out.
func TestReviewPrompt(t *testing.T) {
	got := reviewPrompt(&protocol.ForgeDetail{
		Item: &protocol.ForgeItem{Number: 7, Title: "Fix it", Url: "https://github.com/o/r/pull/7"},
		Comments: []*protocol.ForgeComment{
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

// Each kind of editor is told the line as it takes it.
func TestEditorsOpenAtALine(t *testing.T) {
	if got := atColon("-g")("/a.go", 3); strings.Join(got, " ") != "-g /a.go:3" {
		t.Errorf("VS Code %q", got)
	}
	if got := atColon()("/a.go", 0); strings.Join(got, " ") != "/a.go" {
		t.Errorf("no line %q", got)
	}
	if got := jetBrains("/a.go", 7); strings.Join(got, " ") != "--line 7 /a.go" {
		t.Errorf("JetBrains %q", got)
	}
}
