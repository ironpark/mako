package codex

import (
	"bufio"
	"cmp"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ironpark/mako/internal/agent"
)

// builtins are Codex's own slash commands, as codex-cli 0.171.0 lists them.
var builtins = []agent.Command{
	{Name: "model", Description: "Choose what model and reasoning effort to use"},
	{Name: "presets", Description: "Run or save reusable prompts and slash commands"},
	{Name: "ide", Description: "Include current selection, open files, and other context from your IDE"},
	{Name: "permissions", Description: "Choose what Codex is allowed to do"},
	{Name: "keymap", Description: "Remap TUI shortcuts"},
	{Name: "vim", Description: "Toggle Vim mode for the composer"},
	{Name: "experimental", Description: "Toggle experimental features"},
	{Name: "approve", Description: "Approve one retry of a recent auto-review denial"},
	{Name: "memories", Description: "Configure memory use and generation"},
	{Name: "skills", Description: "Use skills to improve how Codex performs specific tasks"},
	{Name: "import", Description: "Import setup, this project, and recent chats from Claude Code"},
	{Name: "hooks", Description: "View and manage lifecycle hooks"},
	{Name: "review", Description: "Review current changes and find issues"},
	{Name: "rename", Description: "Rename the current thread"},
	{Name: "new", Description: "Start a new chat during a conversation"},
	{Name: "archive", Description: "Archive this session"},
	{Name: "delete", Description: "Permanently delete this session"},
	{Name: "resume", Description: "Resume a saved chat"},
	{Name: "fork", Description: "Fork the current chat"},
	{Name: "worktree", Description: "Start or continue a conversation in a new worktree"},
	{Name: "app", Description: "Continue this session in the Desktop app"},
	{Name: "init", Description: "Create an AGENTS.md file with instructions for Codex"},
	{Name: "compact", Description: "Summarize conversation to prevent hitting the context limit"},
	{Name: "config", Description: "Configure Codex preferences"},
	{Name: "recap", Description: "Summarize the current conversation now"},
	{Name: "plan", Description: "Switch to Plan mode"},
	{Name: "voice", Description: "Start or stop voice; use /voice settings to choose a voice"},
	{Name: "goal", Description: "Set or view the goal for a long-running task"},
	{Name: "agents", Description: "Open the agent command center"},
	{Name: "subagents", Description: "Switch between this session's subagents"},
	{Name: "side", Description: "Start a side conversation"},
	{Name: "btw", Description: "Start a side conversation"},
	{Name: "copy", Description: "Copy the last response or part of it"},
	{Name: "export", Description: "Export the conversation as markdown"},
	{Name: "raw", Description: "Toggle raw scrollback mode for copy-friendly terminal selection"},
	{Name: "tui", Description: "Choose the TUI mode for the next launch"},
	{Name: "diff", Description: "Show git diff (including untracked files)"},
	{Name: "mention", Description: "Mention a file"},
	{Name: "status", Description: "Show current session configuration and token usage"},
	{Name: "daemon", Description: "Manage the local background server"},
	{Name: "rc", Description: "Manage remote control for the local background server"},
	{Name: "warnings", Description: "View retained warnings and diagnostic details"},
	{Name: "cd", Description: "Change the current working directory"},
	{Name: "pwd", Description: "Show the current working directory"},
	{Name: "usage", Description: "View account usage or use a usage limit reset"},
	{Name: "debug-config", Description: "Show config layers and requirement sources for debugging"},
	{Name: "title", Description: "Configure which items appear in the terminal title"},
	{Name: "statusline", Description: "Configure which items appear in the status line"},
	{Name: "theme", Description: "Choose a syntax highlighting theme"},
	{Name: "mcp", Description: "List MCP tools; /mcp verbose or /mcp login <name> [--environment <id>]"},
	{Name: "apps", Description: "Manage apps"},
	{Name: "plugins", Description: "Browse plugins"},
	{Name: "logout", Description: "Log out of Codex"},
	{Name: "quit", Description: "Exit Codex"},
	{Name: "exit", Description: "Exit Codex"},
	{Name: "feedback", Description: "Send logs to maintainers or save a ZIP"},
	{Name: "rollout", Description: "Print the rollout file path"},
	{Name: "ps", Description: "List background terminals"},
	{Name: "stop", Description: "Stop all background terminals"},
	{Name: "clear", Description: "Clear the terminal and start a new chat"},
}

// Commands are Codex's own commands, the user's custom prompts (prompts:name),
// user and system skills under CODEX_HOME/skills (~/.codex/skills), and
// project skills under <project>/.agents/skills.
func (*Agent) Commands(project string, env map[string]string) []agent.Command {
	root := place.Root(env)
	out := append([]agent.Command(nil), builtins...)
	out = append(out, agent.CommandDir(filepath.Join(root, "prompts"), "prompts:", "user")...)
	skillsRoot := filepath.Join(root, "skills")
	out = append(out, discoverSkills(skillsRoot, "user")...)
	out = append(out, discoverSkills(filepath.Join(skillsRoot, ".system"), "system")...)
	if project != "" {
		out = append(out, discoverSkills(filepath.Join(project, ".agents", "skills"), "project")...)
	}
	return out
}

const (
	maxSkillDepth = 6
	maxSkillFile  = 16 << 10
)

// discoverSkills walks dir (including nested subdirectories and symlinked skill
// folders, skipping hidden dot-entries) and returns a Command for each skill
// directory containing SKILL.md.
func discoverSkills(dir, source string) []agent.Command {
	visited := map[string]bool{}
	var out []agent.Command
	walkSkills(dir, source, 0, visited, &out)
	return out
}

func walkSkills(dir, source string, depth int, visited map[string]bool, out *[]agent.Command) {
	if depth > maxSkillDepth {
		return
	}
	realDir := dir
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		realDir = resolved
	}
	if visited[realDir] {
		return
	}
	visited[realDir] = true

	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		child := filepath.Join(dir, name)
		info, err := os.Stat(child) // follows symlinks
		if err != nil || !info.IsDir() {
			continue
		}
		skillFile := filepath.Join(child, "SKILL.md")
		if meta, first, ok := parseSkillFrontMatter(skillFile); ok {
			if meta["user-invocable"] == "false" {
				continue
			}
			*out = append(*out, agent.Command{
				Name:        cmp.Or(meta["name"], name),
				Description: cmp.Or(meta["description"], meta["short-description"], first),
				Args:        meta["argument-hint"],
				Source:      source,
			})
			// A skill folder is a leaf package (its subdirectories are helper
			// resources like scripts/, examples/, or references/).
			continue
		}
		walkSkills(child, source, depth+1, visited, out)
	}
}

// parseSkillFrontMatter reads a SKILL.md file's YAML frontmatter (supporting
// quoted scalars, folded/literal block scalars, and metadata.short-description)
// along with the first non-empty body line as a description fallback.
func parseSkillFrontMatter(path string) (meta map[string]string, first string, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", false
	}
	defer f.Close()

	sc := bufio.NewScanner(io.LimitReader(f, maxSkillFile))
	meta = map[string]string{}
	inFront := false
	inMetadata := false
	blockKey := ""
	var blockLines []string

	flushBlock := func() {
		if blockKey != "" {
			meta[blockKey] = agent.OneLine(strings.Join(blockLines, " "), 200)
			blockKey = ""
			blockLines = nil
		}
	}

	for i := 0; sc.Scan(); i++ {
		raw := sc.Text()
		trimmed := strings.TrimSpace(raw)
		switch {
		case i == 0 && trimmed == "---":
			inFront = true
		case inFront && trimmed == "---":
			flushBlock()
			inFront = false
			inMetadata = false
		case inFront:
			indented := len(raw) > 0 && (raw[0] == ' ' || raw[0] == '\t')
			if blockKey != "" {
				if indented || trimmed == "" {
					if trimmed != "" {
						blockLines = append(blockLines, trimmed)
					}
					continue
				}
				flushBlock()
			}
			if indented {
				if inMetadata {
					if k, v, has := strings.Cut(trimmed, ":"); has && strings.TrimSpace(k) == "short-description" {
						if val := unquoteYAML(v); val != "" {
							meta["short-description"] = val
						}
					}
				}
				continue
			}
			inMetadata = false
			k, v, has := strings.Cut(raw, ":")
			if !has {
				continue
			}
			key := strings.TrimSpace(k)
			val := strings.TrimSpace(v)
			if key == "metadata" && val == "" {
				inMetadata = true
				continue
			}
			if val == ">" || val == "|" || val == ">-" || val == "|-" {
				blockKey = key
				blockLines = nil
				continue
			}
			meta[key] = unquoteYAML(val)
		case trimmed != "":
			return meta, agent.OneLine(strings.TrimLeft(trimmed, "# "), 200), true
		}
	}
	flushBlock()
	return meta, "", true
}

func unquoteYAML(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && ((s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'')) {
		return s[1 : len(s)-1]
	}
	return s
}
