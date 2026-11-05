// Package transcript reads native agent logs without interpreting terminal
// redraws as conversation. It never writes logs or starts an agent.
package transcript

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agents"
	"github.com/ironpark/mako/internal/protocol"
	"google.golang.org/protobuf/proto"
)

const maxRead = 2 << 20

// maxText caps a message's text and a call's result, each.
const maxText = 32 << 10

var errUnsupported = errors.New("agent has no native conversation")

// Read reads the conversation bound to info's ID. A log the agent has not
// written yet yields the ID without messages.
func Read(ctx context.Context, info *protocol.SessionInfo, env map[string]string) (*protocol.Chat, error) {
	a, ok := logsOf(info.GetAgent())
	out := &protocol.Chat{Supported: ok}
	if !ok || info.GetConversationId() == "" {
		return out, nil
	}
	if !agent.ValidID.MatchString(info.GetConversationId()) {
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
		out.Agents = newSubAgents(a).Read(path, out.Messages)
	}
	return out, err
}

// newSubAgents follows the subagents of a conversation of a's, which finds
// none for an agent whose calls start none.
func newSubAgents(a agent.Logs) agent.SubAgentScan {
	if s, ok := a.(agent.SubAgents); ok {
		return s.NewSubAgents()
	}
	return noSubAgents{}
}

type noSubAgents struct{}

func (noSubAgents) Read(string, []*protocol.ChatMessage) []*protocol.SubAgent { return nil }

// logProject reads a log's identity and reports whether it is a usable
// conversation of project.
func logProject(path string, a agent.Logs, project string) (string, bool) {
	id, cwd := metadata(path, a)
	return id, id != "" && agent.ValidID.MatchString(id) && samePath(cwd, project)
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
	return pathKey(a, runtime.GOOS) == pathKey(b, runtime.GOOS)
}

// pathKey is path as two names of one folder compare on goos: on Windows,
// whose names ignore case, without the \\?\ a native program's own
// paths may carry, as Codex's may.
func pathKey(path, goos string) string {
	if goos != "windows" {
		return filepath.Clean(path)
	}
	path = strings.TrimPrefix(path, `\\?\`)
	return strings.ToLower(filepath.Clean(path))
}

func metadata(path string, a agent.Logs) (string, string) {
	id, cwd, _ := scanLog(path, a)
	return id, cwd
}

// identities remembers the identity records already read. A log only grows,
// so its identity holds while it is the same file and has not shrunk, and
// the tracker asks about the same logs every few seconds.
var identities struct {
	sync.Mutex
	logs map[string]identity // a log's path to its identity: no log is two agents'
}

type identity struct {
	stat    os.FileInfo
	id, cwd string
	created time.Time
}

// scanLog is a log's identity record: its native ID, working directory and
// creation time.
func scanLog(path string, a agent.Logs) (string, string, time.Time) {
	stat, err := os.Stat(path)
	if err != nil {
		return "", "", time.Time{}
	}
	identities.Lock()
	known, ok := identities.logs[path]
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
		identities.logs[path] = identity{stat, id, cwd, created}
		identities.Unlock()
	}
	return id, cwd, created
}

func readIdentity(path string, a agent.Logs) (id, cwd string, created time.Time) {
	if f, ok := a.(agent.Folders); ok {
		return f.Whose(path)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", "", time.Time{}
	}
	defer f.Close()
	s := bufio.NewScanner(io.LimitReader(f, maxRead))
	s.Buffer(make([]byte, 4096), maxRead)
	for s.Scan() {
		if id, cwd, created, done := a.Identity(s.Bytes()); done {
			return id, cwd, created
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

func readWindow(path string, a agent.Logs, span Span) (window, error) {
	tools, _ := a.(agent.Tools)
	return readLog(path, a.NewDecoder(), tools, span)
}

// readLog reads span of the log at path with dec, as readWindow does.
func readLog(path string, dec agent.Decoder, tools agent.Tools, span Span) (window, error) {
	r := logRead{dec: dec, tools: tools}
	err := r.read(path, span)
	return r.window(), err
}

// logRead is a read of a log under way: the messages it keeps so far and
// where it stopped, so a read of the log grown since goes on from there
// with the same decoder, as one read of it all would, rather than
// decoding the latest maxRead of it again.
type logRead struct {
	dec   agent.Decoder
	tools agent.Tools

	begun     bool
	skip      bool  // the read began inside a record, which it leaves out
	pos       int64 // where the next record starts
	start     int64 // where the read began, after what it skipped
	truncated bool

	messages []*protocol.ChatMessage
	offsets  []int64 // the start of each message's record
	// A tool call's ID to its place in messages, counting the dropped ones,
	// so dropping never has to move the places.
	calls   map[string]int
	dropped int
	size    int
	// handed is how many of the first messages the last window gave out,
	// which callers share: one of them, or the slice holding them, is
	// changed only as a copy.
	handed int
	shared bool // messages' backing array is a window's
}

// read reads on to span's end, from where the last read stopped or, the
// first time, from as far back as a poll can send.
func (r *logRead) read(path string, span Span) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return err
	}
	end := stat.Size()
	if span.Before > 0 {
		end = min(span.Before, end)
	}
	if !r.begun {
		r.begun = true
		begin := max(end-maxRead, span.After, 0)
		r.truncated = begin > span.After
		r.pos, r.start = begin, begin
		if begin > 0 {
			// The read may begin inside a record, and then the next one is
			// the first. It begins a byte early, to see the end of the one
			// before.
			r.pos, r.skip = begin-1, true
		}
	}
	if end <= r.pos {
		return nil
	}
	if _, err = f.Seek(r.pos, io.SeekStart); err != nil {
		return err
	}
	reader := agent.BorrowReader(io.LimitReader(f, end-r.pos))
	var long []byte // a record longer than reader's buffer, put together
	defer func() { agent.GiveBack(reader, long) }()
	if r.skip {
		skipped, err := agent.ReadRecord(reader, &long)
		if err == io.EOF {
			return nil // the record skipped is still being written
		}
		if err != nil {
			return err
		}
		r.pos += int64(len(skipped))
		r.start, r.skip = r.pos, false
	}
	for {
		line, err := agent.ReadRecord(reader, &long)
		// The writer may be in the middle of a record. Wait for the next poll.
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		at := r.pos
		r.pos += int64(len(line))
		r.add(at, r.dec.Line(line))
	}
}

// add keeps the messages decoded from the record at at.
func (r *logRead) add(at int64, parsed []*protocol.ChatMessage) {
	for _, m := range parsed {
		for _, s := range []*string{&m.Text, &m.Result} {
			if len(*s) > maxText {
				*s = trimUTF8(*s, maxText) + "\n…"
				r.truncated = true
			}
		}
		// A result belongs to the call it answers: one row in the chat
		// rather than a second line of loose output.
		if m.GetRole() == "tool" && m.GetOutput() && m.GetId() != "" {
			if i, ok := r.calls[m.GetId()]; ok && i >= r.dropped {
				call := r.own(i - r.dropped)
				call.Result, call.Error, call.ExitCode, call.Pending = m.GetText(), m.GetError(), m.ExitCode, false
				call.Detail = cmp.Or(call.GetDetail(), m.GetDetail())
				if r.tools != nil {
					// What the call's tool gave back, out of what the agent
					// wrapped it in.
					result, exit := r.tools.Output(call.GetKind(), call.GetResult())
					call.Result = result
					if exit >= 0 {
						call.ExitCode = proto.Int32(int32(exit))
					}
				}
				r.size += len(m.GetText())
				r.trim()
				continue
			}
		}
		if m.GetRole() == "tool" && !m.GetOutput() && m.GetId() != "" {
			if r.calls == nil {
				r.calls = map[string]int{}
			}
			r.calls[m.GetId()] = r.dropped + len(r.messages)
			m.Pending = m.GetResult() == "" // a call read with its result is done
		}
		r.messages = append(r.messages, m)
		r.offsets = append(r.offsets, at)
		r.size += len(m.GetText()) + len(m.GetResult())
		r.trim()
	}
}

// own is messages[i], to be changed: a copy of it, in a copy of the
// slice, when a window gave them out.
func (r *logRead) own(i int) *protocol.ChatMessage {
	if i >= r.handed {
		return r.messages[i]
	}
	if r.shared {
		r.messages, r.shared = slices.Clone(r.messages), false
	}
	m := proto.CloneOf(r.messages[i])
	r.messages[i] = m
	return m
}

// trim keeps the window small enough to send, dropping the oldest
// messages. The messages of one record go together, so the window starts
// at a record and a read up to there ends at it.
func (r *logRead) trim() {
	for len(r.messages) > 0 && (len(r.messages) > 200 || r.size > 512<<10) {
		gone := r.offsets[0]
		for len(r.messages) > 0 && r.offsets[0] == gone {
			r.size -= len(r.messages[0].GetText()) + len(r.messages[0].GetResult())
			r.messages, r.offsets = r.messages[1:], r.offsets[1:]
			r.dropped++
			r.handed = max(r.handed-1, 0)
		}
	}
	// The calls of dropped messages can no longer be answered.
	if len(r.calls) > 2*len(r.messages)+64 {
		maps.DeleteFunc(r.calls, func(_ string, i int) bool { return i < r.dropped })
	}
}

// window is what the read keeps, for callers to share.
func (r *logRead) window() window {
	r.handed, r.shared = len(r.messages), true
	w := window{messages: r.messages, start: r.start, truncated: r.truncated}
	if r.dropped > 0 {
		w.truncated, w.start = true, r.pos
		if len(r.offsets) > 0 {
			w.start = r.offsets[0]
		}
	}
	return w
}

func trimUTF8(s string, n int) string {
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// Upgrade structures the tool messages of an older daemon, which sent a call
// as "Name\n<arguments>" and its result as loose text, and names the kinds
// of tool a daemon before drivers left unnamed, so a front-end only ever
// reads one form.
func Upgrade(chat *protocol.Chat) {
	for i, m := range chat.GetMessages() {
		if m.GetRole() != "tool" || m.GetTool() != "" || m.GetOutput() || m.GetResult() != "" {
			continue
		}
		if head, rest, ok := strings.Cut(m.GetText(), "\n"); ok && head != "" && !strings.ContainsAny(head, " \t{}[]\"'") {
			rest = strings.TrimSpace(rest)
			var raw json.RawMessage
			if rest == "" || json.Unmarshal([]byte(rest), &raw) == nil {
				chat.Messages[i] = agent.ToolCall("", head, raw)
				continue
			}
		}
		chat.Messages[i] = &protocol.ChatMessage{Role: "tool", Text: m.GetText(), Output: true}
	}
	// A daemon before drivers sent the tools' arguments and results as its
	// agent wrote them: they are read here as the first driver that knows
	// the tool reads them.
	for _, m := range chat.GetMessages() {
		if m.GetRole() != "tool" || m.GetTool() == "" || m.GetKind() != "" || m.GetOutput() {
			continue
		}
		tools, kind := anyTools(m.GetTool())
		if tools == nil {
			continue
		}
		raw := json.RawMessage(m.GetText())
		if !json.Valid(raw) {
			raw, _ = json.Marshal(m.GetText())
		}
		read := agent.Call(tools, m.GetId(), m.GetTool(), raw)
		m.Kind, m.Text, m.Detail = cmp.Or(read.GetKind(), kind), read.GetText(), read.GetDetail()
		if m.GetResult() != "" {
			var exit int
			if m.Result, exit = tools.Output(m.GetKind(), m.GetResult()); exit >= 0 {
				m.ExitCode = proto.Int32(int32(exit))
			}
		}
	}
	for _, a := range chat.GetAgents() {
		if a.GetTool() != "" && a.GetToolKind() == "" {
			_, a.ToolKind = anyTools(a.GetTool())
		}
	}
}

// anyTools is the tools of the first agent that knows the tool name, and
// what the tool does.
func anyTools(name string) (agent.Tools, string) {
	for _, d := range agents.All() {
		if t, ok := d.(agent.Tools); ok {
			if kind, _ := t.Tool(name, nil); kind != "" {
				return t, kind
			}
		}
	}
	return nil, ""
}

// Verify reports whether a bound log is present and carries the session's
// conversation ID, without reading its messages.
func Verify(info *protocol.SessionInfo, path string) error {
	a, ok := logsOf(info.GetAgent())
	return verify(a, ok, info, path)
}

// verify is Verify with the agent's logs looked up, ok false for none.
func verify(a agent.Logs, ok bool, info *protocol.SessionInfo, path string) error {
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
	a, ok := logsOf(info.GetAgent())
	out := &protocol.Chat{Supported: ok, Conversation: info.GetConversationId()}
	if err := verify(a, ok, info, path); err != nil {
		return out, err
	}
	w, err := readWindow(path, a, span)
	w.fill(out)
	if err == nil {
		var scan usageScan
		out.Usage = scan.read(path, a)
		out.Agents = newSubAgents(a).Read(path, out.Messages)
	}
	return out, err
}

// ReadAgent reads span of the log at path of one of the session's
// subagents, which the session's own read found: its messages alone, read
// as the driver reads a subagent's log.
func ReadAgent(info *protocol.SessionInfo, path string, span Span) (*protocol.Chat, error) {
	a, ok := logsOf(info.GetAgent())
	out := &protocol.Chat{Supported: ok, Conversation: info.GetConversationId()}
	if !ok {
		return out, nil
	}
	dec := a.NewDecoder()
	if sub, ok := a.(agent.SubAgentLogs); ok {
		dec = sub.NewAgentDecoder()
	}
	tools, _ := a.(agent.Tools)
	w, err := readLog(path, dec, tools, span)
	w.fill(out)
	return out, err
}

// Cache keeps the last read of a bound log, so polling a log that has not
// changed costs one stat.
type Cache struct {
	mu     sync.Mutex
	stat   os.FileInfo
	chat   *protocol.Chat // shared with every caller, which must not change it
	live   *logRead       // the read chat came from, which a poll goes on with
	usage  usageScan
	agents agent.SubAgentScan // nil until a read
	// subs are the latest reads of the subagents' logs, by log path.
	subs map[string]subRead
	// readAt is when the cache was last read from (see Forget).
	readAt time.Time
}

// Forget lets go of the conversation read, and its subagents', when
// nobody read it since idle ago, and reports whether it did: a session's
// conversation is followed for as long as it runs, and a front-end reads
// the one it shows. The next read reads the log again. The usage and the
// subagents found are kept, as they are scanned on from where they stopped.
func (c *Cache) Forget(idle time.Duration, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.chat == nil || c.live == nil && c.subs == nil || now.Sub(c.readAt) < idle {
		return false
	}
	c.stat, c.live, c.subs = nil, nil, nil
	// The conversation's ID stays, which the subagents' scan is kept by.
	c.chat = &protocol.Chat{Conversation: c.chat.GetConversation()}
	return true
}

// subRead is a subagent's log as last read.
type subRead struct {
	stat os.FileInfo
	chat *protocol.Chat
}

// Agent is the subagent id of the conversation as last read, nil for one
// it does not list.
func (c *Cache) Agent(id string) *protocol.SubAgent {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, a := range c.chat.GetAgents() {
		if a.GetId() == id {
			return a
		}
	}
	return nil
}

// ReadAgent is ReadAgent, its latest messages answered from the cache
// while the subagent's log is unchanged. The reply is shared, and must not
// be changed.
func (c *Cache) ReadAgent(info *protocol.SessionInfo, path string, span Span) (*protocol.Chat, error) {
	c.touch()
	if span != (Span{}) {
		return ReadAgent(info, path, span)
	}
	stat, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	r, ok := c.subs[path]
	c.mu.Unlock()
	if ok && os.SameFile(r.stat, stat) && r.stat.Size() == stat.Size() && r.stat.ModTime().Equal(stat.ModTime()) {
		return r.chat, nil
	}
	chat, err := ReadAgent(info, path, span)
	if err == nil {
		c.mu.Lock()
		if c.subs == nil {
			c.subs = map[string]subRead{}
		}
		c.subs[path] = subRead{stat: stat, chat: chat}
		c.mu.Unlock()
	}
	return chat, err
}

// readSpan is ReadBound of an earlier span, read as asked, with the usage
// and the subagents the cache keeps scanning as the log grows: a page read
// would otherwise scan the whole log and every subagent's again.
func (c *Cache) readSpan(info *protocol.SessionInfo, path string, span Span) (*protocol.Chat, error) {
	a, ok := logsOf(info.GetAgent())
	out := &protocol.Chat{Supported: ok, Conversation: info.GetConversationId()}
	if err := verify(a, ok, info, path); err != nil {
		return out, err
	}
	w, err := readWindow(path, a, span)
	w.fill(out)
	if err == nil {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.agents == nil || c.chat.GetConversation() != info.GetConversationId() {
			c.agents = newSubAgents(a) // another conversation's
		}
		out.Usage = c.usage.read(path, a)
		out.Agents = c.agents.Read(path, out.Messages)
	}
	return out, err
}

// ReadBound is ReadBound of the latest messages, answered from the cache
// while the log is the same file, unchanged, and still bound to the same
// conversation. An earlier span does not change, so it is read as asked.
func (c *Cache) ReadBound(info *protocol.SessionInfo, path string, span Span) (*protocol.Chat, error) {
	c.touch()
	if span != (Span{}) {
		return c.readSpan(info, path, span)
	}
	stat, statErr := os.Stat(path)
	c.mu.Lock()
	defer c.mu.Unlock()
	a, ok := logsOf(info.GetAgent())
	if statErr == nil && c.stat != nil && c.chat.GetConversation() == info.GetConversationId() && os.SameFile(c.stat, stat) &&
		c.stat.Size() == stat.Size() && c.stat.ModTime().Equal(stat.ModTime()) {
		// A subagent works in a log of its own while the session's is
		// quiet: the agents are read again while one is running.
		if slices.ContainsFunc(c.chat.GetAgents(), (*protocol.SubAgent).GetRunning) {
			if agents := c.agents.Read(path, c.chat.GetMessages()); !slices.EqualFunc(agents, c.chat.GetAgents(), protocol.Same) {
				c.chat = withAgents(c.chat, agents)
			}
		}
		return c.chat, nil
	}
	chat := &protocol.Chat{Supported: ok, Conversation: info.GetConversationId()}
	err := verify(a, ok, info, path)
	if err == nil && (c.agents == nil || c.chat.GetConversation() != info.GetConversationId()) {
		c.agents = newSubAgents(a) // another conversation's
	}
	if err == nil {
		// The same log grown: read on from where the last read stopped.
		if c.live == nil || c.stat == nil || statErr != nil || c.chat.GetConversation() != info.GetConversationId() ||
			!os.SameFile(c.stat, stat) || stat.Size() < c.stat.Size() {
			tools, _ := a.(agent.Tools)
			c.live = &logRead{dec: a.NewDecoder(), tools: tools}
		}
		err = c.live.read(path, Span{})
		c.live.window().fill(chat)
	}
	if err == nil {
		chat.Usage = c.usage.read(path, a)
		chat.Agents = c.agents.Read(path, chat.Messages)
	}
	if err == nil && statErr == nil {
		c.stat, c.chat = stat, chat
	} else {
		c.stat, c.live = nil, nil
	}
	return chat, err
}

// touch notes a read, which keeps the cache from Forget.
func (c *Cache) touch() {
	c.mu.Lock()
	c.readAt = time.Now()
	c.mu.Unlock()
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
	a, ok := logsOf(info.GetAgent())
	if err := verify(a, ok, info, path); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.usage.read(path, a), nil
}

// logPaths lists an agent's logs that may be conversation id's, every
// one for id "".
func logPaths(a agent.Logs, env map[string]string, id string) ([]string, error) {
	name := "*.jsonl"
	if id != "" {
		name = "*" + id + name
	}
	return filepath.Glob(logGlob(a, a.Place().Root(env), cmp.Or(id, "*"), name))
}

// logGlob is the glob of conversation id's logs under root: in its folder,
// for an agent that keeps one, else those named name.
func logGlob(a agent.Logs, root, id, name string) string {
	if f, ok := a.(agent.Folders); ok {
		return f.LogPath(root, id)
	}
	return a.Place().Pattern(root, name)
}

// LogOf reports whether path is where agent writes conversation id's log
// under env, whether or not it has written it yet: a path an agent reports
// is trusted no further than that. The root may be named through a link.
func LogOf(name string, env map[string]string, id, path string) bool {
	a, ok := logsOf(name)
	if !ok || !agent.ValidID.MatchString(id) || !filepath.IsAbs(path) {
		return false
	}
	if _, folders := a.(agent.Folders); !folders && a.LogName(id) == "" {
		return false
	}
	path = filepath.Clean(path)
	root := a.Place().Root(env)
	roots := []string{root}
	if resolved, err := filepath.EvalSymlinks(root); err == nil && resolved != root {
		roots = append(roots, resolved)
	}
	return slices.ContainsFunc(roots, func(root string) bool {
		match, err := filepath.Match(logGlob(a, root, id, a.LogName(id)), path)
		return err == nil && match
	})
}

// Locate finds a known native ID without depending on a rollout filename.
func Locate(ctx context.Context, info *protocol.SessionInfo, env map[string]string) (string, error) {
	a, ok := logsOf(info.GetAgent())
	// The ID goes into the glob, so only a valid one may.
	if !ok || !agent.ValidID.MatchString(info.GetConversationId()) {
		return "", nil
	}
	paths, err := logPaths(a, env, info.GetConversationId())
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
	a, ok := logsOf(info.GetAgent())
	if !ok {
		return "", "", errUnsupported
	}
	paths, err := logPaths(a, env, "")
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
		if id == "" || !agent.ValidID.MatchString(id) || claimed[id] || !samePath(cwd, info.GetProject()) {
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
