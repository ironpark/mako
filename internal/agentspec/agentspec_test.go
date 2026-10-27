package agentspec

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
)

func TestAnAgentWrittenAsItsCommandStillReads(t *testing.T) {
	var agents map[string]Agent
	if err := json.Unmarshal([]byte(`{"claude": ["claude", "--fast"], "codex": {"command": ["codex"], "args": ["-q"]}}`), &agents); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(agents["claude"].Command, []string{"claude", "--fast"}) || !slices.Equal(agents["codex"].Args, []string{"-q"}) {
		t.Fatalf("agents = %+v", agents)
	}
}

func TestResolveLayersAgentProfileAndRequest(t *testing.T) {
	agents := map[string]Agent{
		"sh": {Command: []string{"sh", "-c"}, Args: []string{"a"}, Env: map[string]string{"X": "agent", "Y": "agent"},
			Profiles: []Profile{{Name: "p", Args: []string{"b"}, Env: map[string]string{"Y": "profile", "Z": "profile"}}}},
	}
	l, err := Resolve("sh", "p", []string{"c"}, map[string]string{"Z": "request"}, agents)
	if err != nil {
		t.Fatal(err)
	}
	if got := l.Command[1:]; !slices.Equal(got, []string{"-c", "a", "b", "c"}) {
		t.Fatalf("command = %q", l.Command)
	}
	if l.Env["X"] != "agent" || l.Env["Y"] != "profile" || l.Env["Z"] != "request" {
		t.Fatalf("env = %v", l.Env)
	}
	if agents["sh"].Env["Y"] != "agent" {
		t.Fatal("resolving changed the agent's own environment")
	}
	if _, err := Resolve("sh", "missing", nil, nil, agents); err == nil {
		t.Fatal("an unknown profile resolved")
	}
}

// A configured built-in without a command keeps the built-in's.
func TestABuiltinNeedsNoCommandOfItsOwn(t *testing.T) {
	a, ok := Lookup("shell", map[string]Agent{"shell": {Args: []string{"-l"}}})
	if !ok || !slices.Equal(a.Command, Builtin["shell"]) || !slices.Equal(a.Args, []string{"-l"}) {
		t.Fatalf("shell = %+v", a)
	}
}

func TestTargets(t *testing.T) {
	agents := map[string]Agent{"claude": {Profiles: []Profile{{Name: "opus"}, {Name: "work"}}}}
	got := Targets(agents)
	want := []string{"agy", "claude", "claude:opus", "claude:work", "codex", "omo", "omp", "opencode", "pi", "shell"}
	if !slices.Equal(got, want) {
		t.Fatalf("targets = %q, want %q", got, want)
	}
	if a, p := SplitTarget("claude:opus"); a != "claude" || p != "opus" || Target(a, p) != "claude:opus" {
		t.Fatal("a target does not split into its agent and profile")
	}
}

func TestSplitArgsReadsWhatJoinArgsWrites(t *testing.T) {
	for _, args := range [][]string{
		{"--model", "opus"},
		{"--append-system-prompt", "be brief, it's late"},
		{"", `a"b`, `c\d`, "$HOME", "x y"},
	} {
		got, err := SplitArgs(JoinArgs(args))
		if err != nil || !slices.Equal(got, args) {
			t.Fatalf("%q became %q (%v) through %s", args, got, err, JoinArgs(args))
		}
	}
	got, err := SplitArgs(`--a "two words" three\ four 'it''s'`)
	if err != nil || !slices.Equal(got, []string{"--a", "two words", "three four", "its"}) {
		t.Fatalf("split = %q, %v", got, err)
	}
	if _, err := SplitArgs(`"open`); err == nil {
		t.Fatal("an unterminated quote was accepted")
	}
}

func TestResolveExpandsLikeAShell(t *testing.T) {
	t.Setenv("MAKO_TEST_BASE", "/opt")
	home, _ := os.UserHomeDir()
	agents := map[string]Agent{"sh": {Command: []string{"sh"},
		Args:     []string{"--config", "~/.sh", "--cost", "$$5"},
		Env:      map[string]string{"DIR": "$MAKO_TEST_BASE/sh", "SAME": "${DIR}"},
		Profiles: []Profile{{Name: "work", Env: map[string]string{"CLAUDE_CONFIG_DIR": "~/.claude-work", "BIN": "${DIR}/bin"}}}}}
	l, err := Resolve("sh", "work", []string{"$HOME"}, nil, agents)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"--config", home + "/.sh", "--cost", "$5", "$HOME"}; !slices.Equal(l.Command[1:], want) {
		t.Fatalf("command = %q, want %q", l.Command[1:], want)
	}
	// A value sees the layers before it, not its own: SAME's DIR is the
	// daemon's, unset, while the profile's BIN sees the agent's.
	if l.Env["DIR"] != "/opt/sh" || l.Env["SAME"] != "" || l.Env["BIN"] != "/opt/sh/bin" || l.Env["CLAUDE_CONFIG_DIR"] != home+"/.claude-work" {
		t.Fatalf("env = %v", l.Env)
	}
}
