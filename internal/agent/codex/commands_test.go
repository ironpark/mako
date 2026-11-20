package codex

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ironpark/mako/internal/agent"
)

func TestCodexBuiltinSlashCommands(t *testing.T) {
	seen := map[string]agent.Command{}
	for _, c := range builtins {
		if c.Name == "" {
			t.Fatal("builtin command with empty name")
		}
		if c.Description == "" {
			t.Errorf("builtin /%s has empty description", c.Name)
		}
		if c.Source != "" {
			t.Errorf("builtin /%s has non-empty source %q", c.Name, c.Source)
		}
		if _, dup := seen[c.Name]; dup {
			t.Errorf("duplicate builtin command /%s", c.Name)
		}
		seen[c.Name] = c
	}

	// Verify key codex-cli 0.171.0 built-in slash commands are present.
	for _, name := range []string{
		"model", "permissions", "plan", "skills", "hooks", "plugins",
		"fork", "usage", "statusline", "btw", "side", "goal", "agents",
		"subagents", "rename", "clear", "review", "new", "resume", "init",
		"compact", "diff", "mention", "status", "mcp", "logout", "quit",
		"exit", "worktree", "archive", "delete", "config", "memories",
		"presets", "copy", "export", "ps", "stop", "cd", "pwd", "apps",
	} {
		if _, ok := seen[name]; !ok {
			t.Errorf("missing expected codex 0.171.0 builtin /%s", name)
		}
	}

	// Removed commands from older Codex releases must not remain.
	for _, removed := range []string{"approvals", "undo"} {
		if _, ok := seen[removed]; ok {
			t.Errorf("stale command /%s should not be in builtins", removed)
		}
	}
}

func TestCodexCommandsDiscoverPromptsAndSkills(t *testing.T) {
	codexHome := t.TempDir()
	project := t.TempDir()
	external := t.TempDir()

	write := func(path, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Existing prompts discovery under CODEX_HOME/prompts.
	write(filepath.Join(codexHome, "prompts", "review-pr.md"), "---\ndescription: Review a pull request\nargument-hint: <pr>\n---\nBody")
	write(filepath.Join(codexHome, "prompts", "go", "bench.md"), "# Run Go benchmarks\n\nSteps.")

	// User skills under CODEX_HOME/skills: flat, nested, multi-line description,
	// metadata.short-description fallback, body fallback, user-invocable: false,
	// helper subdir that must not be scanned, hidden dir that must be ignored.
	write(filepath.Join(codexHome, "skills", "commit", "SKILL.md"), "---\nname: commit\ndescription: \"Stage and create a commit\"\nargument-hint: \"[scope]\"\n---\n# Commit\n")
	write(filepath.Join(codexHome, "skills", "commit", "scripts", "nested", "SKILL.md"), "---\nname: helper-not-a-skill\ndescription: Should not be scanned inside a skill package\n---\n")
	write(filepath.Join(codexHome, "skills", "frontend", "react", "SKILL.md"), "---\nname: react-review\ndescription: >\n  Audit React components\n  for render issues.\n---\nBody\n")
	write(filepath.Join(codexHome, "skills", "short-desc", "SKILL.md"), "---\nname: short-desc\nmetadata:\n  short-description: Short metadata description\n---\n")
	write(filepath.Join(codexHome, "skills", "body-only", "SKILL.md"), "# Fallback heading description\n\nDetails.\n")
	write(filepath.Join(codexHome, "skills", "internal-only", "SKILL.md"), "---\nname: internal-only\ndescription: Hidden from users\nuser-invocable: false\n---\n")
	write(filepath.Join(codexHome, "skills", ".hidden", "secret", "SKILL.md"), "---\nname: secret\ndescription: Hidden dot directory\n---\n")

	// System skills under CODEX_HOME/skills/.system.
	write(filepath.Join(codexHome, "skills", ".system", "skill-creator", "SKILL.md"), "---\nname: skill-creator\ndescription: Create or update a Codex skill\n---\n")

	// Symlinked user skill + symlink cycle guard.
	write(filepath.Join(external, "linked-skill", "SKILL.md"), "---\nname: linked-skill\ndescription: Loaded via symlink\n---\n")
	if err := os.Symlink(filepath.Join(external, "linked-skill"), filepath.Join(codexHome, "skills", "linked-skill")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(codexHome, "skills"), filepath.Join(codexHome, "skills", "loop")); err != nil {
		t.Fatal(err)
	}

	// Project skills under <project>/.agents/skills (direct and nested).
	write(filepath.Join(project, ".agents", "skills", "deploy", "SKILL.md"), "---\nname: deploy\ndescription: Deploy this project\n---\n")
	write(filepath.Join(project, ".agents", "skills", "ops", "migrate", "SKILL.md"), "---\ndescription: Run database migrations\n---\n")

	cmds := New().Commands(project, map[string]string{"CODEX_HOME": codexHome})
	got := map[string]agent.Command{}
	for _, c := range cmds {
		got[c.Name] = c
	}

	want := map[string]agent.Command{
		"permissions":       {Name: "permissions", Description: "Choose what Codex is allowed to do"},
		"prompts:review-pr": {Name: "prompts:review-pr", Description: "Review a pull request", Args: "<pr>", Source: "user"},
		"prompts:go:bench":  {Name: "prompts:go:bench", Description: "Run Go benchmarks", Source: "user"},
		"commit":            {Name: "commit", Description: "Stage and create a commit", Args: "[scope]", Source: "user"},
		"react-review":      {Name: "react-review", Description: "Audit React components for render issues.", Source: "user"},
		"short-desc":        {Name: "short-desc", Description: "Short metadata description", Source: "user"},
		"body-only":         {Name: "body-only", Description: "Fallback heading description", Source: "user"},
		"linked-skill":      {Name: "linked-skill", Description: "Loaded via symlink", Source: "user"},
		"skill-creator":     {Name: "skill-creator", Description: "Create or update a Codex skill", Source: "system"},
		"deploy":            {Name: "deploy", Description: "Deploy this project", Source: "project"},
		"migrate":           {Name: "migrate", Description: "Run database migrations", Source: "project"},
	}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("%s: got %+v, want %+v", name, got[name], w)
		}
	}

	for _, excluded := range []string{"internal-only", "secret", "helper-not-a-skill"} {
		if c, ok := got[excluded]; ok {
			t.Errorf("%s should not be listed, got %+v", excluded, c)
		}
	}
}

func TestCodexCommandsDefaultHomeSkills(t *testing.T) {
	home := t.TempDir()
	skillPath := filepath.Join(home, ".codex", "skills", "default-home-skill", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skillPath, []byte("---\nname: default-home-skill\ndescription: Found via HOME/.codex/skills\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmds := New().Commands("", map[string]string{"HOME": home})
	found := false
	for _, c := range cmds {
		if c.Name == "default-home-skill" {
			found = true
			if c.Source != "user" || c.Description != "Found via HOME/.codex/skills" {
				t.Fatalf("unexpected command: %+v", c)
			}
		}
	}
	if !found {
		t.Fatalf("expected default-home-skill discovered via HOME/.codex/skills")
	}
}
