package policy

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

func TestEvaluatePolicyDenyWinsAndInteractiveProtected(t *testing.T) {
	rules := []Rule{
		{ID: "r1", Name: "Allow git", Enabled: true, Action: ActionAllow, Tool: "Bash", Pattern: "git *"},
		{ID: "r2", Name: "Block force push", Enabled: true, Action: ActionDeny, Tool: "Bash", Pattern: "*--force*", Message: "Force push denied"},
		{ID: "r3", Name: "Allow claude read", Enabled: true, Action: ActionAllow, Agents: []string{"claude"}, Tool: "Read"},
	}

	if d := EvaluateRules(rules, bash("claude", "git status")); !d.Matched || d.Action != ActionAllow {
		t.Fatalf("expected allow for git status, got %+v", d)
	}
	if d := EvaluateRules(rules, bash("claude", "git push --force")); !d.Matched || d.Action != ActionDeny || d.Reason != "Force push denied" {
		t.Fatalf("expected deny to win over allow, got %+v", d)
	}
	read := func(agent string) Call {
		return Call{Agent: agent, Tool: "Read", Kind: protocol.ToolRead, Input: map[string]any{"path": "main.go"}}
	}
	if d := EvaluateRules(rules, read("codex")); d.Matched {
		t.Fatalf("agent filter should not match codex: %+v", d)
	}
	if d := EvaluateRules(rules, read("claude")); !d.Matched || d.Action != ActionAllow {
		t.Fatalf("agent filter should match claude: %+v", d)
	}

	// Interactive approval kinds must never be auto-decided.
	dir := t.TempDir()
	_ = SaveRules(dir, []Rule{{ID: "all", Enabled: true, Action: ActionAllow, Tool: "*"}})
	if d := Evaluate(dir, "", Call{Agent: "claude", Tool: "ExitPlanMode", Kind: protocol.ToolExitPlan}); d.Matched {
		t.Fatalf("plan approval must never be auto-approved: %+v", d)
	}
	if d := Evaluate(dir, "", Call{Agent: "claude", Tool: "AskUserQuestion", Kind: protocol.ToolQuestion}); d.Matched {
		t.Fatalf("question approval must never be auto-approved: %+v", d)
	}
}

// bash is a call of Claude's Bash tool running command.
func bash(agent, command string) Call {
	return Call{Agent: agent, Tool: "Bash", Kind: protocol.ToolShell, Input: map[string]any{"command": command}}
}

// Rules read the whole command, not the line the window shows of it: an
// allow pattern cannot carry a second command past it on a new line, after
// a separator or padding, and a deny pattern finds what hides there.
func TestRulesReadTheWholeCommand(t *testing.T) {
	allow := []Rule{{ID: "a", Enabled: true, Action: ActionAllow, Tool: "Bash", Pattern: "git status*"}}
	deny := []Rule{{ID: "d", Enabled: true, Action: ActionDeny, Tool: "Bash", Pattern: "*--force*"}}
	prefixDeny := []Rule{{ID: "p", Enabled: true, Action: ActionDeny, Tool: "Bash", Pattern: "git push*"}}
	// Past the 120 runes agent.Detail keeps, whitespace collapsed or not.
	padding := " " + strings.Repeat("x", 200) + strings.Repeat(" ", 200)

	for _, cmd := range []string{"git status", "git status --short", "git status && git status -s", "git status | git status"} {
		if d := EvaluateRules(allow, bash("claude", cmd)); !d.Matched || d.Action != ActionAllow {
			t.Errorf("allow git status* did not allow %q: %+v", cmd, d)
		}
	}
	for _, cmd := range []string{
		"git status\nrm -rf ~",
		"git status\r\nrm -rf ~",
		"git status; rm -rf ~",
		"git status && rm -rf ~",
		"git status || rm -rf ~",
		"git status | sh",
		"git status & rm -rf ~",
		"git status # " + padding + "\nrm -rf ~",
		"git status" + padding + "; rm -rf ~",
		"git status $(rm -rf ~)",
		"git status `rm -rf ~`",
		"git status \"$(rm -rf ~)\"",
		"git status > ~/.bashrc",
		"git status <(rm -rf ~)",
		"git status 'unclosed",
		"(rm -rf ~) git status",
	} {
		if d := EvaluateRules(allow, bash("claude", cmd)); d.Matched {
			t.Errorf("allow git status* allowed %q: %+v", cmd, d)
		}
	}
	for _, cmd := range []string{
		"echo ok\ngit push --force",
		"echo ok" + padding + "git push --force",
		"git push" + padding + "--force",
	} {
		if d := EvaluateRules(deny, bash("claude", cmd)); !d.Matched || d.Action != ActionDeny {
			t.Errorf("deny *--force* missed %q: %+v", cmd, d)
		}
	}
	for _, cmd := range []string{
		"echo ok\ngit push origin main",
		"echo ok; git push",
		"echo $(git push)",
		"echo `git push`",
		"echo \"$(git push)\"",
		"(git push)",
		"echo ok" + padding + "&& git  push",
	} {
		if d := EvaluateRules(prefixDeny, bash("claude", cmd)); !d.Matched || d.Action != ActionDeny {
			t.Errorf("deny git push* missed %q: %+v", cmd, d)
		}
	}
	if d := EvaluateRules(prefixDeny, bash("claude", "echo 'git push; is a command'")); d.Matched {
		t.Errorf("deny git push* matched a quoted separator: %+v", d)
	}
}

// Every path a patch touches is read: a deny pattern finds a file past
// the first, and an allow pattern must hold for each of them.
func TestRulesReadEveryPatchedPath(t *testing.T) {
	patch := "*** Begin Patch\n*** Update File: src/a.go\n@@\n-a\n+b\n" +
		"*** Add File: src/b.go\n+x\n" +
		"*** Update File: src/c.go\n*** Move to: .env\n@@\n-y\n+z\n*** End Patch\n"
	call := Call{Agent: "codex", Tool: "apply_patch", Kind: protocol.ToolPatch, Input: map[string]any{"patch": patch}}
	deny := []Rule{{ID: "d", Enabled: true, Action: ActionDeny, Tool: "apply_patch", Pattern: "*.env*"}}
	if d := EvaluateRules(deny, call); !d.Matched || d.Action != ActionDeny {
		t.Fatalf("deny *.env* missed a patch moving a file to .env: %+v", d)
	}
	allow := []Rule{{ID: "a", Enabled: true, Action: ActionAllow, Tool: "apply_patch", Pattern: "src/*"}}
	if d := EvaluateRules(allow, call); d.Matched {
		t.Fatalf("allow src/* allowed a patch that touches .env: %+v", d)
	}
	inSrc := Call{Agent: "codex", Tool: "apply_patch", Kind: protocol.ToolPatch, Input: map[string]any{"patch": "*** Begin Patch\n*** Update File: src/a.go\n*** Add File: src/b.go\n*** End Patch\n"}}
	if d := EvaluateRules(allow, inSrc); !d.Matched || d.Action != ActionAllow {
		t.Fatalf("allow src/* did not allow a patch inside src: %+v", d)
	}
}

// A call of no kind known is read by every string its arguments hold.
func TestRulesReadEveryArgumentOfAnUnknownTool(t *testing.T) {
	call := Call{Agent: "claude", Tool: "mcp__fs__copy", Input: map[string]any{
		"from": "notes.txt",
		"to":   map[string]any{"dir": "out", "files": []any{"a.txt", "secrets/.env"}},
	}}
	deny := []Rule{{ID: "d", Enabled: true, Action: ActionDeny, Tool: "mcp__*", Pattern: "*.env"}}
	if d := EvaluateRules(deny, call); !d.Matched {
		t.Fatalf("deny *.env missed a nested argument: %+v", d)
	}
	allow := []Rule{{ID: "a", Enabled: true, Action: ActionAllow, Tool: "mcp__*", Pattern: "*.txt"}}
	if d := EvaluateRules(allow, call); d.Matched {
		t.Fatalf("allow *.txt allowed a call with other arguments: %+v", d)
	}
	if d := EvaluateRules(allow, Call{Agent: "claude", Tool: "mcp__fs__copy"}); d.Matched {
		t.Fatalf("an allow pattern matched a call with no arguments: %+v", d)
	}
}

func TestProjectPolicyIsDenyOnly(t *testing.T) {
	proj := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proj, ".mako"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := `
[[rule]]
name = "Sneaky allow"
enabled = true
action = "allow"
tool = "*"

[[rule]]
name = "Protect .env"
enabled = true
action = "deny"
tool = "Edit"
pattern = "*.env*"
`
	if err := os.WriteFile(filepath.Join(proj, ".mako", "policy.toml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	rules := LoadProjectDenyRules(proj)
	if len(rules) != 1 || rules[0].Action != ActionDeny || rules[0].Name != "Protect .env" {
		t.Fatalf("expected only deny rule from project policy, got %+v", rules)
	}
}

// A rule whose tool is left empty is refused, never saved as one for every
// tool; one that reaches the file anyway matches nothing.
func TestEmptyToolIsNeverAWildcard(t *testing.T) {
	r := Rule{Enabled: true, Action: ActionAllow, Tool: "  "}.Normalized()
	if r.Tool != "" {
		t.Fatalf("Normalized turned an empty tool into %q", r.Tool)
	}
	if r.Validate() == nil {
		t.Fatal("Validate accepted a rule with no tool")
	}
	dir := t.TempDir()
	if err := SaveRules(dir, []Rule{{Enabled: true, Action: ActionAllow}}); err == nil {
		t.Fatal("SaveRules saved a rule with no tool")
	}
	if _, err := os.Stat(filepath.Join(dir, policiesFileName)); !os.IsNotExist(err) {
		t.Fatalf("SaveRules wrote %s: %v", policiesFileName, err)
	}

	// Written by hand, it loads, but allows nothing.
	if err := os.WriteFile(filepath.Join(dir, policiesFileName), []byte(`[{"id":"x","enabled":true,"action":"allow","tool":""}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if d := Evaluate(dir, "", bash("claude", "rm -rf ~")); d.Matched {
		t.Fatalf("a rule with no tool matched: %+v", d)
	}
}

// A rule names a tool by what it does, so it holds for every agent's tool
// of the kind; Edit names each kind that changes a file.
func TestRulesMatchToolsByKindAcrossAgents(t *testing.T) {
	rules := []Rule{
		{ID: "git", Enabled: true, Action: ActionAllow, Tool: "Bash", Pattern: "git status*"},
		{ID: "env", Enabled: true, Action: ActionDeny, Tool: "Edit", Pattern: "*.env*"},
		{ID: "src", Enabled: true, Action: ActionAllow, Tool: "Read", Pattern: "src/*"},
	}
	shell := map[string]any{"command": "git status"}
	env := map[string]any{"path": "config/.env"}
	for _, c := range []struct {
		call Call
		want Action
	}{
		{Call{Agent: "codex", Tool: "exec_command", Kind: protocol.ToolShell, Input: shell}, ActionAllow},
		{Call{Agent: "agy", Tool: "run_command", Kind: protocol.ToolShell, Input: shell}, ActionAllow},
		{Call{Agent: "opencode", Tool: "interactive_bash", Kind: protocol.ToolShell, Input: shell}, ActionAllow},
		{Call{Agent: "agy", Tool: "replace_file_content", Kind: protocol.ToolEdit, Input: env}, ActionDeny},
		{Call{Agent: "agy", Tool: "edit_file", Kind: protocol.ToolEdit, Input: env}, ActionDeny},
		{Call{Agent: "claude", Tool: "MultiEdit", Kind: protocol.ToolMultiEdit, Input: env}, ActionDeny},
		{Call{Agent: "claude", Tool: "Write", Kind: protocol.ToolWrite, Input: env}, ActionDeny},
		{Call{Agent: "opencode", Tool: "apply_patch", Kind: protocol.ToolPatch, Input: map[string]any{"patch": "*** Begin Patch\n*** Update File: .env\n*** End Patch"}}, ActionDeny},
		{Call{Agent: "claude", Tool: "Grep", Kind: protocol.ToolSearch, Input: map[string]any{"pattern": "TODO", "path": "src/x"}}, ActionAllow},
		{Call{Agent: "codex", Tool: "read", Kind: protocol.ToolRead, Input: map[string]any{"path": "src/main.go"}}, ActionAllow},
	} {
		if d := EvaluateRules(rules, c.call); !d.Matched || d.Action != c.want {
			t.Errorf("%s %s: %+v, want %s", c.call.Agent, c.call.Tool, d, c.want)
		}
	}
	// A rule names only its own kinds.
	for _, call := range []Call{
		{Agent: "codex", Tool: "apply_patch", Kind: protocol.ToolPatch, Input: map[string]any{"patch": "*** Begin Patch\n*** Update File: git status\n*** End Patch"}},
		{Agent: "claude", Tool: "Read", Kind: protocol.ToolRead, Input: env},
		{Agent: "claude", Tool: "mcp__x__run", Input: shell},
	} {
		if d := EvaluateRules(rules, call); d.Matched {
			t.Errorf("%s %s matched %+v", call.Agent, call.Tool, d)
		}
	}
	write := []Rule{{ID: "w", Enabled: true, Action: ActionDeny, Tool: "Write"}}
	if d := EvaluateRules(write, Call{Agent: "agy", Tool: "edit_file", Kind: protocol.ToolEdit, Input: env}); d.Matched {
		t.Errorf("Write matched an edit: %+v", d)
	}
}

// Verdicts recorded at once, as approval hooks served at once record them,
// are all kept.
func TestAppendAuditKeepsConcurrentEntries(t *testing.T) {
	dir := t.TempDir()
	const n = 64
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			if err := AppendAudit(dir, AuditEntry{Session: strconv.Itoa(i), Action: ActionAllow, RuleID: "r"}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	list, err := LoadAudit(dir)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range list {
		seen[e.Session] = true
	}
	if len(list) != n || len(seen) != n {
		t.Fatalf("audit kept %d entries (%d sessions), want %d", len(list), len(seen), n)
	}
}

// Rules are never saved without a configuration directory to hold them,
// as policies.json in the working directory.
func TestSaveRulesNeedsAConfigDir(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := SaveRules(" ", []Rule{{Enabled: true, Action: ActionDeny, Tool: "Bash"}}); err == nil {
		t.Fatal("SaveRules saved rules with no configuration directory")
	}
	if _, err := os.Stat(policiesFileName); !os.IsNotExist(err) {
		t.Fatalf("SaveRules wrote %s in the working directory: %v", policiesFileName, err)
	}
}
