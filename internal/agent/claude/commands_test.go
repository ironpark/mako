package claude

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ironpark/mako/internal/agent"
)

// Claude Code's commands are its own, the user's and the project's
// commands and skills, and those of the plugins enabled for the project.
func TestCommandsAreFoundWhereClaudeCodeKeepsThem(t *testing.T) {
	root, project, plugin := t.TempDir(), t.TempDir(), t.TempDir()
	write := func(path, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(root, "commands", "frontend", "review.md"), "---\ndescription: Review the UI\nargument-hint: [file]\n---\nBody")
	write(filepath.Join(root, "skills", "commit", "SKILL.md"), "---\nname: commit\ndescription: \"Make a commit\"\n---\n")
	write(filepath.Join(root, "skills", "hidden", "SKILL.md"), "---\nuser-invocable: false\n---\n")
	write(filepath.Join(project, ".claude", "commands", "deploy.md"), "# Deploy the app\n\nSteps.")
	write(filepath.Join(plugin, "commands", "lint.md"), "Lint it")
	write(filepath.Join(root, "plugins", "installed_plugins.json"), `{"version":2,"plugins":{
		"tools@market":[{"scope":"user","installPath":"`+plugin+`"}],
		"off@market":[{"scope":"user","installPath":"`+plugin+`"}]}}`)
	write(filepath.Join(project, ".claude", "settings.json"), `{"enabledPlugins":{"tools@market":true,"off@market":false}}`)

	got := map[string]agent.Command{}
	for _, c := range New().Commands(project, map[string]string{"CLAUDE_CONFIG_DIR": root}) {
		got[c.Name] = c
	}
	want := map[string]agent.Command{
		"compact":         {Name: "compact", Description: builtins[3].Description, Args: "[instructions]"},
		"frontend:review": {Name: "frontend:review", Description: "Review the UI", Args: "[file]", Source: "user"},
		"commit":          {Name: "commit", Description: "Make a commit", Source: "user"},
		"deploy":          {Name: "deploy", Description: "Deploy the app", Source: "project"},
		"tools:lint":      {Name: "tools:lint", Description: "Lint it", Source: "tools"},
	}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("%s: %+v, want %+v", name, got[name], w)
		}
	}
	for _, name := range []string{"hidden", "off:lint"} {
		if _, ok := got[name]; ok {
			t.Errorf("%s is listed", name)
		}
	}
}
