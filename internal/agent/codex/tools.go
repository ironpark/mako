package codex

import (
	"cmp"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/protocol"
)

// tools are Codex's tools, by their names in lower case.
var tools = map[string]string{
	// Its hooks name a command Bash, as Claude's do.
	"bash":  protocol.ToolShell,
	"shell": protocol.ToolShell, "exec_command": protocol.ToolShell, "local_shell": protocol.ToolShell,
	"container.exec": protocol.ToolShell, "write_stdin": protocol.ToolShell,
	"apply_patch": protocol.ToolPatch,
	"read":        protocol.ToolRead, "view_image": protocol.ToolRead,
	"list_dir":           protocol.ToolSearch,
	"update_plan":        protocol.ToolPlan,
	"web_search":         protocol.ToolWebSearch,
	"spawn_agent":        protocol.ToolTask,
	"request_user_input": protocol.ToolQuestion, "request_user_input_async": protocol.ToolQuestion,
}

// toolKind is what a call of the tool name does.
func toolKind(name string) string { return tools[strings.ToLower(name)] }

// Tool reads a call's arguments as Codex's tools write them.
func (*Agent) Tool(name string, args json.RawMessage) (string, map[string]any) {
	kind := toolKind(name)
	if kind == protocol.ToolPatch {
		// The patch itself, or JSON holding it.
		var patch string
		if json.Unmarshal(args, &patch) != nil {
			patch = agent.Str(agent.Fields(args), "input", "patch", "command")
		}
		return kind, agent.Canon(map[string]any{"patch": patch})
	}
	in := agent.Fields(args)
	if kind == "" || in == nil {
		return kind, nil
	}
	switch kind {
	case protocol.ToolShell:
		command := cmp.Or(script(in["command"]), script(in["cmd"]), agent.Str(in, "chars"))
		return kind, agent.Canon(map[string]any{"command": command, "cwd": agent.Str(in, "workdir", "cwd")})
	case protocol.ToolRead:
		return kind, agent.Canon(map[string]any{"path": agent.Str(in, "path", "file_path")})
	case protocol.ToolSearch:
		return kind, agent.Canon(map[string]any{"path": agent.Str(in, "dir_path", "path")})
	case protocol.ToolPlan:
		return kind, agent.Canon(map[string]any{"explanation": agent.Str(in, "explanation"), "items": agent.Items(agent.List(in, "plan"), "step", "status")})
	case protocol.ToolWebSearch:
		return kind, agent.Canon(map[string]any{"query": agent.Str(in, "query")})
	case protocol.ToolTask:
		return kind, agent.Canon(map[string]any{"description": agent.Str(in, "task_name", "description"), "prompt": agent.Str(in, "message", "prompt"), "agent": agent.Str(in, "agent_type", "role")})
	case protocol.ToolQuestion:
		return kind, agent.Question(in, "question", "header")
	}
	return kind, nil
}

// script is the command an argv runs: the script of ["bash", "-lc", "…"],
// or its words; a string as it is.
func script(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case []any:
		var words []string
		for _, a := range v {
			if s, ok := a.(string); ok {
				words = append(words, s)
			}
		}
		if len(words) == 3 && (words[1] == "-lc" || words[1] == "-c") {
			return words[2]
		}
		return strings.Join(words, " ")
	}
	return ""
}

// exitLine finds the exit code in the header Codex writes over a command's
// output.
var exitLine = regexp.MustCompile(`(?m)^(?:Process exited with code|Exit code:) (-?\d+)`)

// Output is a command's output without the header Codex writes over it,
// "Exit code: 0\nWall time: …\nOutput:\n…", or the JSON it wrote before,
// {"output": …, "metadata": {"exit_code": …}}, and its exit code.
func (*Agent) Output(kind, text string) (string, int) {
	if kind != protocol.ToolShell {
		return text, -1
	}
	if head, out, ok := strings.Cut(text, "\nOutput:\n"); ok {
		code := -1
		if m := exitLine.FindStringSubmatch(head); m != nil {
			code, _ = strconv.Atoi(m[1])
		}
		return out, code
	}
	if !strings.HasPrefix(text, "{") {
		return text, -1
	}
	var wrapped struct {
		Output   *string `json:"output"`
		Metadata struct {
			ExitCode *int `json:"exit_code"`
		} `json:"metadata"`
	}
	if json.Unmarshal([]byte(text), &wrapped) == nil && wrapped.Output != nil {
		code := -1
		if wrapped.Metadata.ExitCode != nil {
			code = *wrapped.Metadata.ExitCode
		}
		return *wrapped.Output, code
	}
	return text, -1
}
