package codex

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/protocol"
)

// item is a completed item, of the kinds that are a tool's call: each
// reads as one row with what it gave back. One that completes a call the
// log already holds, by its ID, is that call's result.
func (d *decoder) item(payload json.RawMessage) []*protocol.ChatMessage {
	var ev struct {
		Type string          `json:"type"`
		Item json.RawMessage `json:"item"`
	}
	if json.Unmarshal(payload, &ev) != nil || ev.Type != "item_completed" {
		return nil
	}
	var it struct {
		Type      string            `json:"type"`
		ID        string            `json:"id"`
		Status    string            `json:"status"`
		Command   json.RawMessage   `json:"command"`
		Cwd       string            `json:"cwd"`
		Output    string            `json:"aggregated_output"`
		Stdout    string            `json:"stdout"`
		Stderr    string            `json:"stderr"`
		ExitCode  *int32            `json:"exit_code"`
		Changes   map[string]change `json:"changes"`
		Server    string            `json:"server"`
		Tool      string            `json:"tool"`
		Arguments json.RawMessage   `json:"arguments"`
		Result    *struct {
			Content json.RawMessage `json:"content"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Kind    string `json:"kind"`
		Query   string `json:"query"`
		Results []struct {
			Title string `json:"title"`
			URL   string `json:"url"`
		} `json:"results"`
		Path      string   `json:"path"`
		Prompt    string   `json:"prompt"`
		Receivers []string `json:"receiver_thread_ids"`
		Agents    []struct {
			Nickname string `json:"agent_nickname"`
			Role     string `json:"agent_role"`
		} `json:"receiver_agents"`
	}
	if json.Unmarshal(ev.Item, &it) != nil {
		return nil
	}
	failed := it.Status == "failed" || it.Status == "declined"
	var call *protocol.ChatMessage
	var result string
	switch it.Type {
	case "CommandExecution":
		var argv any
		_ = json.Unmarshal(it.Command, &argv)
		call = agent.Row("", "shell", protocol.ToolShell, agent.Canon(map[string]any{"command": script(argv), "cwd": localPath(it.Cwd)}))
		result = it.Output
		if result == "" {
			result = strings.TrimRight(it.Stdout+"\n"+it.Stderr, "\n")
		}
		call.ExitCode = it.ExitCode
		failed = it.Status == "declined"
	case "FileChange":
		call = agent.Row("", "apply_patch", protocol.ToolPatch, map[string]any{"patch": patchOf(it.Changes)})
		result = it.Stdout
	case "McpToolCall":
		call = agent.ToolCall("", it.Server+"."+it.Tool, it.Arguments)
		if it.Result != nil {
			result = agent.ContentText(it.Result.Content)
		}
		if it.Error != nil {
			result = it.Error.Message
		}
	case "WebSearch", "Extension":
		if it.Type == "Extension" && it.Kind != "web.search" {
			return nil
		}
		call = agent.Row("", "web_search", protocol.ToolWebSearch, agent.Canon(map[string]any{"query": it.Query}))
		var lines []string
		for _, r := range it.Results {
			lines = append(lines, strings.TrimSpace(r.Title+"  "+r.URL))
		}
		result = strings.Join(lines, "\n")
	case "ImageView":
		call = agent.Row("", "view_image", protocol.ToolRead, map[string]any{"path": localPath(it.Path)})
	case "CollabAgentToolCall":
		var role, nickname string
		if len(it.Agents) > 0 {
			role, nickname = it.Agents[0].Role, it.Agents[0].Nickname
		}
		call = agent.Row("", it.Tool, toolKind(it.Tool), agent.Canon(map[string]any{"description": nickname, "prompt": it.Prompt, "agent": role}))
		if len(it.Receivers) > 0 {
			// As spawn_agent answers: the agent it started.
			out, _ := json.Marshal(map[string]any{"agent_id": it.Receivers[0], "nickname": nickname})
			result = string(out)
		}
	default:
		return nil
	}
	if d.calls[it.ID] {
		// The result of a call already read.
		return []*protocol.ChatMessage{{Role: "tool", Id: it.ID, Text: result, ExitCode: call.ExitCode, Output: true, Error: failed}}
	}
	if call.GetKind() == protocol.ToolTask && len(it.Receivers) > 0 {
		call.Id = it.ID // the subagent it started is followed by this call
	}
	call.Result, call.Error = result, failed
	return []*protocol.ChatMessage{call}
}

// localPath is a path Codex gives as a file URL, as a path.
func localPath(p string) string { return strings.TrimPrefix(p, "file://") }

// change is one file a FileChange changed.
type change struct {
	Type    string `json:"type"` // add, delete, update
	Content string `json:"content"`
	Diff    string `json:"unified_diff"`
	Move    string `json:"move_path"`
}

// patchOf is the changes in Codex's patch format, which apply_patch takes.
func patchOf(changes map[string]change) string {
	paths := make([]string, 0, len(changes))
	for p := range changes {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var b strings.Builder
	b.WriteString("*** Begin Patch\n")
	for _, p := range paths {
		c := changes[p]
		switch c.Type {
		case "add":
			fmt.Fprintf(&b, "*** Add File: %s\n", p)
			for _, l := range strings.Split(strings.TrimSuffix(c.Content, "\n"), "\n") {
				b.WriteString("+" + l + "\n")
			}
		case "delete":
			fmt.Fprintf(&b, "*** Delete File: %s\n", p)
		default:
			fmt.Fprintf(&b, "*** Update File: %s\n", p)
			if c.Move != "" {
				fmt.Fprintf(&b, "*** Move to: %s\n", c.Move)
			}
			for _, l := range strings.Split(strings.TrimSuffix(c.Diff, "\n"), "\n") {
				// The diff's own headers are the file's line above.
				if !strings.HasPrefix(l, "--- ") && !strings.HasPrefix(l, "+++ ") {
					b.WriteString(l + "\n")
				}
			}
		}
	}
	b.WriteString("*** End Patch\n")
	return b.String()
}
