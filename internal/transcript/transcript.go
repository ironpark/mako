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
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ironpark/sanbo/internal/protocol"
)

const maxRead = 2 << 20

var validID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)

var errUnsupported = errors.New("agent has no native conversation")

// Read reads the conversation bound to info's ID. A log the agent has not
// written yet yields the ID without messages.
func Read(ctx context.Context, info protocol.SessionInfo, env map[string]string) (protocol.Chat, error) {
	a, ok := Lookup(info.Agent)
	out := protocol.Chat{Supported: ok}
	if !ok || info.ConversationID == "" {
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
	if _, ok := logProject(path, a, info.Project); !ok {
		return out, nil
	}
	out.Messages, out.Truncated, err = readMessages(path, a)
	return out, err
}

// logProject reads a log's identity and reports whether it is a usable
// conversation of project.
func logProject(path string, a *Agent, project string) (string, bool) {
	id, cwd := metadata(path, a)
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

type record struct {
	Type      string          `json:"type"`
	Cwd       string          `json:"cwd"`
	SessionID string          `json:"sessionId"`
	Sidechain bool            `json:"isSidechain"`
	UUID      string          `json:"uuid"`
	Message   json.RawMessage `json:"message"`
	Payload   json.RawMessage `json:"payload"`
}

func metadata(path string, a *Agent) (string, string) {
	id, cwd, _ := scanLog(path, a)
	return id, cwd
}

// identities remembers the identity records already read. A log only grows,
// so its identity holds while it is the same file and has not shrunk, and
// the tracker asks about the same logs every few seconds.
var identities struct {
	sync.Mutex
	logs map[string]identity // agent and path to the log's identity
}

type identity struct {
	stat    os.FileInfo
	id, cwd string
	created time.Time
}

// scanLog is a log's identity record: its native ID, working directory and
// creation time.
func scanLog(path string, a *Agent) (string, string, time.Time) {
	stat, err := os.Stat(path)
	if err != nil {
		return "", "", time.Time{}
	}
	key := a.Name + "\x00" + path
	identities.Lock()
	known, ok := identities.logs[key]
	identities.Unlock()
	if ok && os.SameFile(known.stat, stat) && stat.Size() >= known.stat.Size() {
		return known.id, known.cwd, known.created
	}
	id, cwd, created := readIdentity(path, a)
	if id != "" {
		identities.Lock()
		// Bounded: a history scan may pass over every log there is.
		if len(identities.logs) >= 1024 {
			identities.logs = nil
		}
		if identities.logs == nil {
			identities.logs = map[string]identity{}
		}
		identities.logs[key] = identity{stat, id, cwd, created}
		identities.Unlock()
	}
	return id, cwd, created
}

func readIdentity(path string, a *Agent) (id, cwd string, created time.Time) {
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
		if !a.rollout && !r.Sidechain && r.Cwd != "" && r.SessionID != "" {
			return r.SessionID, r.Cwd, r.Timestamp
		}
		if a.rollout && r.Type == "session_meta" {
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

func readMessages(path string, a *Agent) ([]protocol.ChatMessage, bool, error) {
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
		if a.rollout {
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

// Upgrade structures the tool messages of an older daemon, which sent a call
// as "Name\n<arguments>" and its result as loose text, so a front-end only
// ever reads one form.
func Upgrade(chat *protocol.Chat) {
	for i, m := range chat.Messages {
		if m.Role != "tool" || m.Tool != "" || m.Output || m.Result != "" {
			continue
		}
		if head, rest, ok := strings.Cut(m.Text, "\n"); ok && head != "" && !strings.ContainsAny(head, " \t{}[]\"'") {
			rest = strings.TrimSpace(rest)
			var raw json.RawMessage
			if rest == "" || json.Unmarshal([]byte(rest), &raw) == nil {
				chat.Messages[i] = toolCall("", head, raw)
				continue
			}
		}
		chat.Messages[i] = protocol.ChatMessage{Role: "tool", Text: m.Text, Output: true}
	}
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
	a, ok := Lookup(info.Agent)
	if !ok {
		return errUnsupported
	}
	if id, _ := metadata(path, a); id != info.ConversationID {
		return fmt.Errorf("conversation log identity does not match session")
	}
	return nil
}

// ReadBound reads a daemon-verified path, including rollout names that carry
// a separate file ID. A missing or replaced bound log is an error.
func ReadBound(info protocol.SessionInfo, path string) (protocol.Chat, error) {
	a, ok := Lookup(info.Agent)
	out := protocol.Chat{Supported: ok, Conversation: info.ConversationID}
	if err := Verify(info, path); err != nil {
		return out, err
	}
	var err error
	out.Messages, out.Truncated, err = readMessages(path, a)
	if err == nil {
		var scan usageScan
		out.Usage = usageOf(scan.read(path, a))
	}
	return out, err
}

// usageOf is u as a chat carries it: none when nothing was counted.
func usageOf(u protocol.Usage) *protocol.Usage {
	if u == (protocol.Usage{}) {
		return nil
	}
	return &u
}

// Cache keeps the last read of a bound log, so polling a log that has not
// changed costs one stat.
type Cache struct {
	mu    sync.Mutex
	stat  os.FileInfo
	chat  protocol.Chat
	usage usageScan
}

// ReadBound is ReadBound, answered from the cache while the log is the same
// file, unchanged, and still bound to the same conversation.
func (c *Cache) ReadBound(info protocol.SessionInfo, path string) (protocol.Chat, error) {
	stat, statErr := os.Stat(path)
	c.mu.Lock()
	defer c.mu.Unlock()
	if statErr == nil && c.stat != nil && c.chat.Conversation == info.ConversationID && os.SameFile(c.stat, stat) &&
		c.stat.Size() == stat.Size() && c.stat.ModTime().Equal(stat.ModTime()) {
		return c.chat, nil
	}
	a, ok := Lookup(info.Agent)
	chat := protocol.Chat{Supported: ok, Conversation: info.ConversationID}
	err := Verify(info, path)
	if err == nil {
		chat.Messages, chat.Truncated, err = readMessages(path, a)
	}
	if err == nil {
		chat.Usage = usageOf(c.usage.read(path, a))
	}
	if err == nil && statErr == nil {
		c.stat, c.chat = stat, chat
	} else {
		c.stat = nil
	}
	return chat, err
}

// logPaths lists an agent's logs whose file name matches the glob name.
func logPaths(a *Agent, env map[string]string, name string) ([]string, error) {
	return filepath.Glob(logPattern(a, logRoot(a, env), name))
}

// logPattern is the glob of an agent's logs under root whose file name
// matches name.
func logPattern(a *Agent, root, name string) string {
	return filepath.Join(append(append([]string{root}, a.logDirs...), name)...)
}

// logRoot is the directory an agent keeps its logs under.
func logRoot(a *Agent, env map[string]string) string {
	if root := env[a.rootEnv]; root != "" {
		return root
	}
	home := env["HOME"]
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return filepath.Join(home, a.homeDir)
}

// LogOf reports whether path is where agent writes conversation id's log
// under env, whether or not it has written it yet: a path an agent reports
// is trusted no further than that. The root may be named through a link.
func LogOf(agent string, env map[string]string, id, path string) bool {
	a, ok := Lookup(agent)
	if !ok || a.rollout || !validID.MatchString(id) || !filepath.IsAbs(path) {
		return false
	}
	path = filepath.Clean(path)
	root := logRoot(a, env)
	roots := []string{root}
	if resolved, err := filepath.EvalSymlinks(root); err == nil && resolved != root {
		roots = append(roots, resolved)
	}
	return slices.ContainsFunc(roots, func(root string) bool {
		match, err := filepath.Match(logPattern(a, root, id+".jsonl"), path)
		return err == nil && match
	})
}

// Locate finds a known native ID without depending on a rollout filename.
func Locate(ctx context.Context, info protocol.SessionInfo, env map[string]string) (string, error) {
	a, ok := Lookup(info.Agent)
	// The ID goes into the glob, so only a valid one may.
	if !ok || !validID.MatchString(info.ConversationID) {
		return "", nil
	}
	paths, err := logPaths(a, env, "*"+info.ConversationID+"*.jsonl")
	if err != nil {
		return "", err
	}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		id, _ := metadata(path, a)
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
	a, ok := Lookup(info.Agent)
	if !ok {
		return "", "", errUnsupported
	}
	paths, err := logPaths(a, env, "*.jsonl")
	if err != nil {
		return "", "", err
	}
	var foundID, foundPath string
	projectLogs := false
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return "", "", err
		}
		id, cwd, created := scanLog(path, a)
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
