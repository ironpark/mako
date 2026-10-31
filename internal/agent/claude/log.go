package claude

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/protocol"
)

// Claude logs a conversation in its project's folder, a record a message,
// <root>/projects/<project>/<id>.jsonl.
var place = agent.Place{RootEnv: "CLAUDE_CONFIG_DIR", HomeDir: ".claude", Dirs: []string{"projects", "*"}}

func (*Agent) Place() agent.Place { return place }

func (*Agent) LogName(id string) string { return id + ".jsonl" }

type record struct {
	Type      string    `json:"type"`
	Cwd       string    `json:"cwd"`
	SessionID string    `json:"sessionId"`
	Sidechain bool      `json:"isSidechain"`
	Timestamp time.Time `json:"timestamp"`
	// Meta is a turn Claude Code adds for the model, such as the prompt a
	// slash command expands to; Summary is the summary a compaction leaves.
	Meta    bool            `json:"isMeta"`
	Summary bool            `json:"isCompactSummary"`
	UUID    string          `json:"uuid"`
	Message json.RawMessage `json:"message"`
}

// Identity is the first record of the session's own, not a subagent's.
func (*Agent) Identity(line []byte) (string, string, time.Time, bool) {
	var r record
	if json.Unmarshal(line, &r) != nil || r.Sidechain || r.Cwd == "" || r.SessionID == "" {
		return "", "", time.Time{}, false
	}
	return r.SessionID, r.Cwd, r.Timestamp, true
}

func (a *Agent) NewDecoder() agent.Decoder { return &decoder{tools: a} }

// NewAgentDecoder reads a subagent's log, its sidechain.
func (a *Agent) NewAgentDecoder() agent.Decoder {
	return &decoder{tools: a, sidechain: true}
}

// decoder reads Claude's records. A record may be written again, as a
// resumed conversation copies its history, so each is read once.
type decoder struct {
	tools   agent.Tools
	seen    agent.Seen
	command string // the last slash command typed, whose output follows it
	// sidechain reads a subagent's log, whose records are all sidechains,
	// in place of the session's, which leaves those out.
	sidechain bool
	// last is the latest message read, a summary aside: a command
	// written again after it is not shown twice.
	last *protocol.ChatMessage
}

func (d *decoder) Line(line []byte) []*protocol.ChatMessage {
	var r record
	if json.Unmarshal(line, &r) != nil || r.Sidechain != d.sidechain || r.Meta || r.Type != "user" && r.Type != "assistant" {
		return nil
	}
	if r.UUID != "" && !d.seen.Add(r.UUID) {
		return nil
	}
	parsed := agent.ParseMessage(r.Message, d.tools)
	if r.Summary {
		for i, m := range parsed {
			parsed[i] = agent.Summary(m.GetText())
		}
		return parsed
	}
	out := parsed[:0]
	for _, m := range unwrap(parsed, &d.command) {
		if d.retyped(m) {
			continue
		}
		d.last = m
		out = append(out, m)
	}
	return out
}

// retyped reports whether m is a slash command just read as typed: Claude
// Code writes /compact down as the user typed it, then again as a command
// after the summary, or after the error that ended it.
func (d *decoder) retyped(m *protocol.ChatMessage) bool {
	return m.GetRole() == "user" && strings.HasPrefix(m.GetText(), "/") &&
		d.last.GetRole() == "user" && d.last.GetText() == m.GetText()
}

func (*Agent) NewUsage() agent.Usage { return &usage{} }

// usage counts the tokens of Claude's replies. A reply written in several
// records repeats its usage in each, so each counts once.
type usage struct{ seen agent.Seen }

func (u *usage) Count(line []byte, total *protocol.Usage) {
	if !bytes.Contains(line, []byte(`"usage"`)) {
		return
	}
	var r struct {
		Type      string `json:"type"`
		Sidechain bool   `json:"isSidechain"`
		Message   struct {
			ID    string `json:"id"`
			Usage *struct {
				Input         int64 `json:"input_tokens"`
				CacheCreation int64 `json:"cache_creation_input_tokens"`
				CacheRead     int64 `json:"cache_read_input_tokens"`
				Output        int64 `json:"output_tokens"`
			} `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &r) != nil || r.Type != "assistant" || r.Message.Usage == nil {
		return
	}
	if r.Message.ID != "" && !u.seen.Add(r.Message.ID) {
		return
	}
	t := r.Message.Usage
	total.Input += t.Input + t.CacheCreation
	total.CachedInput += t.CacheRead
	total.Output += t.Output
	if !r.Sidechain {
		total.Context = t.Input + t.CacheCreation + t.CacheRead
	}
}
