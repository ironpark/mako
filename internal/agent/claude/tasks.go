package claude

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/protocol"
)

// Claude Code runs a command in the background when its Bash call says
// run_in_background: the call's result says the task's ID, in
// toolUseResult.backgroundTaskId, and the file its output goes to; a
// <task-notification> says when it ended, and how: as a turn of its own
// when the agent waited, else queued while it worked, which the log keeps
// as the queue's record and as an attachment of the turn; TaskStop, or
// KillShell before it, stops one.

// taskOutput finds the output file in a background call's result.
var taskOutput = regexp.MustCompile(`written to: (\S+\.output)`)

// taskExit finds the exit code in a notification's summary.
var taskExit = regexp.MustCompile(`\(exit code (-?\d+)\)`)

// taskMarks are what a line about a background task holds, to leave the
// rest unparsed.
var taskMarks = [][]byte{[]byte("run_in_background"), []byte("backgroundTaskId"), []byte("<task-notification>"), []byte(`"TaskStop"`), []byte(`"KillShell"`)}

// taskStops are the tools that stop a background task, by the argument
// that names it.
var taskStops = map[string][]string{"TaskStop": {"task_id", "shell_id"}, "KillShell": {"shell_id"}}

func (*Agent) TaskEvents(line []byte) []agent.TaskEvent {
	if !hasAny(line, taskMarks) {
		return nil
	}
	var r struct {
		record
		Result struct {
			TaskID string `json:"backgroundTaskId"`
		} `json:"toolUseResult"`
		Operation  string `json:"operation"` // a queue-operation's
		Content    string `json:"content"`
		Attachment struct {
			Type   string `json:"type"`
			Prompt string `json:"prompt"`
		} `json:"attachment"`
	}
	if json.Unmarshal(line, &r) != nil || r.Sidechain {
		return nil
	}
	switch {
	case r.Type == "queue-operation" && r.Operation == "enqueue":
		return taskEnded(r.Content, r.record)
	case r.Type == "attachment" && r.Attachment.Type == "queued_command":
		return taskEnded(r.Attachment.Prompt, r.record)
	}
	var msg struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(r.Message, &msg) != nil {
		return nil
	}
	var blocks []struct {
		Type      string          `json:"type"`
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Input     json.RawMessage `json:"input"`
		ToolUseID string          `json:"tool_use_id"`
		Content   json.RawMessage `json:"content"`
	}
	_ = json.Unmarshal(msg.Content, &blocks) // a turn of text alone has none
	var out []agent.TaskEvent
	switch r.Type {
	case "assistant":
		for _, b := range blocks {
			if b.Type != "tool_use" {
				continue
			}
			in := agent.Fields(b.Input)
			if names, ok := taskStops[b.Name]; ok {
				if id := agent.Str(in, names...); id != "" {
					out = append(out, agent.TaskEvent{Kind: agent.TaskEnded, ID: id, At: r.Timestamp, Status: protocol.TaskKilled})
				}
				continue
			}
			if background, _ := in["run_in_background"].(bool); background {
				out = append(out, agent.TaskEvent{Kind: agent.TaskCalled, CallID: b.ID, At: r.Timestamp,
					Command: agent.Clip(agent.Str(in, "command"), commandRunes), Description: agent.Str(in, "description")})
			}
		}
	case "user":
		if id := r.Result.TaskID; id != "" {
			for _, b := range blocks {
				if b.Type == "tool_result" {
					ev := agent.TaskEvent{Kind: agent.TaskStarted, CallID: b.ToolUseID, ID: id, At: r.Timestamp}
					if m := taskOutput.FindStringSubmatch(agent.ContentText(b.Content)); m != nil {
						ev.Output = m[1]
					}
					out = append(out, ev)
				}
			}
		}
		out = append(out, taskEnded(agent.ContentText(msg.Content), r.record)...)
	}
	return out
}

// taskEnded is the end text tells of, when it is a task's notification.
func taskEnded(text string, r record) []agent.TaskEvent {
	text = strings.TrimSpace(text)
	body, ok := inner(text, "task-notification")
	if !ok || !strings.HasPrefix(text, "<task-notification>") {
		return nil
	}
	id, _ := inner(body, "task-id")
	call, _ := inner(body, "tool-use-id")
	status, _ := inner(body, "status")
	summary, _ := inner(body, "summary")
	ev := agent.TaskEvent{Kind: agent.TaskEnded, ID: id, CallID: call, At: r.Timestamp, Status: status}
	if m := taskExit.FindStringSubmatch(summary); m != nil {
		if code, err := strconv.ParseInt(m[1], 10, 32); err == nil {
			c := int32(code)
			ev.ExitCode = &c
		}
	}
	return []agent.TaskEvent{ev}
}

// commandRunes is as much of a background command as a session's state
// carries, to every window of every node: a script written inline can be
// pages long.
const commandRunes = 1000

// hasAny reports whether line holds any of marks.
func hasAny(line []byte, marks [][]byte) bool {
	for _, m := range marks {
		if bytes.Contains(line, m) {
			return true
		}
	}
	return false
}
