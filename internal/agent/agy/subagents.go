package agy

import (
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/protocol"
)

// agy records every subagent and swarm worker spawned by a conversation under
// <root>/brain/<parentID>/.system_generated/subagents/<childID>.json, and keeps
// the child's full transcript at
// <root>/brain/<childID>/.system_generated/logs/transcript_full.jsonl.

type subagentMeta struct {
	ConversationID     string `json:"conversationId"`
	SubagentDescriptor struct {
		TypeName string `json:"typeName"`
		Role     string `json:"role"`
	} `json:"subagentDescriptor"`
	State          string `json:"state"`
	SpawnStepIndex int    `json:"spawnStepIndex"`
}

type subagentMetaRead struct {
	size int64
	mod  time.Time
	meta subagentMeta
	ok   bool
}

type membersRead struct {
	size   int64
	mod    time.Time
	killed map[string]bool
}

type childScan struct {
	agent.Tail
	dec                agent.Decoder
	calls              int32
	tool, kind, detail string
}

func (c *childScan) read(path string) {
	c.Tail.Read(path, func() { *c = childScan{dec: New().NewDecoder()} }, func(line []byte) {
		for _, m := range c.dec.Line(line) {
			if m.GetRole() == "tool" && !m.GetOutput() {
				c.calls++
				c.tool, c.kind, c.detail = m.GetTool(), m.GetKind(), m.GetDetail()
			}
		}
	})
}

type subagents struct {
	path       string
	brainDir   string
	subDir     string
	membersLoc string

	dirMod    time.Time
	metaPaths []string
	metas     map[string]*subagentMetaRead
	members   *membersRead
	scans     map[string]*childScan
}

// NewSubAgents follows the subagents and swarm workers of an agy conversation.
func (*Agent) NewSubAgents() agent.SubAgentScan {
	return &subagents{
		metas: map[string]*subagentMetaRead{},
		scans: map[string]*childScan{},
	}
}

func (s *subagents) Read(path string, messages []*protocol.ChatMessage) []*protocol.SubAgent {
	if path != s.path {
		s.path = path
		s.brainDir = brainDirFromLog(path)
		sysDir := filepath.Dir(filepath.Dir(filepath.Clean(path)))
		s.subDir = filepath.Join(sysDir, "subagents")
		s.membersLoc = filepath.Join(sysDir, "swarm", "members.json")
		s.dirMod = time.Time{}
		s.metaPaths = nil
	}
	if s.brainDir == "" {
		return nil
	}
	metaPaths := s.list()
	if len(metaPaths) == 0 {
		return nil
	}
	killed := s.killedMembers()
	stepCalls := callsByStep(messages)

	var out []*protocol.SubAgent
	for _, metaPath := range metaPaths {
		meta, mod, ok := s.readMeta(metaPath)
		if !ok {
			continue
		}
		id := cmp.Or(meta.ConversationID, strings.TrimSuffix(filepath.Base(metaPath), ".json"))
		if !agent.ValidID.MatchString(id) {
			continue
		}
		running := meta.State == "SUBAGENT_STATE_ALIVE" && !killed[id]
		callID := stepCalls[meta.SpawnStepIndex]
		if !running && callID == "" {
			continue
		}
		childLog := filepath.Join(s.brainDir, id, ".system_generated", "logs", logFile)
		scan := s.scans[childLog]
		if scan == nil {
			scan = &childScan{dec: New().NewDecoder()}
			s.scans[childLog] = scan
		}
		scan.read(childLog)

		desc := cmp.Or(meta.SubagentDescriptor.Role, meta.SubagentDescriptor.TypeName, id)
		a := &protocol.SubAgent{
			Id:          id,
			CallId:      callID,
			Running:     running,
			Kind:        meta.SubagentDescriptor.TypeName,
			Description: desc,
			ToolCalls:   scan.calls,
			Tool:        scan.tool,
			ToolKind:    scan.kind,
			Detail:      scan.detail,
			Log:         childLog,
		}
		if !scan.Started.IsZero() {
			a.Started, a.Updated = scan.Started.Unix(), scan.Updated.Unix()
		} else if !mod.IsZero() {
			a.Started, a.Updated = mod.Unix(), mod.Unix()
		}
		out = append(out, a)
	}
	agent.SortAgents(out)
	return out
}

func (s *subagents) list() []string {
	stat, err := os.Stat(s.subDir)
	if err != nil {
		s.dirMod, s.metaPaths = time.Time{}, nil
		return nil
	}
	if !stat.ModTime().Equal(s.dirMod) {
		s.metaPaths, _ = filepath.Glob(filepath.Join(s.subDir, "*.json"))
		s.dirMod = stat.ModTime()
	}
	return s.metaPaths
}

func (s *subagents) readMeta(path string) (subagentMeta, time.Time, bool) {
	stat, err := os.Stat(path)
	if err != nil {
		return subagentMeta{}, time.Time{}, false
	}
	r := s.metas[path]
	if r == nil || r.size != stat.Size() || !r.mod.Equal(stat.ModTime()) {
		r = &subagentMetaRead{size: stat.Size(), mod: stat.ModTime()}
		if body, err := os.ReadFile(path); err == nil && json.Unmarshal(body, &r.meta) == nil {
			r.ok = true
		}
		s.metas[path] = r
	}
	return r.meta, r.mod, r.ok
}

func (s *subagents) killedMembers() map[string]bool {
	stat, err := os.Stat(s.membersLoc)
	if err != nil {
		return nil
	}
	if s.members != nil && s.members.size == stat.Size() && s.members.mod.Equal(stat.ModTime()) {
		return s.members.killed
	}
	mr := &membersRead{size: stat.Size(), mod: stat.ModTime(), killed: map[string]bool{}}
	if body, err := os.ReadFile(s.membersLoc); err == nil {
		var doc struct {
			Members map[string]struct {
				ConversationID string `json:"conversation_id"`
				Killed         bool   `json:"killed"`
				Phase          string `json:"phase"`
			} `json:"members"`
		}
		if json.Unmarshal(body, &doc) == nil {
			for _, m := range doc.Members {
				if m.ConversationID != "" && (m.Killed || strings.EqualFold(m.Phase, "KILLED")) {
					mr.killed[m.ConversationID] = true
				}
			}
		}
	}
	s.members = mr
	return mr.killed
}

// callsByStep maps agy step_index to a spawning tool call's ID ("<step>.<i>").
func callsByStep(messages []*protocol.ChatMessage) map[int]string {
	out := map[int]string{}
	for _, m := range messages {
		if m.GetRole() != "tool" || m.GetOutput() || m.GetId() == "" {
			continue
		}
		stepStr, _, ok := strings.Cut(m.GetId(), ".")
		if !ok {
			continue
		}
		var step int
		if _, err := fmt.Sscanf(stepStr, "%d", &step); err != nil || step <= 0 {
			continue
		}
		// Prefer explicit subagent/swarm spawn calls when a step has multiple tools.
		if _, exists := out[step]; !exists || m.GetTool() == "invoke_subagent" || m.GetTool() == "swarm" {
			out[step] = m.GetId()
		}
	}
	return out
}
