package agents

import (
	"slices"
	"testing"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/protocol"
)

// launcher is the agent called name's Launcher.
func launcher(t *testing.T, name string) agent.Launcher {
	l, ok := As[agent.Launcher](name)
	if !ok {
		t.Fatalf("%s has no launcher", name)
	}
	return l
}

func TestArgumentID(t *testing.T) {
	cases := []struct {
		agent string
		args  []string
		want  string
	}{
		{"claude", []string{"claude", "--session-id=one"}, "one"},
		{"claude", []string{"claude", "--session-id", "one"}, "one"},
		{"claude", []string{"claude", "--resume", "two"}, "two"},
		{"claude", []string{"claude", "-r=three"}, "three"},
		{"claude", []string{"claude", "--fork-session", "--resume", "two"}, ""},
		{"claude", []string{"claude", "--session-id=one", "--fork-session"}, "one"},
		{"codex", []string{"codex", "resume", "four"}, "four"},
		{"codex", []string{"codex", "--model", "x"}, ""},
	}
	for _, c := range cases {
		a := launcher(t, c.agent)
		if got := a.ArgumentID(c.args); got != c.want {
			t.Errorf("%s %q: got %q, want %q", c.agent, c.args, got, c.want)
		}
	}
}

func TestResumeKeepsSettingsNotPromptOrFork(t *testing.T) {
	for _, tc := range []struct {
		agent      string
		args, want []string
	}{
		{"claude", []string{"claude", "--model", "opus", "--settings", "settings.json", "--session-id", "old", "--fork-session", "prompt"}, []string{"claude", "--resume", "new", "--model", "opus", "--settings", "settings.json"}},
		{"codex", []string{"codex", "-c", "mcp_servers.test.command=tool", "--model", "test", "resume", "--last", "old", "prompt", "--image", "image.png"}, []string{"codex", "resume", "new", "-c", "mcp_servers.test.command=tool", "--model", "test"}},
	} {
		a := launcher(t, tc.agent)
		if got := a.Resume(tc.args, "new"); !slices.Equal(got, tc.want) {
			t.Fatalf("%s: %q want %q", tc.agent, got, tc.want)
		}
	}
}

func TestPinOnlyWhenNothingPicksAConversation(t *testing.T) {
	claude := launcher(t, "claude")
	if !claude.Picks([]string{"claude", "--continue"}) || !claude.Picks([]string{"claude", "--resume=x"}) || claude.Picks([]string{"claude", "--model", "opus"}) {
		t.Fatal("claude pickers")
	}
	if got, ok := claude.Pin([]string{"claude"}, "id"); !ok || !slices.Equal(got, []string{"claude", "--session-id", "id"}) {
		t.Fatalf("pin=%q %v", got, ok)
	}
	codex := launcher(t, "codex")
	if _, ok := codex.Pin([]string{"codex"}, "id"); ok {
		t.Fatal("codex picks its own ID")
	}
}

func TestProgramsAreTheirAgents(t *testing.T) {
	for program, want := range map[string]string{"/usr/local/bin/claude": "claude", "/opt/codex.exe": "codex", "claude-wrapper": ""} {
		got := ""
		if e, ok := ByProgram(program); ok {
			got = e.Info.Name
		}
		if got != want {
			t.Errorf("%s: %q, want %q", program, got, want)
		}
	}
	if Caps("claude") == 0 || Caps("shell") != 0 || Caps("agy")&protocol.CapSubAgents != 0 {
		t.Error("caps")
	}
}

func TestForkDoesNotBindSourceConversation(t *testing.T) {
	claude := launcher(t, "claude")
	if got := claude.ArgumentID([]string{"claude", "--resume", "source", "--fork-session"}); got != "" {
		t.Fatalf("fork bound source %q", got)
	}
	if got := claude.ArgumentID([]string{"claude", "--resume", "source", "--fork-session", "--session-id", "destination"}); got != "destination" {
		t.Fatalf("fork ID=%q", got)
	}
}
