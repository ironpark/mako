package agy

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/protocol"
)

// tools are agy's tools.
var tools = map[string]string{
	"run_command":                protocol.ToolShell,
	"replace_file_content":       protocol.ToolEdit,
	"multi_replace_file_content": protocol.ToolMultiEdit,
	"write_to_file":              protocol.ToolWrite, "write_file": protocol.ToolWrite,
	"view_file": protocol.ToolRead, "read_file": protocol.ToolRead,
	"grep_search": protocol.ToolSearch, "find_by_name": protocol.ToolSearch, "list_dir": protocol.ToolSearch,
	"read_url_content": protocol.ToolFetch,
	"search_web":       protocol.ToolWebSearch,
	"invoke_subagent":  protocol.ToolTask,
	"ask_question":     protocol.ToolQuestion,
}

// Tool reads a call's arguments as agy's tools write them.
func (*Agent) Tool(name string, args json.RawMessage) (string, map[string]any) {
	kind := tools[name]
	in := agent.Fields(args)
	if kind == "" || in == nil {
		return kind, nil
	}
	path := agent.Str(in, "TargetFile", "AbsolutePath", "Path")
	switch kind {
	case protocol.ToolShell:
		return kind, agent.Canon(map[string]any{"command": agent.Str(in, "CommandLine"), "cwd": agent.Str(in, "Cwd"), "description": agent.Str(in, "toolSummary")})
	case protocol.ToolEdit:
		return kind, agent.Canon(map[string]any{"path": path, "old": agent.Str(in, "TargetContent"), "new": agent.Str(in, "ReplacementContent")})
	case protocol.ToolMultiEdit:
		var edits []any
		for _, e := range agent.List(in, "ReplacementChunks") {
			if e, ok := e.(map[string]any); ok {
				edits = append(edits, map[string]any{"old": agent.Str(e, "TargetContent"), "new": agent.Str(e, "ReplacementContent")})
			}
		}
		return kind, agent.Canon(map[string]any{"path": path, "edits": edits})
	case protocol.ToolWrite:
		return kind, agent.Canon(map[string]any{"path": path, "content": agent.Str(in, "CodeContent", "Content")})
	case protocol.ToolRead:
		var offset, limit float64
		if start, ok := in["StartLine"].(float64); ok {
			offset = start
			if end, ok := in["EndLine"].(float64); ok && end >= start {
				limit = end - start + 1
			}
		}
		return kind, agent.Canon(map[string]any{"path": path, "offset": offset, "limit": limit})
	case protocol.ToolSearch:
		return kind, agent.Canon(map[string]any{
			"pattern": agent.Str(in, "Query", "Pattern"),
			"path":    agent.Str(in, "SearchPath", "SearchDirectory", "DirectoryPath"),
			"glob":    strings.Join(strs(agent.List(in, "Includes")), ","),
		})
	case protocol.ToolFetch:
		return kind, agent.Canon(map[string]any{"url": agent.Str(in, "Url")})
	case protocol.ToolWebSearch:
		return kind, agent.Canon(map[string]any{"query": agent.Str(in, "query")})
	case protocol.ToolTask:
		// The first of the subagents it starts.
		var first map[string]any
		if subs := agent.List(in, "Subagents"); len(subs) > 0 {
			first, _ = subs[0].(map[string]any)
		}
		return kind, agent.Canon(map[string]any{"description": agent.Str(in, "toolSummary"), "prompt": agent.Str(first, "Prompt"), "agent": agent.Str(first, "Role", "Name")})
	case protocol.ToolQuestion:
		return kind, agent.Question(in, "question", "header")
	}
	return kind, nil
}

// strs are the strings among l.
func strs(l []any) []string {
	var out []string
	for _, v := range l {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// Patterns of what agy writes around a tool's result.
var (
	// stamps are the times it puts first.
	stamps = regexp.MustCompile(`^(?:(?:Created|Completed) At: [^\n]*\n)+\n?`)
	// exited heads a command's output.
	exited = regexp.MustCompile(`^(?:The c|C)ommand exited with code (-?\d+)\.\n+(?:Command output|Output):\n`)
	// numbered heads a file's lines, each with its number.
	numbered = regexp.MustCompile(`(?s)^.*?in the format: <line_number>: <original_line>\.[^\n]*\n`)
	// diff is the change an edit made.
	diff = regexp.MustCompile(`(?s)\[diff_block_start\]\n(.*?)\n?\[diff_block_end\]`)
)

// Output is a result without the times and notes agy writes around it:
// a command's output and exit code, a file's lines without their numbers,
// an edit's diff.
func (*Agent) Output(kind, text string) (string, int) {
	text = strings.ReplaceAll(stamps.ReplaceAllString(text, ""), "\r\n", "\n")
	switch kind {
	case protocol.ToolShell:
		if m := exited.FindStringSubmatch(text); m != nil {
			code, _ := strconv.Atoi(m[1])
			return strings.TrimRight(text[len(m[0]):], "\n"), code
		}
	case protocol.ToolRead:
		if loc := numbered.FindStringIndex(text); loc != nil {
			var lines []string
			for _, l := range strings.Split(text[loc[1]:], "\n") {
				num, rest, ok := strings.Cut(l, ":")
				if _, err := strconv.Atoi(num); !ok || err != nil {
					break // the note after the lines
				}
				lines = append(lines, strings.TrimPrefix(rest, " "))
			}
			return strings.Join(lines, "\n"), -1
		}
	case protocol.ToolEdit, protocol.ToolMultiEdit, protocol.ToolWrite:
		if m := diff.FindStringSubmatch(text); m != nil {
			return m[1], -1
		}
	}
	return strings.TrimRight(text, "\n"), -1
}
