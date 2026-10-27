package opencode

import (
	"encoding/json"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/protocol"
)

// tools are OpenCode's, and OmO's.
var tools = map[string]string{
	"bash": protocol.ToolShell, "interactive_bash": protocol.ToolShell,
	"read": protocol.ToolRead, "edit": protocol.ToolEdit, "write": protocol.ToolWrite,
	"apply_patch": protocol.ToolPatch,
	"glob":        protocol.ToolSearch, "grep": protocol.ToolSearch,
	"webfetch": protocol.ToolFetch, "websearch": protocol.ToolWebSearch,
	"todowrite": protocol.ToolTodo,
	"task":      protocol.ToolTask, "call_omo_agent": protocol.ToolTask,
	"question": protocol.ToolQuestion,
}

// Tool reads a call's arguments as OpenCode's and OmO's tools write them.
func (*Agent) Tool(name string, args json.RawMessage) (string, map[string]any) {
	kind := tools[name]
	in := agent.Fields(args)
	if kind == "" || in == nil {
		return kind, nil
	}
	path := agent.Str(in, "filePath", "path")
	switch kind {
	case protocol.ToolShell:
		return kind, agent.Canon(map[string]any{"command": agent.Str(in, "command", "tmux_command"), "cwd": agent.Str(in, "workdir"), "description": agent.Str(in, "description")})
	case protocol.ToolEdit:
		return kind, agent.Canon(map[string]any{"path": path, "old": agent.Str(in, "oldString"), "new": agent.Str(in, "newString")})
	case protocol.ToolWrite:
		return kind, agent.Canon(map[string]any{"path": path, "content": agent.Str(in, "content")})
	case protocol.ToolPatch:
		return kind, agent.Canon(map[string]any{"patch": agent.Str(in, "patchText")})
	case protocol.ToolRead:
		return kind, agent.Canon(map[string]any{"path": path, "offset": in["offset"], "limit": in["limit"]})
	case protocol.ToolSearch:
		return kind, agent.Canon(map[string]any{"pattern": agent.Str(in, "pattern"), "path": path, "glob": agent.Str(in, "include")})
	case protocol.ToolFetch:
		return kind, agent.Canon(map[string]any{"url": agent.Str(in, "url")})
	case protocol.ToolWebSearch:
		return kind, agent.Canon(map[string]any{"query": agent.Str(in, "query")})
	case protocol.ToolTodo:
		return kind, agent.Canon(map[string]any{"items": agent.Items(agent.List(in, "todos"), "content", "status")})
	case protocol.ToolTask:
		return kind, agent.Canon(map[string]any{"description": agent.Str(in, "description"), "prompt": agent.Str(in, "prompt"), "agent": agent.Str(in, "subagent_type", "category")})
	case protocol.ToolQuestion:
		return kind, agent.Question(in, "question", "header")
	}
	return kind, nil
}

// Output: the plugin writes a result as OpenCode gave it, its exit code
// apart.
func (*Agent) Output(_, text string) (string, int) { return text, -1 }
