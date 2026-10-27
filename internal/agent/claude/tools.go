package claude

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/protocol"
)

// tools are Claude Code's tools, by their names in lower case.
var tools = map[string]string{
	"bash": protocol.ToolShell,
	"edit": protocol.ToolEdit, "str_replace_based_edit_tool": protocol.ToolEdit, "str_replace": protocol.ToolEdit,
	"multiedit": protocol.ToolMultiEdit,
	"write":     protocol.ToolWrite, "create": protocol.ToolWrite, "notebookedit": protocol.ToolWrite,
	"read": protocol.ToolRead, "view": protocol.ToolRead,
	"grep": protocol.ToolSearch, "glob": protocol.ToolSearch, "ls": protocol.ToolSearch,
	"todowrite": protocol.ToolTodo,
	"webfetch":  protocol.ToolFetch,
	"websearch": protocol.ToolWebSearch,
	"task":      protocol.ToolTask, "agent": protocol.ToolTask,
	"askuserquestion": protocol.ToolQuestion,
}

// toolKind is what a call of the tool name does. The rows the decoder makes
// of a slash command's output and a compaction have kinds of their own, as
// a daemon before drivers named them.
func toolKind(name string) string {
	switch {
	case name == agent.SummaryTool:
		return protocol.ToolSummary
	case strings.HasPrefix(name, "/"):
		return protocol.ToolCommand
	}
	return tools[strings.ToLower(name)]
}

// Tool reads a call's arguments as Claude Code's tools write them.
func (*Agent) Tool(name string, args json.RawMessage) (string, map[string]any) {
	kind := toolKind(name)
	in := agent.Fields(args)
	if kind == "" || in == nil {
		return kind, nil
	}
	path := agent.Str(in, "file_path", "path", "notebook_path")
	switch kind {
	case protocol.ToolShell:
		return kind, agent.Canon(map[string]any{"command": agent.Str(in, "command"), "description": agent.Str(in, "description")})
	case protocol.ToolEdit:
		// The API's text editor names what it does in its command.
		switch agent.Str(in, "command") {
		case "create":
			return protocol.ToolWrite, agent.Canon(map[string]any{"path": path, "content": agent.Str(in, "file_text")})
		case "view":
			return protocol.ToolRead, agent.Canon(map[string]any{"path": path})
		}
		return kind, agent.Canon(map[string]any{"path": path, "old": agent.Str(in, "old_string", "old_str"), "new": agent.Str(in, "new_string", "new_str")})
	case protocol.ToolMultiEdit:
		var edits []any
		for _, e := range agent.List(in, "edits") {
			if e, ok := e.(map[string]any); ok {
				edits = append(edits, map[string]any{"old": agent.Str(e, "old_string"), "new": agent.Str(e, "new_string")})
			}
		}
		return kind, agent.Canon(map[string]any{"path": path, "edits": edits})
	case protocol.ToolWrite:
		return kind, agent.Canon(map[string]any{"path": path, "content": agent.Str(in, "content", "file_text", "new_source")})
	case protocol.ToolRead:
		return kind, agent.Canon(map[string]any{"path": path, "offset": in["offset"], "limit": in["limit"]})
	case protocol.ToolSearch:
		return kind, agent.Canon(map[string]any{"pattern": agent.Str(in, "pattern"), "path": agent.Str(in, "path"), "glob": agent.Str(in, "glob"), "type": agent.Str(in, "type"), "mode": agent.Str(in, "output_mode")})
	case protocol.ToolTodo:
		return kind, agent.Canon(map[string]any{"items": agent.Items(agent.List(in, "todos"), "content", "status")})
	case protocol.ToolFetch:
		return kind, agent.Canon(map[string]any{"url": agent.Str(in, "url"), "prompt": agent.Str(in, "prompt")})
	case protocol.ToolWebSearch:
		return kind, agent.Canon(map[string]any{"query": agent.Str(in, "query"), "allowed_domains": in["allowed_domains"], "blocked_domains": in["blocked_domains"]})
	case protocol.ToolTask:
		return kind, agent.Canon(map[string]any{"description": agent.Str(in, "description"), "prompt": agent.Str(in, "prompt"), "agent": agent.Str(in, "subagent_type")})
	case protocol.ToolQuestion:
		return kind, agent.Question(in, "question")
	}
	return kind, nil
}

// reminder is a note Claude Code adds to a tool's result for the model.
var reminder = regexp.MustCompile(`(?s)\s*<system-reminder>.*?</system-reminder>\s*`)

// Output is a result without the notes Claude Code adds for the model,
// and a read's text without the number Read puts before each line.
func (*Agent) Output(kind, text string) (string, int) {
	if strings.Contains(text, "<system-reminder>") {
		text = reminder.ReplaceAllString(text, "\n")
	}
	if kind == protocol.ToolRead {
		if plain, ok := unnumbered(text); ok {
			text = plain
		}
	}
	return text, -1
}

// unnumbered is a file Read gave back without the number before each line,
// "   12→text"; ok is false for text not numbered so.
func unnumbered(text string) (string, bool) {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, l := range lines {
		num, rest, found := strings.Cut(l, "→")
		if !found {
			num, rest, found = strings.Cut(l, "\t")
		}
		if _, err := strconv.Atoi(strings.TrimSpace(num)); !found || err != nil {
			return "", false
		}
		lines[i] = rest
	}
	return strings.Join(lines, "\n"), true
}
