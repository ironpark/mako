// Package transcript reads native agent logs without interpreting terminal
// redraws as conversation. It never writes logs or starts an agent.
package transcript

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ironpark/sanbo/internal/protocol"
)

const maxRead = 2 << 20

var validID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)

// Supported reports whether agent writes a native log this package reads.
func Supported(agent string) bool {
	return agent == "claude" || agent == "codex"
}

// Read reads the conversation bound to info's ID. A log the agent has not
// written yet yields the ID without messages.
func Read(ctx context.Context, info protocol.SessionInfo, env map[string]string) (protocol.Chat, error) {
	out := protocol.Chat{Supported: Supported(info.Agent)}
	if !out.Supported || info.ConversationID == "" {
		return out, nil
	}
	if !validID.MatchString(info.ConversationID) {
		return out, errors.New("invalid conversation ID")
	}
	out.Conversation = info.ConversationID
	path, err := Locate(ctx, info, env)
	if err != nil || path == "" {
		return out, err
	}
	if _, ok := logProject(path, info.Agent, info.Project); !ok {
		return out, nil
	}
	out.Messages, out.Truncated, err = readMessages(path, info.Agent)
	return out, err
}

// logProject reads a log's identity and reports whether it is a usable
// conversation of project.
func logProject(path, agent, project string) (string, bool) {
	id, cwd := metadata(path, agent)
	return id, id != "" && validID.MatchString(id) && samePath(cwd, project)
}

func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if p, err := filepath.EvalSymlinks(a); err == nil {
		a = p
	}
	if p, err := filepath.EvalSymlinks(b); err == nil {
		b = p
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// ArgumentID extracts an explicitly selected native ID. A fork's source
// conversation is not the new process's identity.
func ArgumentID(agent string, args []string) string {
	var sessionID, resumeID string
	fork := false
	for i, arg := range args {
		name, v, hasValue := strings.Cut(arg, "=")
		if !hasValue && i+1 < len(args) {
			v = args[i+1]
		}
		switch {
		case agent == "claude" && name == "--session-id":
			if sessionID == "" && validID.MatchString(v) {
				sessionID = v
			}
		case agent == "claude" && name == "--fork-session":
			fork = true
		case agent == "claude" && (name == "--resume" || name == "-r"):
			if resumeID == "" && validID.MatchString(v) {
				resumeID = v
			}
		case agent == "codex" && arg == "resume":
			if resumeID == "" && validID.MatchString(v) {
				resumeID = v
			}
		}
	}
	switch {
	case sessionID != "":
		return sessionID
	case fork:
		return ""
	}
	return resumeID
}

type record struct {
	Type      string          `json:"type"`
	Cwd       string          `json:"cwd"`
	SessionID string          `json:"sessionId"`
	Sidechain bool            `json:"isSidechain"`
	UUID      string          `json:"uuid"`
	Message   json.RawMessage `json:"message"`
	Payload   json.RawMessage `json:"payload"`
}

func metadata(path, agent string) (string, string) {
	id, cwd, _ := scanLog(path, agent)
	return id, cwd
}

// scanLog reads a log's identity record: its native ID, working directory
// and creation time.
func scanLog(path, agent string) (id, cwd string, created time.Time) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", time.Time{}
	}
	defer f.Close()
	s := bufio.NewScanner(io.LimitReader(f, maxRead))
	s.Buffer(make([]byte, 4096), maxRead)
	for s.Scan() {
		var r struct {
			record
			Timestamp time.Time `json:"timestamp"`
		}
		if json.Unmarshal(s.Bytes(), &r) != nil {
			continue
		}
		if agent == "claude" && !r.Sidechain && r.Cwd != "" && r.SessionID != "" {
			return r.SessionID, r.Cwd, r.Timestamp
		}
		if agent == "codex" && r.Type == "session_meta" {
			var meta struct {
				ID        string          `json:"id"`
				SessionID string          `json:"session_id"`
				Cwd       string          `json:"cwd"`
				Source    json.RawMessage `json:"source"`
				Timestamp time.Time       `json:"timestamp"`
			}
			if json.Unmarshal(r.Payload, &meta) != nil {
				return "", "", time.Time{}
			}
			// Subagent transcripts are not the interactive session's conversation.
			if strings.Contains(string(meta.Source), "subagent") {
				return "", "", time.Time{}
			}
			if meta.ID == "" {
				meta.ID = meta.SessionID
			}
			if meta.Timestamp.IsZero() {
				meta.Timestamp = r.Timestamp
			}
			return meta.ID, meta.Cwd, meta.Timestamp
		}
	}
	return "", "", time.Time{}
}

func readMessages(path, agent string) ([]protocol.ChatMessage, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	truncated := stat.Size() > maxRead
	if truncated {
		if _, err = f.Seek(stat.Size()-maxRead, io.SeekStart); err != nil {
			return nil, false, err
		}
	}
	reader := bufio.NewReader(io.LimitReader(f, maxRead))
	if truncated {
		_, _ = reader.ReadBytes('\n')
	} // discard a partial first record
	var messages []protocol.ChatMessage
	// A tool call's ID to its place in messages, counting the dropped ones,
	// so dropping never has to move the places.
	calls := map[string]int{}
	dropped := 0
	seen := map[string]bool{}
	size := 0
	// trim keeps the window small enough to send, dropping the oldest
	// messages.
	trim := func() {
		for len(messages) > 200 || size > 512<<10 {
			size -= len(messages[0].Text) + len(messages[0].Result)
			messages = messages[1:]
			dropped++
			truncated = true
		}
	}
	for {
		line, err := reader.ReadBytes('\n')
		// The writer may be in the middle of a record. Wait for the next poll.
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, truncated, err
		}
		var r record
		if json.Unmarshal(line, &r) != nil {
			continue
		}
		if r.Sidechain {
			continue
		}
		if r.UUID != "" {
			if seen[r.UUID] {
				continue
			}
			seen[r.UUID] = true
		}
		raw := r.Message
		if agent == "codex" {
			if r.Type != "response_item" {
				continue
			}
			raw = r.Payload
		} else if r.Type != "user" && r.Type != "assistant" {
			continue
		}
		for _, m := range parseMessage(raw) {
			if len(m.Text) > 32<<10 {
				m.Text = trimUTF8(m.Text, 32<<10) + "\n…"
				truncated = true
			}
			// A result belongs to the call it answers: one row in the chat
			// rather than a second line of loose output.
			if m.Role == "tool" && m.Output && m.ID != "" {
				if i, ok := calls[m.ID]; ok && i >= dropped {
					messages[i-dropped].Result = m.Text
					messages[i-dropped].Error = m.Error
					size += len(m.Text)
					trim()
					continue
				}
			}
			if m.Role == "tool" && !m.Output && m.ID != "" {
				calls[m.ID] = dropped + len(messages)
			}
			messages = append(messages, m)
			size += len(m.Text)
			trim()
		}
	}
	return messages, truncated, nil
}

func trimUTF8(s string, n int) string {
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func parseMessage(raw json.RawMessage) []protocol.ChatMessage {
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
			input = argumentsJSON(m.Arguments)
		}
		return []protocol.ChatMessage{toolCall(cmp.Or(m.ID, m.CallID), m.Name, input)}
	case "function_call_output", "custom_tool_call_output":
		return []protocol.ChatMessage{{
			Role: "tool", ID: m.CallID, Text: contentText(m.Output), Output: true,
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
		if text := contentText(m.Content); strings.TrimSpace(text) != "" {
			return []protocol.ChatMessage{{Role: m.Role, Text: text}}
		}
		return nil
	}
	var out []protocol.ChatMessage
	for _, b := range blocks {
		msg := protocol.ChatMessage{Role: m.Role, Text: b.Text}
		switch b.Type {
		case "text", "input_text", "output_text":
		case "tool_use":
			msg = toolCall(b.ID, b.Name, b.Input)
		case "tool_result":
			msg = protocol.ChatMessage{Role: "tool", ID: b.ToolUseID, Text: contentText(b.Content), Output: true, Error: b.IsError}
		case "image", "input_image":
			msg.Text = "[image]"
		default:
			continue
		}
		if strings.TrimSpace(msg.Text) == "" && msg.Tool == "" && !msg.Output {
			continue
		}
		// Tool rows stay apart; prose of one speaker folds together.
		if len(out) > 0 && out[len(out)-1].Role == msg.Role && msg.Role != "tool" {
			out[len(out)-1].Text += "\n\n" + msg.Text
		} else {
			out = append(out, msg)
		}
	}
	return out
}

// toolCall is a tool message from its call: the name, a one-line detail for
// the chat's header, and the arguments as pretty JSON.
func toolCall(id, name string, input json.RawMessage) protocol.ChatMessage {
	args := ""
	if len(input) > 0 {
		var b bytes.Buffer
		if json.Indent(&b, input, "", "  ") == nil {
			args = b.String()
		} else {
			args = string(input)
		}
	}
	return protocol.ChatMessage{Role: "tool", ID: id, Tool: name, Detail: ToolDetail(input), Text: args}
}

// argumentsJSON reads the JSON an agent wrote as a string, as Codex does in
// its function calls.
func argumentsJSON(s string) json.RawMessage {
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

func contentText(raw json.RawMessage) string {
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

// Verify reports whether a bound log is present and carries the session's
// conversation ID, without reading its messages.
func Verify(info protocol.SessionInfo, path string) error {
	if id, _ := metadata(path, info.Agent); id != info.ConversationID {
		return fmt.Errorf("conversation log identity does not match session")
	}
	return nil
}

// ReadBound reads a daemon-verified path, including rollout names that carry
// a separate file ID. A missing or replaced bound log is an error.
func ReadBound(info protocol.SessionInfo, path string) (protocol.Chat, error) {
	out := protocol.Chat{Supported: true, Conversation: info.ConversationID}
	if err := Verify(info, path); err != nil {
		return out, err
	}
	var err error
	out.Messages, out.Truncated, err = readMessages(path, info.Agent)
	return out, err
}

// logPaths lists an agent's logs whose file name matches the glob name.
func logPaths(agent string, env map[string]string, name string) ([]string, error) {
	home := env["HOME"]
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	root := env["CLAUDE_CONFIG_DIR"]
	if agent == "codex" {
		root = env["CODEX_HOME"]
	}
	if root == "" {
		if agent == "claude" {
			root = filepath.Join(home, ".claude")
		} else {
			root = filepath.Join(home, ".codex")
		}
	}
	var pattern string
	if agent == "claude" {
		pattern = filepath.Join(root, "projects", "*", name)
	} else {
		pattern = filepath.Join(root, "sessions", "*", "*", "*", name)
	}
	return filepath.Glob(pattern)
}

// Locate finds a known native ID without depending on a rollout filename.
func Locate(ctx context.Context, info protocol.SessionInfo, env map[string]string) (string, error) {
	// The ID goes into the glob, so only a valid one may.
	if !validID.MatchString(info.ConversationID) {
		return "", nil
	}
	paths, err := logPaths(info.Agent, env, "*"+info.ConversationID+"*.jsonl")
	if err != nil {
		return "", err
	}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		id, _ := metadata(path, info.Agent)
		if id == info.ConversationID {
			return path, nil
		}
	}
	return "", nil
}

// Recover is a one-time migration for launches saved before native IDs were
// recorded. Only one unclaimed log created during that launch can be adopted;
// ambiguous history is never replaced with a fresh conversation.
func Recover(ctx context.Context, info protocol.SessionInfo, env map[string]string, claimed map[string]bool) (string, string, error) {
	paths, err := logPaths(info.Agent, env, "*.jsonl")
	if err != nil {
		return "", "", err
	}
	var foundID, foundPath string
	projectLogs := false
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return "", "", err
		}
		id, cwd, created := scanLog(path, info.Agent)
		if id == "" || !validID.MatchString(id) || claimed[id] || !samePath(cwd, info.Project) {
			continue
		}
		projectLogs = true
		if created.IsZero() || created.Before(info.StartedAt.Add(-2*time.Second)) {
			continue
		}
		if foundID != "" && foundID != id {
			return "", "", fmt.Errorf("cannot identify the original conversation for this older saved session: multiple logs match")
		}
		foundID, foundPath = id, path
	}
	if foundID == "" && projectLogs {
		return "", "", fmt.Errorf("cannot identify the original conversation for this older saved session; no log matches its launch time")
	}
	return foundID, foundPath, nil
}
