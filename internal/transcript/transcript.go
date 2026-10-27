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

	"github.com/ironpark/mako/internal/protocol"
)

const maxRead = 2 << 20

var validID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)

var errUnsupported = errors.New("agent has no native conversation")

// Read reads the conversation bound to info's ID. A log the agent has not
// written yet yields the ID without messages.
func Read(ctx context.Context, info *protocol.SessionInfo, env map[string]string) (*protocol.Chat, error) {
	a, ok := Lookup(info.GetAgent())
	out := &protocol.Chat{Supported: ok}
	if !ok || info.GetConversationId() == "" {
		return out, nil
	}
	if !validID.MatchString(info.GetConversationId()) {
		return out, errors.New("invalid conversation ID")
	}
	out.Conversation = info.GetConversationId()
	path, err := Locate(ctx, info, env)
	if err != nil || path == "" {
		return out, err
	}
	if _, ok := logProject(path, a, info.GetProject()); !ok {
		return out, nil
	}
	w, err := readWindow(path, a, Span{})
	w.fill(out)
	if err == nil {
		out.Agents = new(agentScans).read(a, path, out.Messages)
	}
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
	Type      string `json:"type"`
	Cwd       string `json:"cwd"`
	SessionID string `json:"sessionId"`
	Sidechain bool   `json:"isSidechain"`
	// Meta is a turn Claude Code adds for the model, such as the prompt a
	// slash command expands to; Summary is the summary a compaction leaves.
	Meta    bool            `json:"isMeta"`
	Summary bool            `json:"isCompactSummary"`
	UUID    string          `json:"uuid"`
	Message json.RawMessage `json:"message"`
	Payload json.RawMessage `json:"payload"`
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

// Span is the part of a log a read takes its messages from: the records
// from After up to Before, byte offsets at records' starts. Before 0 is the
// log's end and After 0 its beginning, so the zero Span is the latest
// messages. A read keeps the latest of what it finds, as many as a poll can
// send.
type Span struct{ After, Before int64 }

// window is a read of a log's messages: from start, the byte offset of the
// first message's record, or of the read itself when nothing it read was
// left out, so a read up to start finds what comes before.
type window struct {
	messages  []*protocol.ChatMessage
	start     int64
	truncated bool // older messages or long content left out
}

// fill gives chat the window's messages.
func (w window) fill(chat *protocol.Chat) {
	chat.Messages, chat.Start, chat.Truncated = w.messages, w.start, w.truncated
}

func readWindow(path string, a *Agent, span Span) (window, error) {
	f, err := os.Open(path)
	if err != nil {
		return window{}, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return window{}, err
	}
	end := stat.Size()
	if span.Before > 0 {
		end = min(span.Before, end)
	}
	begin := max(end-maxRead, span.After, 0)
	truncated := begin > span.After
	pos := begin // where the next record starts
	if begin > 0 {
		// The read may begin inside a record, and then the next one is the
		// first. It begins a byte early, to see the end of the one before.
		pos--
	}
	if _, err = f.Seek(pos, io.SeekStart); err != nil {
		return window{}, err
	}
	reader := bufio.NewReader(io.LimitReader(f, end-pos))
	if pos > 0 {
		skipped, _ := reader.ReadBytes('\n')
		pos += int64(len(skipped))
	}
	start := pos
	var messages []*protocol.ChatMessage
	var offsets []int64 // the start of each message's record
	// A tool call's ID to its place in messages, counting the dropped ones,
	// so dropping never has to move the places.
	calls := map[string]int{}
	dropped := 0
	seen := map[string]bool{}
	size := 0
	command := "" // the last slash command typed, whose output follows it
	// trim keeps the window small enough to send, dropping the oldest
	// messages. The messages of one record go together, so the window
	// starts at a record and a read up to there ends at it.
	trim := func() {
		for len(messages) > 0 && (len(messages) > 200 || size > 512<<10) {
			gone := offsets[0]
			for len(messages) > 0 && offsets[0] == gone {
				size -= len(messages[0].GetText()) + len(messages[0].GetResult())
				messages, offsets = messages[1:], offsets[1:]
				dropped++
			}
		}
	}
	for {
		line, err := reader.ReadBytes('\n')
		// The writer may be in the middle of a record. Wait for the next poll.
		if err == io.EOF {
			break
		}
		if err != nil {
			return window{truncated: truncated}, err
		}
		at := pos
		pos += int64(len(line))
		var r record
		if json.Unmarshal(line, &r) != nil {
			continue
		}
		if r.Sidechain || r.Meta {
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
		parsed := parseMessage(raw)
		if r.Summary {
			for i, m := range parsed {
				parsed[i] = &protocol.ChatMessage{Role: "tool", Tool: "Summary", Detail: OneLine(m.GetText(), 120), Text: m.GetText(), Output: true}
			}
		} else {
			parsed = unwrap(parsed, &command)
		}
		for _, m := range parsed {
			if retyped(messages, m) {
				continue
			}
			if len(m.GetText()) > 32<<10 {
				m.Text = trimUTF8(m.GetText(), 32<<10) + "\n…"
				truncated = true
			}
			// A result belongs to the call it answers: one row in the chat
			// rather than a second line of loose output.
			if m.GetRole() == "tool" && m.GetOutput() && m.GetId() != "" {
				if i, ok := calls[m.GetId()]; ok && i >= dropped {
					messages[i-dropped].Result = m.GetText()
					messages[i-dropped].Error = m.GetError()
					messages[i-dropped].Pending = false
					size += len(m.GetText())
					trim()
					continue
				}
			}
			if m.GetRole() == "tool" && !m.GetOutput() && m.GetId() != "" {
				calls[m.GetId()] = dropped + len(messages)
				m.Pending = true
			}
			messages = append(messages, m)
			offsets = append(offsets, at)
			size += len(m.GetText())
			trim()
		}
	}
	if dropped > 0 {
		truncated, start = true, pos
		if len(offsets) > 0 {
			start = offsets[0]
		}
	}
	return window{messages: messages, start: start, truncated: truncated}, nil
}

// retyped reports whether m is a slash command the log already holds as
// typed: Claude Code writes /compact down as the user typed it, then again
// as a command after the summary, or after the error that ended it.
func retyped(messages []*protocol.ChatMessage, m *protocol.ChatMessage) bool {
	if m.GetRole() != "user" || !strings.HasPrefix(m.GetText(), "/") {
		return false
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if p := messages[i]; p.GetTool() != "Summary" {
			return p.GetRole() == "user" && p.GetText() == m.GetText()
		}
	}
	return false
}

func trimUTF8(s string, n int) string {
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func parseMessage(raw json.RawMessage) []*protocol.ChatMessage {
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
		return []*protocol.ChatMessage{toolCall(cmp.Or(m.ID, m.CallID), m.Name, input)}
	case "function_call_output", "custom_tool_call_output":
		return []*protocol.ChatMessage{{
			Role: "tool", Id: m.CallID, Text: contentText(m.Output), Output: true,
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
			msg = toolCall(b.ID, b.Name, b.Input)
		case "tool_result":
			msg = &protocol.ChatMessage{Role: "tool", Id: b.ToolUseID, Text: contentText(b.Content), Output: true, Error: b.IsError}
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

// toolCall is a tool message from its call: the name, a one-line detail for
// the chat's header, and the arguments as pretty JSON.
func toolCall(id, name string, input json.RawMessage) *protocol.ChatMessage {
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

// Upgrade structures the tool messages of an older daemon, which sent a call
// as "Name\n<arguments>" and its result as loose text, so a front-end only
// ever reads one form.
func Upgrade(chat *protocol.Chat) {
	for i, m := range chat.GetMessages() {
		if m.GetRole() != "tool" || m.GetTool() != "" || m.GetOutput() || m.GetResult() != "" {
			continue
		}
		if head, rest, ok := strings.Cut(m.GetText(), "\n"); ok && head != "" && !strings.ContainsAny(head, " \t{}[]\"'") {
			rest = strings.TrimSpace(rest)
			var raw json.RawMessage
			if rest == "" || json.Unmarshal([]byte(rest), &raw) == nil {
				chat.Messages[i] = toolCall("", head, raw)
				continue
			}
		}
		chat.Messages[i] = &protocol.ChatMessage{Role: "tool", Text: m.GetText(), Output: true}
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
func Verify(info *protocol.SessionInfo, path string) error {
	a, ok := Lookup(info.GetAgent())
	if !ok {
		return errUnsupported
	}
	if id, _ := metadata(path, a); id != info.GetConversationId() {
		return fmt.Errorf("conversation log identity does not match session")
	}
	return nil
}

// ReadBound reads span of a daemon-verified path, including rollout names
// that carry a separate file ID. A missing or replaced bound log is an
// error.
func ReadBound(info *protocol.SessionInfo, path string, span Span) (*protocol.Chat, error) {
	a, ok := Lookup(info.GetAgent())
	out := &protocol.Chat{Supported: ok, Conversation: info.GetConversationId()}
	if err := Verify(info, path); err != nil {
		return out, err
	}
	w, err := readWindow(path, a, span)
	w.fill(out)
	if err == nil {
		var scan usageScan
		out.Usage = scan.read(path, a)
		out.Agents = new(agentScans).read(a, path, out.Messages)
	}
	return out, err
}

// Cache keeps the last read of a bound log, so polling a log that has not
// changed costs one stat.
type Cache struct {
	mu     sync.Mutex
	stat   os.FileInfo
	chat   *protocol.Chat // shared with every caller, which must not change it
	usage  usageScan
	agents agentScans
}

// ReadBound is ReadBound of the latest messages, answered from the cache
// while the log is the same file, unchanged, and still bound to the same
// conversation. An earlier span does not change, so it is read as asked.
func (c *Cache) ReadBound(info *protocol.SessionInfo, path string, span Span) (*protocol.Chat, error) {
	if span != (Span{}) {
		return ReadBound(info, path, span)
	}
	stat, statErr := os.Stat(path)
	c.mu.Lock()
	defer c.mu.Unlock()
	a, ok := Lookup(info.GetAgent())
	if statErr == nil && c.stat != nil && c.chat.GetConversation() == info.GetConversationId() && os.SameFile(c.stat, stat) &&
		c.stat.Size() == stat.Size() && c.stat.ModTime().Equal(stat.ModTime()) {
		// A subagent works in a log of its own while the session's is
		// quiet: the agents are read again while one is running.
		if slices.ContainsFunc(c.chat.GetAgents(), (*protocol.SubAgent).GetRunning) {
			if agents := c.agents.read(a, path, c.chat.GetMessages()); !slices.EqualFunc(agents, c.chat.GetAgents(), protocol.Same) {
				c.chat = withAgents(c.chat, agents)
			}
		}
		return c.chat, nil
	}
	if c.chat.GetConversation() != info.GetConversationId() {
		c.agents = agentScans{} // another conversation's
	}
	chat := &protocol.Chat{Supported: ok, Conversation: info.GetConversationId()}
	err := Verify(info, path)
	if err == nil {
		var w window
		w, err = readWindow(path, a, Span{})
		w.fill(chat)
	}
	if err == nil {
		chat.Usage = c.usage.read(path, a)
		chat.Agents = c.agents.read(a, path, chat.Messages)
	}
	if err == nil && statErr == nil {
		c.stat, c.chat = stat, chat
	} else {
		c.stat = nil
	}
	return chat, err
}

// withAgents is chat with other agents. chat is shared, so it is copied,
// all but the agents; a field Chat gains is copied here too.
func withAgents(chat *protocol.Chat, agents []*protocol.SubAgent) *protocol.Chat {
	return &protocol.Chat{
		Supported: chat.GetSupported(), Conversation: chat.GetConversation(), Messages: chat.GetMessages(),
		Truncated: chat.GetTruncated(), Start: chat.GetStart(), Usage: chat.GetUsage(), Agents: agents,
	}
}

// ReadUsage is a daemon-verified log's usage alone, read on from where the
// cache's last read of it stopped.
func (c *Cache) ReadUsage(info *protocol.SessionInfo, path string) (*protocol.Usage, error) {
	a, ok := Lookup(info.GetAgent())
	if !ok {
		return nil, errUnsupported
	}
	if err := Verify(info, path); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.usage.read(path, a), nil
}

// ConfigRoot is the directory agent keeps its settings and logs under,
// given env; "" for an agent mako does not know.
func ConfigRoot(agent string, env map[string]string) string {
	a, ok := Lookup(agent)
	if !ok {
		return ""
	}
	return logRoot(a, env)
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
func Locate(ctx context.Context, info *protocol.SessionInfo, env map[string]string) (string, error) {
	a, ok := Lookup(info.GetAgent())
	// The ID goes into the glob, so only a valid one may.
	if !ok || !validID.MatchString(info.GetConversationId()) {
		return "", nil
	}
	paths, err := logPaths(a, env, "*"+info.GetConversationId()+"*.jsonl")
	if err != nil {
		return "", err
	}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		id, _ := metadata(path, a)
		if id == info.GetConversationId() {
			return path, nil
		}
	}
	return "", nil
}

// Recover is a one-time migration for launches saved before native IDs were
// recorded. Only one unclaimed log created during that launch can be adopted;
// ambiguous history is never replaced with a fresh conversation.
func Recover(ctx context.Context, info *protocol.SessionInfo, env map[string]string, claimed map[string]bool) (string, string, error) {
	a, ok := Lookup(info.GetAgent())
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
		if id == "" || !validID.MatchString(id) || claimed[id] || !samePath(cwd, info.GetProject()) {
			continue
		}
		projectLogs = true
		if created.IsZero() || created.Before(protocol.TimeOf(info.GetStartedAt()).Add(-2*time.Second)) {
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
