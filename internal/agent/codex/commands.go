package codex

import (
	"path/filepath"

	"github.com/ironpark/mako/internal/agent"
)

// builtins are Codex's own commands, as its slash popup lists them.
var builtins = []agent.Command{
	{Name: "model", Description: "Choose the model and its reasoning effort"},
	{Name: "approvals", Description: "Choose what Codex can do without approval"},
	{Name: "review", Description: "Review the current changes and find issues"},
	{Name: "new", Description: "Start a new chat during a conversation"},
	{Name: "resume", Description: "Resume a saved chat"},
	{Name: "init", Description: "Create an AGENTS.md file with instructions for Codex"},
	{Name: "compact", Description: "Summarize the conversation to free up context"},
	{Name: "undo", Description: "Undo the last turn's changes"},
	{Name: "diff", Description: "Show the git diff, including untracked files"},
	{Name: "mention", Description: "Mention a file"},
	{Name: "status", Description: "Show the session's configuration and token usage"},
	{Name: "mcp", Description: "List the configured MCP tools"},
	{Name: "logout", Description: "Log out of Codex"},
	{Name: "quit", Description: "Exit Codex"},
}

// Commands are Codex's own commands, then the user's custom prompts, which
// it takes as prompts:name.
func (*Agent) Commands(_ string, env map[string]string) []agent.Command {
	root := place.Root(env)
	out := append([]agent.Command(nil), builtins...)
	return append(out, agent.CommandDir(filepath.Join(root, "prompts"), "prompts:", "user")...)
}
