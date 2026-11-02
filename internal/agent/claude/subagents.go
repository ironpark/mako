package claude

import (
	"bytes"
	"cmp"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/protocol"
)

// Claude keeps the conversation of each agent its Agent tool starts in a
// log of its own beside the session's, <session>/subagents/agent-<id>.jsonl,
// with agent-<id>.meta.json saying what sort of agent it is and, for one
// run in the background, which call started it and whether it has
// finished. One in the foreground holds its call open until it is done,
// and is found by the prompt the call gave it.

type agentMeta struct {
	AgentType    string `json:"agentType"`
	Description  string `json:"description"`
	ToolUseID    string `json:"toolUseId"`
	RequestShape string `json:"requestShape"`
	Finished     *bool  `json:"finished"`
}

// agentScan is one subagent's log, read as far as it was written.
type agentScan struct {
	agent.Tail
	prompt             string // what its first message asked
	calls              int32
	tool, kind, detail string // its latest call
}

// agentScans keeps what was read of a conversation's subagents, to read
// again only what changed: the list of them while their folder is the
// same, each meta while its file is, and each log on from where it was.
// A read then costs a stat of each file. The arguments of each call that
// started one are parsed once, and again only if they change.
type agentScans struct {
	path, at   string    // the conversation's log, and the subagents folder beside it
	dir        string    // the subagents folder listed
	dirMod     time.Time // its time when it was
	metaPaths  []string
	metaByPath map[string]*metaRead // by path
	subs       map[string]*subagent // by meta path

	tasks map[string]task // the calls' arguments, by call ID
	read  uint64          // reads, to tell the tasks of calls gone
	// calls and byPrompt are the calls a read was given, by ID and by
	// prompt, kept to fill again at the next.
	calls    map[string]started
	byPrompt map[string]string
}

// started is a call that starts a subagent, and its arguments.
type started struct {
	msg *protocol.ChatMessage
	in  agent.Task
}

// task is a call's arguments, text, as parsed into in by the read'th read.
type task struct {
	text string
	in   agent.Task
	read uint64
}

// subagent is one subagent as last read.
type subagent struct {
	id, log string // its ID, and its log's path
	scan    agentScan
}

// metaRead is a meta file as last read.
type metaRead struct {
	size int64
	mod  time.Time
	meta agentMeta
	ok   bool
}

// read brings the scan up to the end of path's complete records.
//
// It reads of each record only what it keeps, as ParseMessage would read
// it: the text of the first message the agent was given, and the calls
// it made. The rest, results that carry whole files and images among
// them, is left undecoded, and once the prompt is known a record that
// makes no call is not decoded at all.
func (s *agentScan) read(path string) {
	s.Tail.Read(path, func() { *s = agentScan{} }, func(line []byte) {
		if s.prompt != "" && !bytes.Contains(line, toolUse) {
			return
		}
		var r struct {
			Message struct {
				Role    string     `json:"role"`
				Content subContent `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &r) != nil {
			return
		}
		m := &r.Message
		if m.Role != "user" && m.Role != "assistant" {
			return
		}
		if m.Content.text != nil {
			if s.prompt == "" && m.Role == "user" {
				s.prompt = strings.TrimSpace(*m.Content.text)
			}
			return
		}
		// Prose of one speaker folds together until a call or a result
		// comes between, as ParseMessage folds it: the prompt is the first
		// of the user's.
		var prose string
		inProse := false
		endProse := func() {
			if inProse && m.Role == "user" && s.prompt == "" {
				s.prompt = strings.TrimSpace(prose)
			}
			prose, inProse = "", false
		}
		for _, b := range m.Content.blocks {
			switch b.Type {
			case "text", "input_text", "output_text":
				if strings.TrimSpace(b.Text) == "" {
					continue
				}
				switch {
				case prose == "":
					prose = b.Text
				default:
					prose += "\n\n" + b.Text
				}
				inProse = true
			case "image", "input_image":
				inProse = true // kept as a file, it adds no text
			case "tool_use":
				call := agent.Call(claudeTools, b.ID, b.Name, b.Input)
				if strings.TrimSpace(call.GetText()) == "" && call.GetTool() == "" {
					continue
				}
				endProse()
				s.calls++
				s.tool, s.kind, s.detail = call.GetTool(), call.GetKind(), call.GetDetail()
			case "tool_result":
				endProse()
			}
		}
		endProse()
	})
}

// toolUse is in every record that makes a call.
var toolUse = []byte(`"tool_use"`)

// subContent is a message's content: its text, or its blocks with no more
// of each than a scan keeps.
type subContent struct {
	text   *string
	blocks []struct {
		Type  string          `json:"type"`
		Text  string          `json:"text"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	}
}

func (c *subContent) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] == '"' {
		c.text = new(string)
		return json.Unmarshal(data, c.text)
	}
	return json.Unmarshal(data, &c.blocks)
}

// claudeTools reads the subagents' calls.
var claudeTools = &Agent{}

func (*Agent) NewSubAgents() agent.SubAgentScan { return &agentScans{} }

// Read is the subagents the calls in messages started, from the
// conversation logged at path, oldest first.
func (scans *agentScans) Read(path string, messages []*protocol.ChatMessage) []*protocol.SubAgent {
	if scans.subs == nil {
		scans.metaByPath, scans.subs = map[string]*metaRead{}, map[string]*subagent{}
		scans.tasks, scans.calls, scans.byPrompt = map[string]task{}, map[string]started{}, map[string]string{}
	}
	scans.read++
	calls, byPrompt := scans.calls, scans.byPrompt
	clear(calls)
	clear(byPrompt)
	for _, m := range messages {
		if m.GetRole() != "tool" || m.GetOutput() || m.GetKind() != protocol.ToolTask || m.GetId() == "" {
			continue
		}
		t, ok := scans.tasks[m.GetId()]
		if !ok || t.text != m.GetText() {
			t = task{text: m.GetText(), in: parseTask(m.GetText())}
		}
		t.read = scans.read
		scans.tasks[m.GetId()] = t
		calls[m.GetId()] = started{m, t.in}
		if p := strings.TrimSpace(t.in.Prompt); p != "" {
			byPrompt[p] = m.GetId()
		}
	}
	if len(scans.tasks) > len(calls) {
		for id, t := range scans.tasks {
			if t.read != scans.read {
				delete(scans.tasks, id)
			}
		}
	}
	if len(calls) == 0 {
		return nil
	}
	if path != scans.path {
		scans.path, scans.at = path, filepath.Join(strings.TrimSuffix(path, filepath.Ext(path)), "subagents")
	}
	now := time.Now()
	var out []*protocol.SubAgent
	for _, metaPath := range scans.list(scans.at) {
		sub := scans.subs[metaPath]
		if sub == nil {
			log := strings.TrimSuffix(metaPath, ".meta.json") + ".jsonl"
			sub = &subagent{log: log, id: strings.TrimPrefix(filepath.Base(strings.TrimSuffix(log, ".jsonl")), "agent-")}
			scans.subs[metaPath] = sub
		}
		meta, ok := scans.readMeta(metaPath)
		if !ok {
			continue
		}
		if meta.ToolUseID != "" && calls[meta.ToolUseID].msg == nil {
			continue // started by a call out of the window
		}
		scan := &sub.scan
		scan.read(sub.log)
		callID := cmp.Or(meta.ToolUseID, byPrompt[scan.prompt])
		call, ok := calls[callID]
		if !ok {
			continue
		}
		running := call.msg.GetPending()
		if meta.RequestShape == "background" {
			running = meta.Finished == nil || !*meta.Finished
		}
		if running && now.Sub(scan.Updated) > agent.StaleAgent {
			running = false
		}
		a := &protocol.SubAgent{
			Id:     sub.id,
			CallId: callID, Running: running,
			Kind:        cmp.Or(meta.AgentType, call.in.Agent),
			Description: cmp.Or(meta.Description, call.in.Description),
			ToolCalls:   scan.calls, Tool: scan.tool, ToolKind: scan.kind, Detail: scan.detail,
			Log: sub.log,
		}
		if !scan.Started.IsZero() {
			a.Started, a.Updated = scan.Started.Unix(), scan.Updated.Unix()
		}
		out = append(out, a)
	}
	agent.SortAgents(out)
	return out
}

// parseTask is the arguments of a call that starts a subagent.
func parseTask(text string) agent.Task {
	var in agent.Task
	_ = json.Unmarshal([]byte(text), &in)
	return in
}

// list is the meta files in dir, listed again only once dir has changed.
func (scans *agentScans) list(dir string) []string {
	stat, err := os.Stat(dir)
	if err != nil {
		scans.dir, scans.metaPaths = "", nil
		return nil
	}
	if dir != scans.dir || !stat.ModTime().Equal(scans.dirMod) {
		scans.metaPaths, _ = filepath.Glob(filepath.Join(dir, "agent-*.meta.json"))
		scans.dir, scans.dirMod = dir, stat.ModTime()
	}
	return scans.metaPaths
}

// readMeta is the meta file at path, read again only once it has changed.
func (scans *agentScans) readMeta(path string) (agentMeta, bool) {
	stat, err := os.Stat(path)
	if err != nil {
		return agentMeta{}, false
	}
	m := scans.metaByPath[path]
	if m == nil || m.size != stat.Size() || !m.mod.Equal(stat.ModTime()) {
		m = &metaRead{size: stat.Size(), mod: stat.ModTime()}
		if data, err := os.ReadFile(path); err == nil && json.Unmarshal(data, &m.meta) == nil {
			m.ok = true
		}
		scans.metaByPath[path] = m
	}
	return m.meta, m.ok
}
