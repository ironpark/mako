package policy

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

func TestEvaluatePolicyDenyWinsAndInteractiveProtected(t *testing.T) {
	rules := []Rule{
		{ID: "r1", Name: "Allow git", Enabled: true, Action: ActionAllow, Tool: "Bash", Pattern: "git *"},
		{ID: "r2", Name: "Block force push", Enabled: true, Action: ActionDeny, Tool: "Bash", Pattern: "*--force*", Message: "Force push denied"},
		{ID: "r3", Name: "Allow claude read", Enabled: true, Action: ActionAllow, Agents: []string{"claude"}, Tool: "Read"},
	}

	if d := EvaluateRules(rules, "claude", "Bash", "git status"); !d.Matched || d.Action != ActionAllow {
		t.Fatalf("expected allow for git status, got %+v", d)
	}
	if d := EvaluateRules(rules, "claude", "Bash", "git push --force"); !d.Matched || d.Action != ActionDeny || d.Reason != "Force push denied" {
		t.Fatalf("expected deny to win over allow, got %+v", d)
	}
	if d := EvaluateRules(rules, "codex", "Read", "main.go"); d.Matched {
		t.Fatalf("agent filter should not match codex: %+v", d)
	}
	if d := EvaluateRules(rules, "claude", "Read", "main.go"); !d.Matched || d.Action != ActionAllow {
		t.Fatalf("agent filter should match claude: %+v", d)
	}

	// Interactive approval kinds must never be auto-decided.
	dir := t.TempDir()
	_ = SaveRules(dir, []Rule{{ID: "all", Enabled: true, Action: ActionAllow, Tool: "*"}})
	if d := Evaluate(dir, "", "claude", protocol.ToolExitPlan, "ExitPlanMode", ""); d.Matched {
		t.Fatalf("plan approval must never be auto-approved: %+v", d)
	}
	if d := Evaluate(dir, "", "claude", protocol.ToolQuestion, "AskUserQuestion", ""); d.Matched {
		t.Fatalf("question approval must never be auto-approved: %+v", d)
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

func TestLifecycleHooksExecution(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell test is unix-only")
	}
	cfgDir := t.TempDir()
	proj := t.TempDir()
	marker := filepath.Join(proj, "hook.out")
	hooks := []Hook{{
		ID:      "hk-1",
		Name:    "Record turn",
		Enabled: true,
		Event:   EventTurnDone,
		Command: `printf '%s|%s|%s' "$AGENT_MAKO_SESSION" "$AGENT_MAKO_AGENT" "$AGENT_MAKO_EVENT" > hook.out`,
	}}
	if err := SaveHooks(cfgDir, hooks); err != nil {
		t.Fatal(err)
	}
	if err := RunHooks(context.Background(), cfgDir, HookContext{
		SessionID: "s-42",
		Agent:     "claude",
		Project:   proj,
		Event:     EventTurnDone,
	}); err != nil {
		t.Fatalf("RunHooks: %v", err)
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "s-42|claude|on_turn_done" {
		t.Fatalf("marker = %q, err=%v", data, err)
	}
}

func TestBuildSeatbeltProfile(t *testing.T) {
	prof := BuildSeatbeltProfile("/tmp/my-proj", []string{"/Users/test/.claude"})
	if !strings.Contains(prof, "(deny file-write*)") ||
		!strings.Contains(prof, `"/tmp/my-proj"`) ||
		!strings.Contains(prof, `"/Users/test/.claude"`) {
		t.Fatalf("unexpected seatbelt profile:\n%s", prof)
	}
}
