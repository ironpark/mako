// Package transcript reads native agent logs without interpreting terminal
// redraws as conversation. It never writes logs or starts an agent.
package transcript

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/ironpark/sanbo/internal/protocol"
)

const maxRead = 2 << 20

var validID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

// Read lists conversations in this project or reads the selected one. Only
// an explicit ID in launch arguments is selected automatically: cwd and mtime
// alone cannot distinguish two agents working in the same directory.
func Read(ctx context.Context, info protocol.SessionInfo, env map[string]string, selected string) (protocol.Chat, error) {
	out := protocol.Chat{Supported: info.Agent == "claude" || info.Agent == "codex"}
	if !out.Supported {
		return out, nil
	}
	if selected == "" {
		selected = argumentID(info.Agent, info.Command)
	}
	if selected != "" && !validID.MatchString(selected) {
		return out, errors.New("invalid conversation ID")
	}
	home := env["HOME"]
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	root := env["CLAUDE_CONFIG_DIR"]
	if info.Agent == "codex" {
		root = env["CODEX_HOME"]
	}
	if root == "" {
		if info.Agent == "claude" {
			root = filepath.Join(home, ".claude")
		} else {
			root = filepath.Join(home, ".codex")
		}
	}
	var pattern string
	if info.Agent == "claude" {
		pattern = filepath.Join(root, "projects", "*", "*.jsonl")
	} else {
		pattern = filepath.Join(root, "sessions", "*", "*", "*", "*.jsonl")
	}
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return out, err
	}
	matched := ""
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		// A selected conversation does not require opening every history file.
		if selected != "" && !strings.HasSuffix(path, selected+".jsonl") {
			continue
		}
		id, cwd := metadata(path, info.Agent)
		if id == "" || !validID.MatchString(id) || !samePath(cwd, info.Project) {
			continue
		}
		stat, err := os.Stat(path)
		if err != nil || !stat.Mode().IsRegular() {
			continue
		}
		out.Conversations = append(out.Conversations, protocol.Conversation{ID: id, Updated: stat.ModTime()})
		if id == selected {
			matched = path
		}
	}
	sort.Slice(out.Conversations, func(i, j int) bool { return out.Conversations[i].Updated.After(out.Conversations[j].Updated) })
	if len(out.Conversations) > 100 {
		out.Conversations = out.Conversations[:100]
	}
	if selected == "" {
		return out, nil
	}
	if matched == "" {
		return out, fmt.Errorf("conversation %s is not available for this project", selected)
	}
	out.Conversation = selected
	out.Messages, out.Truncated, err = readMessages(matched, info.Agent)
	return out, err
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

func argumentID(agent string, args []string) string {
	for i, arg := range args {
		if agent == "claude" {
			for _, flag := range []string{"--session-id", "--resume", "-r"} {
				if arg == flag && i+1 < len(args) && validID.MatchString(args[i+1]) {
					return args[i+1]
				}
				if v, ok := strings.CutPrefix(arg, flag+"="); ok && validID.MatchString(v) {
					return v
				}
			}
		} else if arg == "resume" && i+1 < len(args) && validID.MatchString(args[i+1]) {
			return args[i+1]
		}
	}
	return ""
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
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	s := bufio.NewScanner(io.LimitReader(f, maxRead))
	s.Buffer(make([]byte, 4096), maxRead)
	for s.Scan() {
		var r record
		if json.Unmarshal(s.Bytes(), &r) != nil {
			continue
		}
		if agent == "claude" && !r.Sidechain && r.Cwd != "" && r.SessionID != "" {
			return r.SessionID, r.Cwd
		}
		if agent == "codex" && r.Type == "session_meta" {
			var meta struct {
				ID        string          `json:"id"`
				SessionID string          `json:"session_id"`
				Cwd       string          `json:"cwd"`
				Source    json.RawMessage `json:"source"`
			}
			if json.Unmarshal(r.Payload, &meta) != nil {
				return "", ""
			}
			// Subagent transcripts are not the interactive session's conversation.
			if strings.Contains(string(meta.Source), "subagent") {
				return "", ""
			}
			if meta.ID == "" {
				meta.ID = meta.SessionID
			}
			return meta.ID, meta.Cwd
		}
	}
	return "", ""
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
	seen := map[string]bool{}
	size := 0
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
			messages = append(messages, m)
			size += len(m.Text)
			for len(messages) > 200 || size > 512<<10 {
				size -= len(messages[0].Text)
				messages = messages[1:]
				truncated = true
			}
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
		Output    json.RawMessage `json:"output"`
		Content   json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	switch m.Type {
	case "function_call", "custom_tool_call":
		return []protocol.ChatMessage{{Role: "tool", Text: m.Name + "\n" + m.Arguments}}
	case "function_call_output", "custom_tool_call_output":
		return []protocol.ChatMessage{{Role: "tool", Text: contentText(m.Output)}}
	}
	if m.Role != "user" && m.Role != "assistant" {
		return nil
	}
	var blocks []struct {
		Type    string          `json:"type"`
		Text    string          `json:"text"`
		Name    string          `json:"name"`
		Input   json.RawMessage `json:"input"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(m.Content, &blocks) != nil {
		if text := contentText(m.Content); strings.TrimSpace(text) != "" {
			return []protocol.ChatMessage{{Role: m.Role, Text: text}}
		}
		return nil
	}
	var out []protocol.ChatMessage
	for _, b := range blocks {
		role, text := m.Role, b.Text
		switch b.Type {
		case "text", "input_text", "output_text":
		case "tool_use":
			role, text = "tool", b.Name+"\n"+string(b.Input)
		case "tool_result":
			role, text = "tool", contentText(b.Content)
		case "image", "input_image":
			text = "[image]"
		default:
			continue
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		if len(out) > 0 && out[len(out)-1].Role == role {
			out[len(out)-1].Text += "\n\n" + text
		} else {
			out = append(out, protocol.ChatMessage{Role: role, Text: text})
		}
	}
	return out
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
