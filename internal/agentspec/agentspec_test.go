package agentspec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

// onPath puts programs that do nothing on a PATH of their own.
func onPath(t testing.TB, programs ...string) {
	dir := t.TempDir()
	for _, p := range programs {
		if err := os.WriteFile(filepath.Join(dir, p), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	t.Setenv("SHELL", filepath.Join(dir, "sh"))
}

func TestTargets(t *testing.T) {
	onPath(t, "claude", "codex", "sh")
	agents := map[string]Agent{"claude": {Profiles: []Profile{{Name: "opus"}, {Name: "work"}}}}
	got := Targets(agents)
	want := []string{"claude", "claude:opus", "claude:work", "codex"}
	if !slices.Equal(got, want) {
		t.Fatalf("targets = %q, want %q: the shell is not an agent", got, want)
	}
	// A node's daemon starts the shell as it does an agent.
	if got := Targets(WithShell(agents, Shell{})); !slices.Contains(got, ShellName) {
		t.Fatalf("targets with the shell = %q", got)
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
	// A Windows path keeps its backslashes.
	got, err = SplitArgs(`C:\tools\claude.exe --add-dir \\server\share C:\with\ space\`)
	if err != nil || !slices.Equal(got, []string{`C:\tools\claude.exe`, "--add-dir", `\\server\share`, `C:\with space\`}) {
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

func TestAnAgentNotInstalledIsNotOffered(t *testing.T) {
	onPath(t, "claude", "codex", "sh")
	yes, no := true, false
	agents := map[string]Agent{
		"agy":   {Enabled: &yes}, // turned on, though not found
		"codex": {Enabled: &no},  // turned off, though found
	}
	got := Offered(agents)
	if want := []string{"agy", "claude"}; !slices.Equal(got, want) {
		t.Fatalf("offered %q, want %q", got, want)
	}
	if !slices.Contains(Names(agents), "codex") || !slices.Contains(Names(agents), "opencode") {
		t.Fatal("an agent not offered left the names a node can start")
	}
	if !(Agent{Command: []string{"claude", "--x"}}).Installed() || (Agent{Command: []string{"nowhere-to-be-found"}}).Installed() {
		t.Fatal("installed misread the PATH")
	}
}

// The shell starts as the user's login shell, unless it names another
// program, with its own flags and environment.
func TestTheShellIsConfiguredApart(t *testing.T) {
	onPath(t, "sh", "zsh")
	l, err := Resolve("", "", nil, nil, WithShell(nil, Shell{}))
	if err != nil || filepath.Base(l.Command[0]) != "sh" {
		t.Fatalf("the default shell is %q, %v; want the login shell", l.Command, err)
	}
	l, err = Resolve(ShellName, "", nil, nil, WithShell(nil, Shell{Command: []string{"zsh"}, Args: []string{"-l"}, Env: map[string]string{"A": "b"}}))
	if err != nil || filepath.Base(l.Command[0]) != "zsh" || !slices.Equal(l.Command[1:], []string{"-l"}) || l.Env["A"] != "b" {
		t.Fatalf("the configured shell is %q %v, %v", l.Command, l.Env, err)
	}
}

// A program found is taken to be there for a while, without allocating,
// on the PATH it was found on: another PATH looks it up again.
func TestInstalledLooksUpOnlyOnceAWhile(t *testing.T) {
	onPath(t, "claude")
	found := os.Getenv("PATH")
	if !installed("claude") {
		t.Fatal("claude not found on the PATH")
	}
	if n := testing.AllocsPerRun(100, func() { installed("claude") }); n != 0 {
		t.Fatalf("%v allocations for a program looked up already", n)
	}
	t.Setenv("PATH", t.TempDir())
	if installed("claude") {
		t.Fatal("claude found on a PATH it is not on: the lookup on another PATH was taken")
	}
	t.Setenv("PATH", found)
	if !installed("claude") {
		t.Fatal("claude not found on its PATH again")
	}
}

func BenchmarkInstalled(b *testing.B) {
	onPath(b, "claude")
	// A PATH as long as a user's.
	b.Setenv("PATH", os.Getenv("PATH")+strings.Repeat(":/opt/homebrew/bin", 20))
	b.ReportAllocs()
	for b.Loop() {
		installed("claude")
	}
}

// A profile's model starts its agent on it by the agent's own flag, after
// the agent's and the profile's flags and before the request's; an agent
// run through a wrapper gets no flag mako does not know the place of.
func TestResolveGivesAProfilesModel(t *testing.T) {
	claude := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(claude, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	profiles := []Profile{{Name: "p", Args: []string{"--b"}, Model: "opus", Effort: "high"}}
	l, err := Resolve("claude", "p", []string{"req"}, nil, map[string]Agent{"claude": {Command: []string{claude}, Args: []string{"--a"}, Profiles: profiles}})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(l.Command[1:], " "); got != "--a --b --model opus --effort high req" {
		t.Fatalf("command %q", got)
	}
	l, err = Resolve("claude", "p", nil, nil, map[string]Agent{"claude": {Command: []string{"/bin/sh", "-c", "claude"}, Profiles: profiles}})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(l.Command, "--model") || slices.Contains(l.Command, "--effort") {
		t.Fatalf("a wrapper given %q", l.Command)
	}

	codex := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(codex, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	lc, err := Resolve("codex", "p", nil, nil, map[string]Agent{"codex": {Command: []string{codex}, Profiles: []Profile{{Name: "p", Model: "gpt-6.2-sol", Effort: "xhigh"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(lc.Command[1:], " "); got != `--model gpt-6.2-sol -c model_reasoning_effort="xhigh"` {
		t.Fatalf("codex command %q", got)
	}
}

func TestResolveYoloAndOverrides(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"claude", "codex", "agy", "omp", "opencode"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)

	no := false
	yes := true
	agentsMap := map[string]Agent{
		"claude": {
			Yolo: true,
			Profiles: []Profile{
				{Name: "safe", Yolo: &no},
				{Name: "inherit"},
			},
		},
		"codex": {
			Profiles: []Profile{{Name: "fast", Yolo: &yes}},
		},
		"agy":      {Yolo: true},
		"omp":      {Yolo: true},
		"opencode": {Yolo: true},
	}

	// Agent YOLO applies and stays before request args.
	l, err := Resolve("claude", "inherit", []string{"do work"}, nil, agentsMap)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(l.Command[1:], " "); got != "--dangerously-skip-permissions do work" {
		t.Fatalf("claude inherit = %q", got)
	}

	// Profile YOLO false overrides agent YOLO true.
	l, err = Resolve("claude", "safe", []string{"do work"}, nil, agentsMap)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(l.Command, "--dangerously-skip-permissions") {
		t.Fatalf("claude safe kept skip-permissions: %q", l.Command)
	}

	// Launch options override profile settings.
	l, err = ResolveWith("claude", "safe", []string{"hi"}, nil, ResolveOptions{Model: "sonnet", Effort: "low", Yolo: &yes}, agentsMap)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(l.Command[1:], " "); got != "--model sonnet --effort low --dangerously-skip-permissions hi" {
		t.Fatalf("claude override = %q", got)
	}

	// Codex, agy, omp, opencode YOLO behavior.
	l, err = Resolve("codex", "fast", nil, nil, agentsMap)
	if err != nil || !slices.Contains(l.Command, "--dangerously-bypass-approvals-and-sandbox") {
		t.Fatalf("codex = %q, %v", l.Command, err)
	}
	l, err = Resolve("agy", "", nil, nil, agentsMap)
	if err != nil || !slices.Contains(l.Command, "--dangerously-skip-permissions") {
		t.Fatalf("agy = %q, %v", l.Command, err)
	}
	// agy normalizes legacy variant model IDs so --model and --effort never conflict.
	l, err = ResolveWith("agy", "", nil, nil, ResolveOptions{Model: "gemini-4-neon-high", Effort: "low"}, agentsMap)
	if err != nil || !strings.Contains(strings.Join(l.Command[1:], " "), "--model gemini-4-neon --effort low") {
		t.Fatalf("agy normalized conflicting variant = %q, %v", l.Command, err)
	}
	l, err = ResolveWith("agy", "", nil, nil, ResolveOptions{Model: "gemini-4-neon-high", Effort: "high"}, agentsMap)
	if err != nil || !strings.Contains(strings.Join(l.Command[1:], " "), "--model gemini-4-neon-high") || slices.Contains(l.Command, "--effort") {
		t.Fatalf("agy normalized matching variant = %q, %v", l.Command, err)
	}
	l, err = Resolve("omp", "", nil, nil, agentsMap)
	if err != nil || !slices.Contains(l.Command, "--yolo") {
		t.Fatalf("omp = %q, %v", l.Command, err)
	}
	t.Setenv("OPENCODE_CONFIG_CONTENT", `{"theme":"dark"}`)
	l, err = Resolve("opencode", "", nil, nil, agentsMap)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Theme      string            `json:"theme"`
		Permission map[string]string `json:"permission"`
	}
	if err := json.Unmarshal([]byte(l.Env["OPENCODE_CONFIG_CONTENT"]), &doc); err != nil {
		t.Fatalf("opencode env %q: %v", l.Env["OPENCODE_CONFIG_CONTENT"], err)
	}
	if doc.Theme != "dark" || doc.Permission["*"] != "allow" {
		t.Fatalf("opencode config = %+v", doc)
	}

	// LiftYoloArgs removes standalone YOLO flags cleanly.
	cleaned, lifted := LiftYoloArgs("claude", []string{"--verbose", "--dangerously-skip-permissions", "--model", "opus"})
	if !lifted || !slices.Equal(cleaned, []string{"--verbose", "--model", "opus"}) {
		t.Fatalf("lifted=%v cleaned=%q", lifted, cleaned)
	}
	cleaned, lifted = LiftYoloArgs("codex", []string{"--yolo", "-m", "o3"})
	if !lifted || !slices.Equal(cleaned, []string{"-m", "o3"}) {
		t.Fatalf("codex lifted=%v cleaned=%q", lifted, cleaned)
	}

	// Legacy standalone YOLO flags in config Args are honored by EffectiveYolo
	// and cleanly overridden when a profile or launch turns YOLO off.
	legacy := map[string]Agent{
		"claude": {
			Args: []string{"--verbose", "--dangerously-skip-permissions"},
			Profiles: []Profile{
				{Name: "inherit"},
				{Name: "safe", Yolo: &no},
			},
		},
		"opencode": {
			Args: []string{"--yolo"},
		},
	}
	if !legacy["claude"].EffectiveYolo("claude") || !legacy["claude"].Profiles[0].EffectiveYolo("claude", legacy["claude"]) || legacy["claude"].Profiles[1].EffectiveYolo("claude", legacy["claude"]) {
		t.Fatalf("unexpected EffectiveYolo for legacy claude config")
	}
	l, err = Resolve("claude", "inherit", nil, nil, legacy)
	if err != nil || !slices.Equal(l.Command[1:], []string{"--verbose", "--dangerously-skip-permissions"}) {
		t.Fatalf("legacy claude inherit = %q, %v", l.Command, err)
	}
	l, err = Resolve("claude", "safe", nil, nil, legacy)
	if err != nil || !slices.Equal(l.Command[1:], []string{"--verbose"}) {
		t.Fatalf("legacy claude safe should strip flag: %q, %v", l.Command, err)
	}
	l, err = ResolveWith("claude", "", nil, nil, ResolveOptions{Yolo: &no}, legacy)
	if err != nil || !slices.Equal(l.Command[1:], []string{"--verbose"}) {
		t.Fatalf("legacy claude opts.Yolo=false should strip flag: %q, %v", l.Command, err)
	}
	t.Setenv("OPENCODE_CONFIG_CONTENT", "")
	l, err = Resolve("opencode", "", nil, nil, legacy)
	if err != nil || slices.Contains(l.Command, "--yolo") || !strings.Contains(l.Env["OPENCODE_CONFIG_CONTENT"], `"*":"allow"`) {
		t.Fatalf("legacy opencode --yolo should lift to env config: cmd=%q env=%v err=%v", l.Command, l.Env, err)
	}
	l, err = ResolveWith("opencode", "", nil, nil, ResolveOptions{Yolo: &no}, legacy)
	if err != nil || slices.Contains(l.Command, "--yolo") || l.Env["OPENCODE_CONFIG_CONTENT"] != "" {
		t.Fatalf("legacy opencode opts.Yolo=false should strip --yolo and env: cmd=%q env=%v err=%v", l.Command, l.Env, err)
	}
}

func TestResolveAgentDefaultsAndPermissionModes(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"claude", "codex", "agy"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)

	agentsMap := map[string]Agent{
		"claude": {
			Model:      "claude-sonnet-4-6",
			Effort:     "medium",
			Permission: "acceptEdits",
			Profiles: []Profile{
				{Name: "inherit"},
				{Name: "planner", Model: "claude-opus-4-6", Effort: "high", Permission: "plan"},
				{Name: "manual", Permission: "manual"},
				{Name: "auto", Permission: "auto"},
				{Name: "dontask", Permission: "dontAsk"},
				{Name: "yolo", Permission: PermissionYOLO},
				{Name: "normal", Permission: PermissionDefault},
			},
		},
		"codex": {
			Model:      "gpt-5.4",
			Effort:     "high",
			Permission: "readOnly",
			Profiles: []Profile{
				{Name: "edit", Permission: "acceptEdits"},
				{Name: "auto", Permission: "auto"},
				{Name: "yolo", Permission: PermissionYOLO},
				{Name: "normal", Permission: PermissionDefault},
			},
		},
		"agy": {
			Model:      "gemini-4-neon",
			Effort:     "low",
			Permission: "auto",
			Profiles: []Profile{
				{Name: "edit", Permission: "acceptEdits"},
				{Name: "plan", Permission: "plan"},
				{Name: "yolo", Permission: PermissionYOLO},
			},
		},
	}

	cases := []struct {
		agent, profile string
		wantArgs       string
	}{
		{"claude", "", "--model claude-sonnet-4-6 --effort medium --permission-mode acceptEdits"},
		{"claude", "inherit", "--model claude-sonnet-4-6 --effort medium --permission-mode acceptEdits"},
		{"claude", "planner", "--model claude-opus-4-6 --effort high --permission-mode plan"},
		{"claude", "manual", "--model claude-sonnet-4-6 --effort medium --permission-mode default"},
		{"claude", "auto", "--model claude-sonnet-4-6 --effort medium --permission-mode auto"},
		{"claude", "dontask", "--model claude-sonnet-4-6 --effort medium --permission-mode dontAsk"},
		{"claude", "yolo", "--model claude-sonnet-4-6 --effort medium --dangerously-skip-permissions"},
		{"claude", "normal", "--model claude-sonnet-4-6 --effort medium"},
		{"codex", "", `--model gpt-5.4 -c model_reasoning_effort="high" -a on-request -s read-only`},
		{"codex", "edit", `--model gpt-5.4 -c model_reasoning_effort="high" -a on-request -s workspace-write`},
		{"codex", "auto", `--model gpt-5.4 -c model_reasoning_effort="high" --approve-for-me`},
		{"codex", "yolo", `--model gpt-5.4 -c model_reasoning_effort="high" --dangerously-bypass-approvals-and-sandbox`},
		{"codex", "normal", `--model gpt-5.4 -c model_reasoning_effort="high"`},
		{"agy", "", "--model gemini-4-neon --effort low --mode auto"},
		{"agy", "edit", "--model gemini-4-neon --effort low --mode accept-edits"},
		{"agy", "plan", "--model gemini-4-neon --effort low --mode plan"},
		{"agy", "yolo", "--model gemini-4-neon --effort low --dangerously-skip-permissions"},
	}
	for _, tc := range cases {
		l, err := Resolve(tc.agent, tc.profile, nil, nil, agentsMap)
		if err != nil {
			t.Fatalf("%s:%s: %v", tc.agent, tc.profile, err)
		}
		if got := strings.Join(l.Command[1:], " "); got != tc.wantArgs {
			t.Fatalf("%s:%s = %q, want %q", tc.agent, tc.profile, got, tc.wantArgs)
		}
	}

	// EffectiveYolo reflects Permission == "yolo".
	if !agentsMap["claude"].Profiles[5].EffectiveYolo("claude", agentsMap["claude"]) {
		t.Fatal("claude:yolo EffectiveYolo should be true")
	}
	if agentsMap["claude"].EffectiveYolo("claude") {
		t.Fatal("claude acceptEdits EffectiveYolo should be false")
	}

	// Launch option Yolo=false does not drop non-YOLO permission modes, only YOLO.
	no := false
	l, err := ResolveWith("claude", "planner", nil, nil, ResolveOptions{Yolo: &no}, agentsMap)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(l.Command[1:], " "); got != "--model claude-opus-4-6 --effort high --permission-mode plan" {
		t.Fatalf("claude:planner with opts.Yolo=false = %q", got)
	}
	l, err = ResolveWith("claude", "yolo", nil, nil, ResolveOptions{Yolo: &no}, agentsMap)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(l.Command, "--dangerously-skip-permissions") {
		t.Fatalf("claude:yolo with opts.Yolo=false kept skip-permissions: %q", l.Command)
	}
}

func TestResolveProfileReplaceArgs(t *testing.T) {
	onPath(t, "claude")
	agentsMap := map[string]Agent{
		"claude": {
			Args:       []string{"--verbose", "--debug"},
			Permission: "acceptEdits",
			Profiles: []Profile{
				{Name: "append", Args: []string{"--extra"}},
				{Name: "overwrite", Args: []string{"--only-this"}, ReplaceArgs: true},
				{Name: "clear", ReplaceArgs: true},
			},
		},
	}

	l, err := Resolve("claude", "append", []string{"req"}, nil, agentsMap)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(l.Command[1:], " "); got != "--verbose --debug --extra --permission-mode acceptEdits req" {
		t.Fatalf("append = %q", got)
	}

	l, err = Resolve("claude", "overwrite", []string{"req"}, nil, agentsMap)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(l.Command[1:], " "); got != "--only-this --permission-mode acceptEdits req" {
		t.Fatalf("overwrite = %q", got)
	}

	l, err = Resolve("claude", "clear", []string{"req"}, nil, agentsMap)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(l.Command[1:], " "); got != "--permission-mode acceptEdits req" {
		t.Fatalf("clear = %q", got)
	}

	// ReplaceArgs also replaces a legacy YOLO flag sitting in Agent.Args unless
	// Agent.Permission or Agent.Yolo explicitly enabled it.
	legacyReplace := map[string]Agent{
		"claude": {
			Args: []string{"--dangerously-skip-permissions", "--verbose"},
			Profiles: []Profile{
				{Name: "clean", Args: []string{"--quiet"}, ReplaceArgs: true},
			},
		},
	}
	l, err = Resolve("claude", "clean", nil, nil, legacyReplace)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(l.Command[1:], " "); got != "--quiet" {
		t.Fatalf("legacy replace = %q, want --quiet", got)
	}
}
