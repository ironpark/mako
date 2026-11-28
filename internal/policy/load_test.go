package policy

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile writes text to path, its folders made.
func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

const projectDeny = `
[[rule]]
id = "env"
enabled = true
action = "deny"
tool = "Bash"
pattern = "*.env*"
`

// Rules that cannot be read allow nothing, as a deny rule might be among
// them, and say why; the deny rules that were read still deny.
func TestUnreadableRulesAllowNothing(t *testing.T) {
	allowAll := `[{"id":"all","enabled":true,"action":"allow","tool":"*"}]`
	for name, c := range map[string]struct {
		global, project string
	}{
		"a broken policies.json":                            {global: `[{"id":`},
		"a deny rule that does not validate":                {global: `[{"id":"x","enabled":true,"action":"block","tool":"Bash"},` + allowAll[1:]},
		"a broken policy.toml":                              {global: allowAll, project: "[[rule]\n"},
		"a deny rule in policy.toml that does not validate": {global: allowAll, project: "[[rule]]\nenabled = true\naction = \"deny\"\n"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg, proj := t.TempDir(), t.TempDir()
			writeFile(t, filepath.Join(cfg, policiesFileName), c.global)
			if c.project != "" {
				writeFile(t, filepath.Join(proj, ".mako", "policy.toml"), c.project)
			}
			d, err := Evaluate(cfg, proj, bash("claude", "ls"))
			if err == nil {
				t.Fatal("no error for rules that cannot be read")
			}
			if d.Matched {
				t.Fatalf("decided %+v with rules that cannot be read", d)
			}
		})
	}

	// A deny rule read still denies.
	cfg, proj := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(cfg, policiesFileName), `[{"id":`)
	writeFile(t, filepath.Join(proj, ".mako", "policy.toml"), projectDeny)
	if d, err := Evaluate(cfg, proj, bash("claude", "cat .env")); err == nil || !d.Matched || d.Action != ActionDeny {
		t.Fatalf("a project deny rule beside a broken policies.json: %+v, %v", d, err)
	}

	// An action in capitals is the action, not a rule lost.
	writeFile(t, filepath.Join(cfg, policiesFileName), `[{"id":"x","enabled":true,"action":"Deny","tool":"Bash","pattern":"rm*"}]`)
	if d, err := Evaluate(cfg, t.TempDir(), bash("claude", "rm -rf ~")); err != nil || d.Action != ActionDeny {
		t.Fatalf("action Deny: %+v, %v", d, err)
	}
}

// A repository's policy file is read at the top of its work tree, and of
// the main work tree for a linked one, for a session in a folder of it.
func TestProjectRulesAreReadFromTheRepository(t *testing.T) {
	main := t.TempDir()
	if err := os.MkdirAll(filepath.Join(main, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(main, ".mako", "policy.toml"), projectDeny)
	sub := filepath.Join(main, "src", "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if rules, err := LoadProjectDenyRules(sub); err != nil || len(rules) != 1 {
		t.Fatalf("from a folder of the work tree: %+v, %v", rules, err)
	}

	wt := t.TempDir()
	writeFile(t, filepath.Join(wt, ".git"), "gitdir: "+filepath.Join(main, ".git", "worktrees", "wt")+"\n")
	writeFile(t, filepath.Join(main, ".git", "worktrees", "wt", "commondir"), "../..\n")
	if rules, err := LoadProjectDenyRules(filepath.Join(wt)); err != nil || len(rules) != 1 {
		t.Fatalf("from a linked work tree: %+v, %v", rules, err)
	}
}

// A repository's policy file is read no further than a bound, and only as
// a regular file.
func TestProjectPolicyReadsAreBounded(t *testing.T) {
	proj := t.TempDir()
	writeFile(t, filepath.Join(proj, ".mako", "policy.toml"), projectDeny+"#"+strings.Repeat("x", maxProjectPolicySize))
	if _, err := LoadProjectDenyRules(proj); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("a policy.toml past the bound: err = %v", err)
	}
	proj = t.TempDir()
	if err := os.MkdirAll(filepath.Join(proj, ".mako", "policy.toml"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProjectDenyRules(proj); err == nil {
		t.Fatal("a policy.toml that is a folder read without an error")
	}
}

// An audit log that cannot be read is kept as it is: a decision is not
// logged over it.
func TestAuditLogIsNeverWrittenOver(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, auditFileName)
	writeFile(t, path, `[{"time":1,"session":"old"`)
	if err := AppendAudit(dir, AuditEntry{Session: "new", Action: ActionAllow, RuleID: "r"}); err == nil {
		t.Fatal("AppendAudit logged over an audit log it could not read")
	}
	if b, _ := os.ReadFile(path); string(b) != `[{"time":1,"session":"old"` {
		t.Fatalf("the audit log now holds %q", b)
	}
}

// A rule's agents are trimmed and its blank names dropped; one of blank
// names alone is refused rather than read as every agent's.
func TestRuleAgentsAreTrimmed(t *testing.T) {
	r := Rule{Enabled: true, Action: ActionAllow, Tool: "Bash", Agents: []string{" claude ", "", "  "}}.Normalized()
	if len(r.Agents) != 1 || r.Agents[0] != "claude" || r.Validate() != nil {
		t.Fatalf("agents = %q, %v", r.Agents, r.Validate())
	}
	blank := Rule{Enabled: true, Action: ActionAllow, Tool: "Bash", Agents: []string{" ", ""}}.Normalized()
	if err := blank.Validate(); !errors.Is(err, ErrBlankAgent) {
		t.Fatalf("a rule of blank agents: %q, %v", blank.Agents, err)
	}
	if d := EvaluateRules([]Rule{blank}, bash("claude", "ls")); d.Matched {
		t.Fatalf("a rule of blank agents matched: %+v", d)
	}
	star := []Rule{{ID: "s", Enabled: true, Action: ActionAllow, Tool: "Bash", Agents: []string{" * "}}}
	if d := EvaluateRules(star, bash("codex", "ls")); !d.Matched {
		t.Fatal(`agents " * " did not hold for codex`)
	}
}

// Rules are bounded: in number, and each field in length.
func TestRulesAreBounded(t *testing.T) {
	dir := t.TempDir()
	many := make([]Rule, MaxRules+1)
	for i := range many {
		many[i] = Rule{Enabled: true, Action: ActionDeny, Tool: "Bash"}
	}
	if err := SaveRules(dir, many); !errors.Is(err, ErrTooMany) {
		t.Fatalf("SaveRules of %d rules: %v", len(many), err)
	}
	if err := SaveRules(dir, many[:MaxRules]); err != nil {
		t.Fatalf("SaveRules of %d rules: %v", MaxRules, err)
	}
	for _, r := range []Rule{
		{Action: ActionDeny, Tool: "Bash", Pattern: strings.Repeat("x", maxPatternLen+1)},
		{Action: ActionDeny, Tool: strings.Repeat("x", maxToolLen+1)},
		{Action: ActionDeny, Tool: "Bash", Message: strings.Repeat("x", maxMessageLen+1)},
		{Action: ActionDeny, Tool: "Bash", Name: strings.Repeat("x", maxNameLen+1)},
		{Action: ActionDeny, Tool: "Bash", Agents: []string{strings.Repeat("x", maxAgentLen+1)}},
		{Action: ActionDeny, Tool: "Bash", Agents: strings.Fields(strings.Repeat("a ", maxAgents+1))},
	} {
		if err := r.Normalized().Validate(); err == nil {
			t.Errorf("Validate accepted %+v", r)
		}
	}
}
