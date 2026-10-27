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

	"github.com/ironpark/mako/internal/protocol"
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
func (s *agentScan) read(path string) {
	stat, err := os.Stat(path)
	if err != nil || stat.Size() == s.offset {
		return
	}
	if stat.Size() < s.offset {
		*s = agentScan{} // rewritten
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
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
