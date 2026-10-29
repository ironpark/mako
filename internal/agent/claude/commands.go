package claude

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/ironpark/mako/internal/agent"
)

// builtins are Claude Code's own commands, as its /help lists them.
var builtins = []agent.Command{
	{Name: "add-dir", Description: "Add a working directory", Args: "<path>"},
	{Name: "agents", Description: "Manage agent configurations"},
	{Name: "clear", Description: "Clear conversation history and free up context"},
	{Name: "compact", Description: "Clear conversation history but keep a summary in context", Args: "[instructions]"},
	{Name: "config", Description: "Open the settings panel"},
	{Name: "context", Description: "Show the current context usage"},
	{Name: "cost", Description: "Show the total cost and duration of the current session"},
	{Name: "doctor", Description: "Diagnose and verify the installation and settings"},
	{Name: "exit", Description: "Exit the REPL"},
	{Name: "export", Description: "Export the current conversation to a file or the clipboard", Args: "[filename]"},
	{Name: "help", Description: "Show help and the available commands"},
	{Name: "hooks", Description: "Manage hook configurations for tool events"},
	{Name: "ide", Description: "Manage IDE integrations and show status"},
	{Name: "init", Description: "Initialize a new CLAUDE.md file with codebase documentation"},
	{Name: "login", Description: "Sign in with an Anthropic account"},
	{Name: "logout", Description: "Sign out from an Anthropic account"},
	{Name: "mcp", Description: "Manage MCP servers"},
	{Name: "memory", Description: "Edit Claude memory files"},
	{Name: "model", Description: "Set the AI model", Args: "[model]"},
	{Name: "output-style", Description: "Set the output style"},
	{Name: "permissions", Description: "Manage allow and deny tool permission rules"},
	{Name: "plugin", Description: "Manage plugins"},
	{Name: "pr-comments", Description: "Get comments from a GitHub pull request"},
	{Name: "release-notes", Description: "View the release notes"},
	{Name: "resume", Description: "Resume a conversation", Args: "[conversation]"},
	{Name: "review", Description: "Review a pull request"},
	{Name: "rewind", Description: "Restore the code and/or conversation to a previous point"},
	{Name: "security-review", Description: "Complete a security review of the pending changes on the current branch"},
	{Name: "status", Description: "Show the version, model, account and connectivity"},
	{Name: "statusline", Description: "Set up the status line"},
	{Name: "terminal-setup", Description: "Install the Shift+Enter key binding for newlines"},
	{Name: "todos", Description: "List the current todo items"},
	{Name: "usage", Description: "Show plan usage limits"},
	{Name: "vim", Description: "Toggle between Vim and normal editing modes"},
}

// Commands are Claude Code's own commands, then the user's and the
// project's commands and skills, then those of the plugins enabled for
// the project.
func (*Agent) Commands(project string, env map[string]string) []agent.Command {
	root := place.Root(env)
	out := append([]agent.Command(nil), builtins...)
	// A folder of commands and skills, each named after prefix.
	add := func(dir, prefix, source string) {
		out = append(out, agent.CommandDir(filepath.Join(dir, "commands"), prefix, source)...)
		out = append(out, agent.SkillDir(filepath.Join(dir, "skills"), prefix, source)...)
	}
	add(root, "", "user")
	if project != "" {
		add(filepath.Join(project, ".claude"), "", "project")
	}
	for _, p := range plugins(root, project) {
		add(p.path, p.name+":", p.name)
	}
	return out
}

type plugin struct{ name, path string }

// plugins are the plugins installed for the user, or for project, that
// its settings enable.
func plugins(root, project string) []plugin {
	data, err := os.ReadFile(filepath.Join(root, "plugins", "installed_plugins.json"))
	if err != nil {
		return nil
	}
	var installed struct {
		Plugins map[string][]struct {
			Scope       string `json:"scope"`
			ProjectPath string `json:"projectPath"`
			InstallPath string `json:"installPath"`
		} `json:"plugins"`
	}
	if json.Unmarshal(data, &installed) != nil {
		return nil
	}
	settings := []string{filepath.Join(root, "settings.json")}
	if project != "" {
		settings = append(settings, filepath.Join(project, ".claude", "settings.json"), filepath.Join(project, ".claude", "settings.local.json"))
	}
	enabled := map[string]bool{}
	for _, path := range settings { // the later, nearer the project, wins
		var s struct {
			Enabled map[string]bool `json:"enabledPlugins"`
		}
		if data, err := os.ReadFile(path); err == nil && json.Unmarshal(data, &s) == nil {
			maps.Copy(enabled, s.Enabled)
		}
	}
	var out []plugin
	for id, installs := range installed.Plugins {
		if !enabled[id] {
			continue
		}
		name, _, _ := strings.Cut(id, "@")
		for _, in := range installs {
			if in.Scope == "user" || (project != "" && in.ProjectPath == project) {
				out = append(out, plugin{name: name, path: in.InstallPath})
				break
			}
		}
	}
	return out
}
