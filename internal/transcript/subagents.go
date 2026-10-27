package transcript

import (
	"bufio"
	"cmp"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ironpark/sanbo/internal/protocol"
)

// Claude keeps the conversation of each agent its Agent tool starts in a
// log of its own beside the session's, <session>/subagents/agent-<id>.jsonl,
// with agent-<id>.meta.json saying what sort of agent it is and, for one
// run in the background, which call started it and whether it has
// finished. One in the foreground holds its call open until it is done,
// and is found by the prompt the call gave it.

// staleAgent is how long a subagent can go without writing before it is
// taken to have stopped, as when its session was killed under it.
const staleAgent = 15 * time.Minute

// startsAgent reports whether a call of tool starts a subagent.
func startsAgent(tool string) bool { return tool == "Agent" || tool == "Task" }

type agentMeta struct {
	AgentType    string `json:"agentType"`
	Description  string `json:"description"`
	ToolUseID    string `json:"toolUseId"`
	RequestShape string `json:"requestShape"`
	Finished     *bool  `json:"finished"`
}

// agentCall is what a call that starts an agent asks of it.
type agentCall struct {
	Prompt       string `json:"prompt"`
	Description  string `json:"description"`
	SubagentType string `json:"subagent_type"`
}

// agentScan is one subagent's log, read as far as it was written.
type agentScan struct {
	offset           int64
	prompt           string // what its first message asked
	calls            int32
	tool, detail     string // its latest call
	started, updated time.Time
}

// agentScans keeps the subagent logs read, by path, to read on from where
// each stopped.
type agentScans map[string]*agentScan

// read brings the scan up to the end of path's complete records.
func (s *agentScan) read(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	if stat, err := f.Stat(); err != nil || stat.Size() < s.offset {
		*s = agentScan{} // rewritten
	}
	if _, err := f.Seek(s.offset, io.SeekStart); err != nil {
		return
	}
	reader := bufio.NewReaderSize(f, 64<<10)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return // the writer may be in the middle of a record
		}
		s.offset += int64(len(line))
		var r struct {
			Type      string          `json:"type"`
			Timestamp time.Time       `json:"timestamp"`
			Message   json.RawMessage `json:"message"`
		}
		if json.Unmarshal(line, &r) != nil {
			continue
		}
		if !r.Timestamp.IsZero() {
			if s.started.IsZero() {
				s.started = r.Timestamp
			}
			s.updated = r.Timestamp
		}
		for _, m := range parseMessage(r.Message) {
			switch {
			case m.GetRole() == "user" && s.prompt == "":
				s.prompt = strings.TrimSpace(m.GetText())
			case m.GetRole() == "tool" && !m.GetOutput():
				s.calls++
				s.tool, s.detail = m.GetTool(), m.GetDetail()
			}
		}
	}
}

// read is the subagents the calls in messages started, from the
// conversation logged at path, oldest first; none for an agent that keeps
// no subagent logs.
func (scans *agentScans) read(a *Agent, path string, messages []*protocol.ChatMessage) []*protocol.SubAgent {
	if a.rollout {
		return nil
	}
	type started struct {
		msg *protocol.ChatMessage
		in  agentCall
	}
	calls := map[string]started{}
	byPrompt := map[string]string{}
	for _, m := range messages {
		if m.GetRole() != "tool" || m.GetOutput() || !startsAgent(m.GetTool()) || m.GetId() == "" {
			continue
		}
		var in agentCall
		_ = json.Unmarshal([]byte(m.GetText()), &in)
		calls[m.GetId()] = started{m, in}
		if p := strings.TrimSpace(in.Prompt); p != "" {
			byPrompt[p] = m.GetId()
		}
	}
	if len(calls) == 0 {
		return nil
	}
	if *scans == nil {
		*scans = agentScans{}
	}
	now := time.Now()
	metas, _ := filepath.Glob(filepath.Join(strings.TrimSuffix(path, filepath.Ext(path)), "subagents", "agent-*.meta.json"))
	var out []*protocol.SubAgent
	for _, metaPath := range metas {
		var meta agentMeta
		if data, err := os.ReadFile(metaPath); err != nil || json.Unmarshal(data, &meta) != nil {
			continue
		}
		if meta.ToolUseID != "" && calls[meta.ToolUseID].msg == nil {
			continue // started by a call out of the window
		}
		logPath := strings.TrimSuffix(metaPath, ".meta.json") + ".jsonl"
		scan := (*scans)[logPath]
		if scan == nil {
			scan = &agentScan{}
			(*scans)[logPath] = scan
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
		if running && now.Sub(scan.updated) > staleAgent {
			running = false
		}
		agent := &protocol.SubAgent{
			Id:     strings.TrimPrefix(filepath.Base(strings.TrimSuffix(logPath, ".jsonl")), "agent-"),
			CallId: callID, Running: running,
			Kind:        cmp.Or(meta.AgentType, call.in.SubagentType),
			Description: cmp.Or(meta.Description, call.in.Description),
			ToolCalls:   scan.calls, Tool: scan.tool, Detail: scan.detail,
		}
		if !scan.started.IsZero() {
			agent.Started, agent.Updated = scan.started.Unix(), scan.updated.Unix()
		}
		out = append(out, agent)
	}
	slices.SortStableFunc(out, func(a, b *protocol.SubAgent) int { return int(a.GetStarted() - b.GetStarted()) })
	return out
}
