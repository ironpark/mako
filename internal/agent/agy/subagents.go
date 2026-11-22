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
	size     int64
	mod      time.Time
	inactive map[string]bool
}

var subagentClock = time.Now

type childScan struct {
	agent.Tail
	dec                agent.Decoder
	seenSteps          bool
	working            bool
	lastTool           string
	started, updated   time.Time
	fileMod            time.Time
	calls              int32
	tool, kind, detail string
}

func (c *childScan) read(path string) {
	if stat, err := os.Stat(path); err == nil {
		c.fileMod = stat.ModTime()
	} else {
		c.fileMod = time.Time{}
	}
	c.Tail.Read(path, func() { *c = childScan{dec: New().NewDecoder(), fileMod: c.fileMod} }, func(line []byte) {
		var st struct {
			Type      string    `json:"type"`
			Status    string    `json:"status"`
			CreatedAt time.Time `json:"created_at"`
			ToolCalls []struct {
				Name string `json:"name"`
			} `json:"tool_calls"`
		}
		if json.Unmarshal(line, &st) == nil && st.Type != "" {
			c.seenSteps = true
			if !st.CreatedAt.IsZero() {
				if c.started.IsZero() {
					c.started = st.CreatedAt
				}
				c.updated = st.CreatedAt
			}
			switch st.Type {
			case "USER_INPUT", "SYSTEM_MESSAGE":
				c.working = true
				c.lastTool = ""
			case "PLANNER_RESPONSE":
				if len(st.ToolCalls) > 0 {
					last := st.ToolCalls[len(st.ToolCalls)-1].Name
					c.lastTool = last
					// swarm_wait parks the worker on the board until woken.
					c.working = last != "swarm_wait"
				} else {
					c.lastTool = ""
					c.working = strings.EqualFold(st.Status, "RUNNING")
				}
			case "GENERIC":
				switch {
				case strings.EqualFold(st.Status, "RUNNING"):
					c.working = true
				case c.lastTool == "swarm_wait":
					c.working = false
				default:
					c.working = true
				}
			case "ERROR_MESSAGE", "CANCELLED":
				c.working = false
				c.lastTool = ""
			}
		}
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
	inactive := s.inactiveMembers()
	stepCalls := callsByStep(messages)
	now := subagentClock()

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
		alive := meta.State == "SUBAGENT_STATE_ALIVE" && !inactive[id]
		callID := stepCalls[meta.SpawnStepIndex]
		if !alive && callID == "" {
			continue
		}
		childLog := filepath.Join(s.brainDir, id, ".system_generated", "logs", logFile)
		scan := s.scans[childLog]
		if scan == nil {
			scan = &childScan{dec: New().NewDecoder()}
			s.scans[childLog] = scan
		}
		scan.read(childLog)

		// agy leaves subagents in SUBAGENT_STATE_ALIVE after they finish so
		// the parent can still send follow-up messages. Derive whether the
		// subagent is actively working from its transcript turn state and
		// recency so completed or parked subagents do not pile up as running.
		running := alive
		if running && scan.seenSteps {
			running = scan.working
		}
		if running {
			lastActive := scan.fileMod
			if scan.updated.After(lastActive) {
				lastActive = scan.updated
			}
			if lastActive.IsZero() {
				lastActive = mod
			}
			if !lastActive.IsZero() && now.Sub(lastActive) > agent.StaleAgent {
				running = false
			}
		}
		if !running && callID == "" {
			continue
		}

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
		started, updated := scan.started, scan.updated
		if started.IsZero() {
			started, updated = scan.Started, scan.Updated
		}
		if !started.IsZero() {
			a.Started, a.Updated = started.Unix(), updated.Unix()
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

func (s *subagents) inactiveMembers() map[string]bool {
	stat, err := os.Stat(s.membersLoc)
	if err != nil {
		return nil
	}
	if s.members != nil && s.members.size == stat.Size() && s.members.mod.Equal(stat.ModTime()) {
		return s.members.inactive
	}
	mr := &membersRead{size: stat.Size(), mod: stat.ModTime(), inactive: map[string]bool{}}
	if body, err := os.ReadFile(s.membersLoc); err == nil {
		var doc struct {
			Members map[string]struct {
				ConversationID string `json:"conversation_id"`
				Killed         bool   `json:"killed"`
				Phase          string `json:"phase"`
				DesiredState   string `json:"desired_state"`
				WakeMode       string `json:"wake_mode"`
			} `json:"members"`
		}
		if json.Unmarshal(body, &doc) == nil {
			for _, m := range doc.Members {
				if m.ConversationID == "" {
					continue
				}
				parked := m.WakeMode != "" && !strings.EqualFold(m.WakeMode, "working")
				stopped := m.Killed ||
					strings.EqualFold(m.Phase, "KILLED") ||
					strings.EqualFold(m.Phase, "STOPPED") ||
					strings.EqualFold(m.DesiredState, "KILLED")
				if stopped || parked {
					mr.inactive[m.ConversationID] = true
				}
			}
		}
	}
	s.members = mr
	return mr.inactive
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
