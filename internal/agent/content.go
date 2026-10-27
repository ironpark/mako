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
// write them: prose, tool calls and their results. kind names what a call's
// tool does, as Tools.ToolKind.
func ParseMessage(raw json.RawMessage, kind func(tool string) string) []*protocol.ChatMessage {
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
		call := ToolCall(cmp.Or(m.ID, m.CallID), m.Name, input)
		call.Kind = kind(m.Name)
		return []*protocol.ChatMessage{call}
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
			msg = ToolCall(b.ID, b.Name, b.Input)
			msg.Kind = kind(b.Name)
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

// ToolDetail is what a tool row shows beside its name: a command, a path, a
// pattern, or the first string the arguments carry.
func ToolDetail(input json.RawMessage) string {
	var m map[string]any
	if len(input) == 0 || json.Unmarshal(input, &m) != nil {
		return ""
	}
	for _, key := range []string{"command", "file_path", "path", "pattern", "url", "description", "query", "notebook_path", "glob"} {
		if s, ok := m[key].(string); ok && strings.TrimSpace(s) != "" {
			return OneLine(s, 120)
		}
	}
	for _, v := range m {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			return OneLine(s, 120)
		}
	}
	return ""
}

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
