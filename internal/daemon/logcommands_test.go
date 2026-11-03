package daemon

import (
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/agent"
)

// A session has the commands its log said, or before it says any, the
// latest its agent's logs said; they go with it as it closes.
func TestLoggedCommandsAreASessionsOrItsAgents(t *testing.T) {
	names := func(cmds []agent.Command) string {
		var out []string
		for _, c := range cmds {
			out = append(out, c.Name)
		}
		return strings.Join(out, ",")
	}
	var l loggedCommands
	l.set("a", "claude", []agent.Command{{Name: "simplify"}, {Name: "loop"}})
	if got := names(l.of("a", "claude")); got != "simplify,loop" {
		t.Fatalf("a session's %q", got)
	}
	if got := names(l.of("b", "claude")); got != "simplify,loop" {
		t.Fatalf("a new session's %q", got)
	}
	l.set("b", "claude", nil)
	l.forget("a")
	if got := names(l.of("a", "codex")); got != "" {
		t.Fatalf("a closed session's %q", got)
	}
	if got := names(withLogged([]agent.Command{{Name: "loop"}, {Name: "clear"}}, l.of("b", "claude"))); got != "loop,clear,simplify" {
		t.Fatalf("merged %q", got)
	}
}
