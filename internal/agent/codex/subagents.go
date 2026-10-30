package codex

import (
	"cmp"
	"encoding/json"
	"path/filepath"
	"time"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/protocol"
)

// An agent spawn_agent starts is a thread of its own, logged as a rollout
// in the same tree as the session's, named for its thread ID, which the
// call answers with: {"agent_id": …, "nickname": …}.

func (*Agent) NewSubAgents() agent.SubAgentScan {
	return &subagents{logs: map[string]string{}, searched: map[string]bool{}, scans: map[string]*childScan{}}
}

// subagents follows a conversation's subagents: each one's log, once
// found, read on from where the last read stopped.
type subagents struct {
	logs     map[string]string // thread ID to its log
	searched map[string]bool   // the thread IDs looked for in every day
	scans    map[string]*childScan
}

// Read is the subagents the spawn_agent calls in messages started, from
// the conversation logged at path, oldest first.
func (s *subagents) Read(path string, messages []*protocol.ChatMessage) []*protocol.SubAgent {
	// path is <sessions>/YYYY/MM/DD/rollout-….jsonl.
	sessions := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(path))))
	now := time.Now()
	var out []*protocol.SubAgent
	for _, m := range messages {
		if m.GetRole() != "tool" || m.GetOutput() || m.GetKind() != protocol.ToolTask || m.GetResult() == "" {
			continue
		}
		var started struct {
			ID       string `json:"agent_id"`
			Nickname string `json:"nickname"`
		}
		if json.Unmarshal([]byte(m.GetResult()), &started) != nil || !agent.ValidID.MatchString(started.ID) {
			continue
		}
		log := s.find(sessions, filepath.Dir(path), started.ID)
		if log == "" {
			continue
		}
		scan := s.scans[log]
		if scan == nil {
			scan = &childScan{dec: New().NewDecoder()}
			s.scans[log] = scan
		}
		scan.read(log)
		var in agent.Task
		_ = json.Unmarshal([]byte(m.GetText()), &in)
		a := &protocol.SubAgent{
			Id: started.ID, CallId: m.GetId(),
			Running:     scan.running && now.Sub(scan.Updated) < agent.StaleAgent,
			Kind:        cmp.Or(scan.role, in.Agent),
			Description: cmp.Or(started.Nickname, in.Description, agent.OneLine(in.Prompt, 80)),
			ToolCalls:   scan.calls, Tool: scan.tool, ToolKind: scan.kind, Detail: scan.detail,
			Log: log,
		}
		if !scan.Started.IsZero() {
			a.Started, a.Updated = scan.Started.Unix(), scan.Updated.Unix()
		}
		out = append(out, a)
	}
	agent.SortAgents(out)
	return out
}

// find is the log of thread id under sessions, "" until it is written. A
// subagent starts beside its session, so its log is looked for in the
// session's day and today's, and once in every day.
func (s *subagents) find(sessions, day, id string) string {
	if log, ok := s.logs[id]; ok {
		return log
	}
	name := "rollout-*" + id + "*.jsonl"
	dirs := []string{day, filepath.Join(sessions, time.Now().Format("2006/01/02"))}
	if !s.searched[id] {
		s.searched[id] = true
		dirs = append(dirs, filepath.Join(sessions, "*", "*", "*"))
	}
	for _, dir := range dirs {
		if matches, _ := filepath.Glob(filepath.Join(dir, name)); len(matches) > 0 {
			s.logs[id] = matches[0]
			return matches[0]
		}
	}
	return ""
}

// childScan is one subagent's log, read as far as it was written.
type childScan struct {
	agent.Tail
	dec                agent.Decoder
	role               string // the sort of agent, as its session says
	running            bool   // between a turn's start and its end
	calls              int32
	tool, kind, detail string // its latest call
}

// read brings the scan up to the end of path's complete records.
func (c *childScan) read(path string) {
	c.Tail.Read(path, func() { *c = childScan{dec: New().NewDecoder()} }, func(line []byte) {
		var r struct {
			Type    string `json:"type"`
			Payload struct {
				Type string `json:"type"`
				Role string `json:"agent_role"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &r) != nil {
			return
		}
		if r.Type == "session_meta" {
			c.role = r.Payload.Role
		} else if status := turnStatus(r.Type, r.Payload.Type); status != "" {
			c.running = protocol.AtWork(status)
		}
		for _, m := range c.dec.Line(line) {
			if m.GetRole() == "tool" && !m.GetOutput() {
				c.calls++
				c.tool, c.kind, c.detail = m.GetTool(), m.GetKind(), m.GetDetail()
			}
		}
	})
}
