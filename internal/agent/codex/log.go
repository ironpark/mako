package codex

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/protocol"
)

// Codex logs a conversation as a rollout by its day,
// <root>/sessions/<yyyy>/<mm>/<dd>/rollout-<time>-<id>.jsonl: a
// session_meta record, then a response_item record an item.
var place = agent.Place{RootEnv: "CODEX_HOME", HomeDir: ".codex", Dirs: []string{"sessions", "*", "*", "*"}}

func (*Agent) Place() agent.Place { return place }

// LogName: a rollout's name holds its time as well as its ID.
func (*Agent) LogName(string) string { return "" }

type record struct {
	Type      string          `json:"type"`
	Timestamp time.Time       `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
}

// Identity is the session_meta record's. A subagent's rollout is not the
// interactive session's conversation.
func (*Agent) Identity(line []byte) (string, string, time.Time, bool) {
	var r record
	if json.Unmarshal(line, &r) != nil || r.Type != "session_meta" {
		return "", "", time.Time{}, false
	}
	var meta struct {
		ID        string          `json:"id"`
		SessionID string          `json:"session_id"`
		Cwd       string          `json:"cwd"`
		Source    json.RawMessage `json:"source"`
		Timestamp time.Time       `json:"timestamp"`
	}
	if json.Unmarshal(r.Payload, &meta) != nil || strings.Contains(string(meta.Source), "subagent") {
		return "", "", time.Time{}, true
	}
	if meta.ID == "" {
		meta.ID = meta.SessionID
	}
	if meta.Timestamp.IsZero() {
		meta.Timestamp = r.Timestamp
	}
	return meta.ID, meta.Cwd, meta.Timestamp, true
}

func (*Agent) NewDecoder() agent.Decoder { return decoder{} }

// decoder reads the response items; the events beside them repeat them.
type decoder struct{}

// hidden are the tags Codex wraps what it tells the model in, within a
// user's turn.
var hidden = []string{"environment_context", "user_instructions"}

func (decoder) Line(line []byte) []*protocol.ChatMessage {
	var r record
	if json.Unmarshal(line, &r) != nil || r.Type != "response_item" {
		return nil
	}
	parsed := agent.ParseMessage(r.Payload, toolKind)
	out := parsed[:0]
	for _, m := range parsed {
		if m.GetRole() == "user" {
			if m.Text = strings.TrimSpace(agent.DropTags(m.GetText(), hidden...)); m.Text == "" {
				continue
			}
		}
		out = append(out, m)
	}
	return out
}

func (*Agent) NewUsage() agent.Usage { return usage{} }

// usage reads Codex's token counts, which are running totals.
type usage struct{}

func (usage) Count(line []byte, total *protocol.Usage) {
	if !bytes.Contains(line, []byte(`"token_count"`)) {
		return
	}
	type tokens struct {
		Input  int64 `json:"input_tokens"` // the cached included
		Cached int64 `json:"cached_input_tokens"`
		Output int64 `json:"output_tokens"`
	}
	type limit struct {
		Used    float64 `json:"used_percent"`
		Minutes int64   `json:"window_minutes"`
		Resets  int64   `json:"resets_at"`
	}
	var r struct {
		Payload struct {
			Type string `json:"type"`
			Info *struct {
				Total  tokens `json:"total_token_usage"`
				Last   tokens `json:"last_token_usage"`
				Window int64  `json:"model_context_window"`
			} `json:"info"`
			Limits *struct {
				Primary   *limit `json:"primary"`
				Secondary *limit `json:"secondary"`
			} `json:"rate_limits"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &r) != nil || r.Payload.Type != "token_count" {
		return
	}
	if info := r.Payload.Info; info != nil {
		t := info.Total
		total.Input, total.CachedInput, total.Output = t.Input-t.Cached, t.Cached, t.Output
		total.Context, total.Window = info.Last.Input, info.Window
	}
	if l := r.Payload.Limits; l != nil {
		for _, w := range []*limit{l.Primary, l.Secondary} {
			if w != nil && w.Minutes != 0 {
				total.SetLimit(&protocol.RateLimit{Minutes: w.Minutes, Used: w.Used, Resets: w.Resets})
			}
		}
	}
}
