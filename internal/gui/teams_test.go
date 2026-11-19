package gui

import (
	"strings"
	"testing"
)

func TestTeamPrompts(t *testing.T) {
	team := agentTeam{Name: "review", Members: []teamMember{
		{Name: "Builder", Agent: "claude", Role: "Build it.\nRun the tests."},
		{Name: "Reviewer", Agent: "codex"},
	}}
	brief := teamBrief(team)
	if !strings.HasPrefix(brief, "[Mako team review:") || !strings.Contains(brief, "- Builder (claude): Build it. Run the tests.\n") ||
		!strings.Contains(brief, "- Reviewer (codex)\n") {
		t.Fatalf("brief =\n%s", brief)
	}
	for _, c := range []struct{ role, task, want string }{
		{"Review.", "", "Review."},
		{"", "Fix login", "The team's task:\nFix login"},
		{"Review.", "Fix login", "Review.\n\nThe team's task:\nFix login"},
	} {
		if got := memberPrompt(teamMember{Role: c.role}, c.task); got != c.want {
			t.Errorf("memberPrompt(%q, %q) = %q, want %q", c.role, c.task, got, c.want)
		}
	}
	if s := teamShape([]agentTeam{team, {}}); len(s) != 2 || s[0] != 2 || s[1] != 0 {
		t.Fatalf("shape = %v", s)
	}
}
