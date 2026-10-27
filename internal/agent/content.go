package agent

import (
	"bytes"
	"cmp"
	"encoding/json"
	"strings"

	"github.com/ironpark/mako/internal/protocol"
)

// The shapes of messages agents log: what a driver's decoder makes into the
// chat's messages.

// ParseMessage reads a message as the Messages API and the Responses API
// write them: prose, tool calls and their results, each call as tools
// reads it.
func ParseMessage(raw json.RawMessage, tools Tools) []*protocol.ChatMessage {
	var m struct {
		Type      string          `json:"type"`
		Role      string          `json:"role"`
		Name      string          `json:"name"`
		Arguments string          `json:"arguments"`
		Input     json.RawMessage `json:"input"`
		CallID    string          `json:"call_id"`
		ID        string          `json:"id"`
		Output    json.RawMessage `json:"output"`
		Content   json.RawMessage `json:"content"`
		Success   *bool           `json:"success"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	switch m.Type {
	case "function_call", "custom_tool_call":
		input := m.Input
		if len(input) == 0 {
			input = ArgumentsJSON(m.Arguments)
		}
		return []*protocol.ChatMessage{Call(tools, cmp.Or(m.CallID, m.ID), m.Name, input)}
	case "function_call_output", "custom_tool_call_output":
		return []*protocol.ChatMessage{{
			Role: "tool", Id: m.CallID, Text: ContentText(m.Output), Output: true,
			Error: m.Success != nil && !*m.Success,
		}}
	}
	if m.Role != "user" && m.Role != "assistant" {
		return nil
	}
	var blocks []struct {
		Type      string          `json:"type"`
		Text      string          `json:"text"`
		Name      string          `json:"name"`
		ID        string          `json:"id"`
		ToolUseID string          `json:"tool_use_id"`
		IsError   bool            `json:"is_error"`
		Input     json.RawMessage `json:"input"`
		Content   json.RawMessage `json:"content"`
	}
	if json.Unmarshal(m.Content, &blocks) != nil {
		if text := ContentText(m.Content); strings.TrimSpace(text) != "" {
			return []*protocol.ChatMessage{{Role: m.Role, Text: text}}
		}
		return nil
	}
	var out []*protocol.ChatMessage
	for _, b := range blocks {
		msg := &protocol.ChatMessage{Role: m.Role, Text: b.Text}
		switch b.Type {
		case "text", "input_text", "output_text":
		case "tool_use":
			msg = Call(tools, b.ID, b.Name, b.Input)
		case "tool_result":
			msg = &protocol.ChatMessage{Role: "tool", Id: b.ToolUseID, Text: ContentText(b.Content), Output: true, Error: b.IsError}
		case "image", "input_image":
			msg.Text = "[image]"
		default:
			continue
		}
		if strings.TrimSpace(msg.GetText()) == "" && msg.GetTool() == "" && !msg.GetOutput() {
			continue
		}
		// Tool rows stay apart; prose of one speaker folds together.
		if len(out) > 0 && out[len(out)-1].GetRole() == msg.GetRole() && msg.GetRole() != "tool" {
			out[len(out)-1].Text += "\n\n" + msg.GetText()
		} else {
			out = append(out, msg)
		}
	}
	return out
}

// Call is a call of the tool name as tools reads it: its kind, and its
// arguments in the kind's form.
func Call(tools Tools, id, name string, input json.RawMessage) *protocol.ChatMessage {
	kind, canon := tools.Tool(name, input)
	if canon == nil {
		return ToolCall(id, name, input)
	}
	return Row(id, name, kind, canon)
}

// Row is a call of kind with its arguments already in the kind's form.
func Row(id, name, kind string, canon map[string]any) *protocol.ChatMessage {
	args, _ := json.MarshalIndent(canon, "", "  ")
	return &protocol.ChatMessage{Role: "tool", Id: id, Tool: name, Kind: kind, Detail: Detail(kind, canon), Text: string(args)}
}

// Summary is the row a compaction leaves, text its summary.
func Summary(text string) *protocol.ChatMessage {
	return &protocol.ChatMessage{Role: "tool", Tool: SummaryTool, Kind: protocol.ToolSummary, Detail: OneLine(text, detailWidth), Text: text, Output: true}
}

// SummaryTool names the row a compaction leaves.
const SummaryTool = "Summary"

// Canon is a call's arguments in the form protocol gives its kind: the
// fields given, without the empty ones.
func Canon(fields map[string]any) map[string]any {
	for k, v := range fields {
		switch v := v.(type) {
		case nil:
			delete(fields, k)
		case string:
			if v == "" {
				delete(fields, k)
			}
		case float64:
			if v == 0 {
				delete(fields, k)
			}
		case []any:
			if len(v) == 0 {
				delete(fields, k)
			}
		}
	}
	return fields
}

// Task is a call that starts an agent, in the form protocol gives the task
// kind.
type Task struct {
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
	Agent       string `json:"agent"`
}

// Question is a question tool's arguments in the question kind's form:
// the first of its questions, read from the first of keys it has, and its
// options' labels.
func Question(in map[string]any, keys ...string) map[string]any {
	var question string
	var options []any
	if qs := List(in, "questions"); len(qs) > 0 {
		q, _ := qs[0].(map[string]any)
		question = Str(q, keys...)
		for _, o := range List(q, "options") {
			if o, ok := o.(map[string]any); ok {
				options = append(options, Str(o, "label"))
			}
		}
	}
	return Canon(map[string]any{"question": question, "options": options})
}

// Fields are a call's arguments as a JSON object, nil for others.
func Fields(args json.RawMessage) map[string]any {
	var in map[string]any
	_ = json.Unmarshal(args, &in)
	return in
}

// Str is the first of keys in holding a string, "" for none.
func Str(in map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := in[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// List is in[key] as a list, nil for none.
func List(in map[string]any, key string) []any {
	l, _ := in[key].([]any)
	return l
}

// Items are a list's entries as a checklist's, {text, status}, read from
// the entries' textKey and statusKey.
func Items(entries []any, textKey, statusKey string) []any {
	var out []any
	for _, e := range entries {
		if e, ok := e.(map[string]any); ok {
			out = append(out, map[string]any{"text": Str(e, textKey), "status": Str(e, statusKey)})
		}
	}
	return out
}

// ToolCall is a tool message from its call: the name, a one-line detail for
// the chat's header, and the arguments as pretty JSON.
func ToolCall(id, name string, input json.RawMessage) *protocol.ChatMessage {
	args := ""
	if len(input) > 0 {
		var b bytes.Buffer
		if json.Indent(&b, input, "", "  ") == nil {
			args = b.String()
		} else {
			args = string(input)
		}
	}
	return &protocol.ChatMessage{Role: "tool", Id: id, Tool: name, Detail: ToolDetail(input), Text: args}
}

// ArgumentsJSON reads the JSON an agent wrote as a string, as Codex does in
// its function calls.
func ArgumentsJSON(s string) json.RawMessage {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var raw json.RawMessage
	if json.Unmarshal([]byte(s), &raw) == nil {
		return raw
	}
	return json.RawMessage(s)
}

// detailWidth is how many runes a row's detail shows.
const detailWidth = 120

// Detail is what a row shows beside a call's name: for a call of a kind,
// the field that says most of what it does; for one of no kind, the first
// string its arguments carry.
func Detail(kind string, canon map[string]any) string {
	var s string
	switch kind {
	case protocol.ToolShell:
		s = Str(canon, "command")
	case protocol.ToolPatch:
		s = strings.Join(PatchFiles(Str(canon, "patch")), ", ")
	case protocol.ToolEdit, protocol.ToolMultiEdit, protocol.ToolWrite, protocol.ToolRead:
		s = Str(canon, "path")
	case protocol.ToolSearch:
		s = Str(canon, "pattern", "path")
	case protocol.ToolFetch:
		s = Str(canon, "url")
	case protocol.ToolWebSearch:
		s = Str(canon, "query")
	case protocol.ToolTask:
		s = Str(canon, "description", "prompt")
	case protocol.ToolQuestion:
		s = Str(canon, "question")
	case protocol.ToolTodo, protocol.ToolPlan:
		s = Str(canon, "explanation")
	default:
		s = Str(canon, "command", "file_path", "path", "pattern", "url", "description", "query", "notebook_path", "glob")
		for _, v := range canon {
			if v, ok := v.(string); s == "" && ok && strings.TrimSpace(v) != "" {
				s = v
			}
		}
	}
	return OneLine(s, detailWidth)
}

// ToolDetail is Detail of arguments of no kind, as an agent wrote them.
func ToolDetail(input json.RawMessage) string { return Detail("", Fields(input)) }

// OneLine is s's first line, its whitespace collapsed, capped at n runes.
func OneLine(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		s = string(r[:n]) + "…"
	}
	return s
}

// ContentText is a content field's text: a string, or its blocks' text.
func ContentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		var parts []string
		for _, b := range blocks {
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// DropTags is s without its tags of names and their content.
func DropTags(s string, names ...string) string {
	for _, name := range names {
		s = dropTag(s, name)
	}
	return s
}

// dropTag is s without its <name> tags and their content.
func dropTag(s, name string) string {
	open, end := "<"+name+">", "</"+name+">"
	for {
		i := strings.Index(s, open)
		if i < 0 {
			return s
		}
		j := strings.Index(s[i:], end)
		if j < 0 {
			return s[:i]
		}
		s = s[:i] + s[i+j+len(end):]
	}
}

// PatchFiles are the files a patch in Codex's format changes, in order.
func PatchFiles(patch string) []string {
	var files []string
	for line := range strings.Lines(patch) {
		for _, op := range []string{"*** Add File: ", "*** Update File: ", "*** Delete File: "} {
			if f, ok := strings.CutPrefix(line, op); ok {
				files = append(files, strings.TrimSpace(f))
			}
		}
	}
	return files
}
