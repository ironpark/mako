package pi

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/protocol"
)

// A session is logged in its working directory's folder,
// <root>/sessions/<dir>/<time>_<id>.jsonl: a session header, then an entry
// a line. Entries form a tree, a branch for each road taken back from an
// earlier one; the log holds every branch, in the order they were written.
// omp keeps a subagent's session a folder deeper, which is not the
// session's.

func (a *Agent) Place() agent.Place { return a.place }

// LogName is the glob of conversation id's log, whatever its time.
func (*Agent) LogName(id string) string { return "*_" + id + ".jsonl" }

// entry is a log's line.
type entry struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Cwd       string          `json:"cwd"`
	Timestamp time.Time       `json:"timestamp"`
	Message   json.RawMessage `json:"message"`
	Summary   string          `json:"summary"`
}

// Identity is the session header's, the log's first line.
func (*Agent) Identity(line []byte) (string, string, time.Time, bool) {
	var e entry
	if json.Unmarshal(line, &e) != nil || e.Type != "session" {
		return "", "", time.Time{}, true
	}
	return e.ID, e.Cwd, e.Timestamp, true
}

func (a *Agent) NewDecoder() agent.Decoder { return &decoder{tools: a} }

type decoder struct{ tools agent.Tools }

// message is an entry's message, of any role.
type message struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCallID string          `json:"toolCallId"`
	IsError    bool            `json:"isError"`
	// A command the user ran with !, and what it gave.
	Command  string `json:"command"`
	Output   string `json:"output"`
	ExitCode *int   `json:"exitCode"`
}

// block is a message's content block.
type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (d *decoder) Line(line []byte) []*protocol.ChatMessage {
	var e entry
	if json.Unmarshal(line, &e) != nil {
		return nil
	}
	switch e.Type {
	case "message":
		return d.message(e)
	case "compaction", "branch_summary":
		if e.Summary != "" {
			return []*protocol.ChatMessage{agent.Summary(e.Summary)}
		}
	}
	return nil
}

func (d *decoder) message(e entry) []*protocol.ChatMessage {
	var m message
	if json.Unmarshal(e.Message, &m) != nil {
		return nil
	}
	switch m.Role {
	case "user":
		if text := strings.TrimSpace(contentText(m.Content)); text != "" {
			return []*protocol.ChatMessage{{Role: "user", Text: text}}
		}
	case "assistant":
		var blocks []block
		_ = json.Unmarshal(m.Content, &blocks)
		var out []*protocol.ChatMessage
		for _, b := range blocks {
			switch b.Type {
			case "text":
				if strings.TrimSpace(b.Text) == "" {
					continue
				}
				if n := len(out); n > 0 && out[n-1].GetRole() == "assistant" {
					out[n-1].Text += "\n\n" + b.Text
				} else {
					out = append(out, &protocol.ChatMessage{Role: "assistant", Text: b.Text})
				}
			case "toolCall":
				out = append(out, agent.Call(d.tools, b.ID, b.Name, b.Arguments))
			}
		}
		return out
	case "toolResult":
		return []*protocol.ChatMessage{{Role: "tool", Id: m.ToolCallID, Text: contentText(m.Content), Output: true, Error: m.IsError}}
	case "bashExecution":
		// A command the user ran, its call and its result at once.
		call := agent.Row(e.ID, "bash", protocol.ToolShell, map[string]any{"command": m.Command})
		call.Result = m.Output
		if m.ExitCode != nil {
			code := int32(*m.ExitCode)
			call.ExitCode = &code
		}
		return []*protocol.ChatMessage{call}
	}
	return nil
}

// contentText is a message's text: its content as a string, or its text
// blocks.
func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []block
	_ = json.Unmarshal(raw, &blocks)
	var parts []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			parts = append(parts, b.Text)
		case "image":
			parts = append(parts, "[image]")
		}
	}
	return strings.Join(parts, "\n")
}

func (*Agent) NewUsage() agent.Usage { return usage{} }

// usage counts the tokens of each model call: the assistant's messages,
// and the calls that made summaries and the like.
type usage struct{}

func (usage) Count(line []byte, total *protocol.Usage) {
	if !bytes.Contains(line, []byte(`"usage"`)) {
		return
	}
	// An entry's tokens, or its message's: no more of either.
	var e struct {
		Type    string        `json:"type"`
		Usage   *agent.Tokens `json:"usage"`
		Message struct {
			Role  string        `json:"role"`
			Usage *agent.Tokens `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &e) != nil {
		return
	}
	u, reply := e.Usage, false
	if e.Type == "message" {
		u, reply = e.Message.Usage, e.Message.Role == "assistant"
	}
	if u != nil {
		u.Add(total, reply)
	}
}

// exitCode reads the code a command's result ends with,
// "Command exited with code 2".
func exitCode(text string) (string, int) {
	const status = "Command exited with code "
	i := strings.LastIndex(text, status)
	if i < 0 {
		return text, -1
	}
	code, err := strconv.Atoi(strings.TrimSpace(text[i+len(status):]))
	if err != nil {
		return text, -1
	}
	return strings.TrimRight(text[:i], "\n"), code
}
