package pi

import (
	"encoding/json"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/protocol"
)

// tools are pi's, and those omp and omo add: no name means two things.
var tools = map[string]string{
	"bash": protocol.ToolShell, "powershell": protocol.ToolShell,
	"read": protocol.ToolRead, "edit": protocol.ToolEdit, "write": protocol.ToolWrite,
	"grep": protocol.ToolSearch, "find": protocol.ToolSearch, "ls": protocol.ToolSearch,
	"glob": protocol.ToolSearch, "ast_grep": protocol.ToolSearch,
	"ask":  protocol.ToolQuestion,
	"task": protocol.ToolTask, "call_omo_agent": protocol.ToolTask,
}

// Tool reads a call's arguments as pi's, omp's and omo's tools write them.
func (*Agent) Tool(name string, args json.RawMessage) (string, map[string]any) {
	kind := tools[name]
	in := agent.Fields(args)
	if kind == "" || in == nil {
		return kind, nil
	}
	path := agent.Str(in, "path", "file_path")
	switch kind {
	case protocol.ToolShell:
		return kind, agent.Canon(map[string]any{"command": agent.Str(in, "command"), "cwd": agent.Str(in, "cwd")})
	case protocol.ToolEdit:
		// pi's edits are replacements; omp's edit takes one replacement,
		// or a patch in one of its formats.
		if patch := agent.Str(in, "input"); patch != "" {
			return protocol.ToolPatch, agent.Canon(map[string]any{"patch": patch})
		}
		if edits := agent.List(in, "edits"); len(edits) > 0 {
			var out []any
			for _, e := range edits {
				if e, ok := e.(map[string]any); ok {
					out = append(out, map[string]any{"old": agent.Str(e, "oldText"), "new": agent.Str(e, "newText")})
				}
			}
			return protocol.ToolMultiEdit, agent.Canon(map[string]any{"path": path, "edits": out})
		}
		return kind, agent.Canon(map[string]any{"path": path, "old": agent.Str(in, "oldText", "old_string"), "new": agent.Str(in, "newText", "new_string")})
	case protocol.ToolWrite:
		return kind, agent.Canon(map[string]any{"path": path, "content": agent.Str(in, "content")})
	case protocol.ToolRead:
		return kind, agent.Canon(map[string]any{"path": path, "offset": in["offset"], "limit": in["limit"]})
	case protocol.ToolSearch:
		return kind, agent.Canon(map[string]any{"pattern": agent.Str(in, "pattern", "query"), "path": path, "glob": agent.Str(in, "glob")})
	case protocol.ToolQuestion:
		return kind, agent.Question(in, "question")
	case protocol.ToolTask:
		return kind, agent.Canon(map[string]any{"description": agent.Str(in, "description"), "prompt": agent.Str(in, "prompt"), "agent": agent.Str(in, "subagent_type", "category")})
	}
	return kind, nil
}

// Output is a result without the status pi adds to a command's, and the
// exit code it says.
func (*Agent) Output(kind, text string) (string, int) {
	if kind == protocol.ToolShell {
		return exitCode(text)
	}
	return text, -1
}
