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
// A read while one runs then costs a stat of each file.
type agentScans struct {
	dir        string    // the subagents folder listed
	dirMod     time.Time // its time when it was
	metaPaths  []string
	metaByPath map[string]*metaRead  // by path
	scans      map[string]*agentScan // by log path
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
	type started struct {
		msg *protocol.ChatMessage
		in  agent.Task
	}
	calls := map[string]started{}
	byPrompt := map[string]string{}
	for _, m := range messages {
		if m.GetRole() != "tool" || m.GetOutput() || m.GetKind() != protocol.ToolTask || m.GetId() == "" {
			continue
		}
		var in agent.Task
		_ = json.Unmarshal([]byte(m.GetText()), &in)
		calls[m.GetId()] = started{m, in}
		if p := strings.TrimSpace(in.Prompt); p != "" {
			byPrompt[p] = m.GetId()
		}
	}
	if len(calls) == 0 {
		return nil
	}
	if scans.scans == nil {
		scans.metaByPath, scans.scans = map[string]*metaRead{}, map[string]*agentScan{}
	}
	now := time.Now()
	var out []*protocol.SubAgent
	for _, metaPath := range scans.list(filepath.Join(strings.TrimSuffix(path, filepath.Ext(path)), "subagents")) {
		meta, ok := scans.readMeta(metaPath)
		if !ok {
			continue
		}
		if meta.ToolUseID != "" && calls[meta.ToolUseID].msg == nil {
			continue // started by a call out of the window
		}
		logPath := strings.TrimSuffix(metaPath, ".meta.json") + ".jsonl"
		scan := scans.scans[logPath]
		if scan == nil {
			scan = &agentScan{}
			scans.scans[logPath] = scan
		}
		scan.read(logPath)
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
		sub := &protocol.SubAgent{
			Id:     strings.TrimPrefix(filepath.Base(strings.TrimSuffix(logPath, ".jsonl")), "agent-"),
			CallId: callID, Running: running,
			Kind:        cmp.Or(meta.AgentType, call.in.Agent),
			Description: cmp.Or(meta.Description, call.in.Description),
			ToolCalls:   scan.calls, Tool: scan.tool, ToolKind: scan.kind, Detail: scan.detail,
			Log: logPath,
		}
		if !scan.Started.IsZero() {
			sub.Started, sub.Updated = scan.Started.Unix(), scan.Updated.Unix()
		}
		out = append(out, sub)
	}
	agent.SortAgents(out)
	return out
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
