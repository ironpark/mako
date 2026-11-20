package agy

import (
	"os"
	"path/filepath"

	"github.com/ironpark/mako/internal/agent"
)

// builtins are agy's built-in slash commands.
var builtins = []agent.Command{
	{Name: "add-dir", Description: "Add a directory to the workspace", Args: "<path>"},
	{Name: "agents", Description: "List available custom agents"},
	{Name: "artifact", Description: "View and review artifacts"},
	{Name: "boost", Description: "Deep thinking, strategic planning and rigorous verification"},
	{Name: "btw", Description: "Ask a side question without interrupting the current task"},
	{Name: "changelog", Description: "Show release notes and changes"},
	{Name: "compact", Description: "Compact conversation context to free up the context window"},
	{Name: "config", Description: "Open settings"},
	{Name: "effort", Description: "Set reasoning effort level", Args: "[level]"},
	{Name: "exit", Description: "Exit Antigravity CLI"},
	{Name: "goal", Description: "Run a thorough long-running task until achieved"},
	{Name: "grill-me", Description: "Align on a plan through an interactive design interview"},
	{Name: "help", Description: "Show help and available commands"},
	{Name: "learn", Description: "Persist learned behavior for future tasks"},
	{Name: "mcp", Description: "Manage MCP servers"},
	{Name: "model", Description: "Switch the active AI model", Args: "[model]"},
	{Name: "plan", Description: "Step-by-step planning before execution"},
	{Name: "plugin", Description: "Manage plugins"},
	{Name: "quit", Description: "Exit Antigravity CLI"},
	{Name: "resume", Description: "Resume a previous conversation"},
	{Name: "schedule", Description: "Run an instruction on a recurring schedule or timer"},
	{Name: "settings", Description: "Open settings"},
	{Name: "status", Description: "Show current session status"},
	{Name: "teamwork", Description: "Coordinate a team of autonomous agents"},
}

// Commands returns agy's built-in slash commands along with discovered user
// and project skills.
func (*Agent) Commands(project string, env map[string]string) []agent.Command {
	out := append([]agent.Command(nil), builtins...)
	home := env["HOME"]
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if home != "" {
		out = append(out, agent.SkillDir(filepath.Join(home, ".gemini", "config", "skills"), "", "user")...)
	}
	if project != "" {
		for _, dir := range []string{".agents", ".agent"} {
			out = append(out, agent.SkillDir(filepath.Join(project, dir, "skills"), "", "project")...)
		}
	}
	return out
}
