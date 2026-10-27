package agy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/protocol"
)

// agy keeps each conversation in a folder named by its ID,
// <root>/brain/<id>/.system_generated/logs/transcript_full.jsonl: a record
// a step, which says nothing of whose it is. The working directory is in
// the history of the prompts typed, <root>/history.jsonl.
var place = agent.Place{HomeDir: filepath.Join(".gemini", "antigravity-cli")}

// logFile is a conversation's transcript, in full: transcript.jsonl beside
// it cuts long fields short.
const logFile = "transcript_full.jsonl"

func (*Agent) Place() agent.Place { return place }

// LogName: the folder, not the file, is named by the ID; see LogPath.
func (*Agent) LogName(string) string { return "" }

// LogPath is conversation id's transcript under root.
func (*Agent) LogPath(root, id string) string {
	return filepath.Join(root, "brain", id, ".system_generated", "logs", logFile)
}

// Identity: the lines do not say; see Whose.
func (*Agent) Identity([]byte) (string, string, time.Time, bool) {
	return "", "", time.Time{}, true
}

// Whose is the conversation of the transcript at path, by its folder, the
// workspace its first prompt was typed in, and its first step's time.
func (a *Agent) Whose(path string) (id, cwd string, created time.Time) {
	logs := filepath.Dir(path)
	folder := filepath.Dir(filepath.Dir(logs))
	id = filepath.Base(folder)
	root := filepath.Dir(filepath.Dir(folder))
	if !agent.ValidID.MatchString(id) || a.LogPath(root, id) != filepath.Clean(path) {
		return "", "", time.Time{}
	}
	if f, err := os.Open(path); err == nil {
		var first struct {
			Created time.Time `json:"created_at"`
		}
		_ = json.NewDecoder(io.LimitReader(f, 1<<20)).Decode(&first)
		f.Close()
		created = first.Created
	}
	return id, workspace(filepath.Join(root, "history.jsonl"), id), created
}

// workspace is where the history at path says conversation id's prompts
// were typed, "" when it has none of them.
func workspace(path, id string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	want := []byte(`"` + id + `"`)
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 64<<10), 4<<20)
	for s.Scan() {
		if !bytes.Contains(s.Bytes(), want) {
			continue
		}
		var h struct {
			Workspace      string `json:"workspace"`
			ConversationID string `json:"conversationId"`
		}
		if json.Unmarshal(s.Bytes(), &h) == nil && h.ConversationID == id && h.Workspace != "" {
			return h.Workspace
		}
	}
	return ""
}

// step is a transcript's record.
type step struct {
	Index     int    `json:"step_index"`
	Source    string `json:"source"`
	Type      string `json:"type"`
	Status    string `json:"status"`
	Content   string `json:"content"`
	ToolCalls []struct {
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	} `json:"tool_calls"`
	Input  int64 `json:"input_tokens"`
	Cached int64 `json:"cache_read_tokens"`
	Output int64 `json:"output_tokens"`
}

func (a *Agent) NewDecoder() agent.Decoder { return &decoder{tools: a} }

// decoder reads the steps. A tool's result is a step of its own, after the
// model's step that called it, in the order of the calls.
type decoder struct {
	tools   agent.Tools
	pending []string // the IDs of the calls yet to give results, oldest first
}

func (d *decoder) Line(line []byte) []*protocol.ChatMessage {
	var s step
	if json.Unmarshal(line, &s) != nil {
		return nil
	}
	switch s.Type {
	case "USER_INPUT":
		d.pending = nil
		if text := userText(s.Content); text != "" {
			return []*protocol.ChatMessage{{Role: "user", Text: text}}
		}
	case "PLANNER_RESPONSE":
		var out []*protocol.ChatMessage
		if strings.TrimSpace(s.Content) != "" {
			out = append(out, &protocol.ChatMessage{Role: "assistant", Text: s.Content})
		}
		d.pending = d.pending[:0]
		for i, c := range s.ToolCalls {
			id := fmt.Sprintf("%d.%d", s.Index, i)
			d.pending = append(d.pending, id)
			out = append(out, agent.Call(d.tools, id, c.Name, c.Args))
		}
		return out
	case "GENERIC":
		if len(d.pending) == 0 {
			return nil
		}
		id := d.pending[0]
		d.pending = d.pending[1:]
		return []*protocol.ChatMessage{{Role: "tool", Id: id, Text: s.Content, Output: true, Error: s.Status == "ERROR"}}
	case "CHECKPOINT":
		return []*protocol.ChatMessage{agent.Summary(s.Content)}
	}
	return nil
}

// request is the user's own words in a prompt agy wraps.
var request = regexp.MustCompile(`(?s)<USER_REQUEST>\n?(.*?)\n?</USER_REQUEST>`)

// userText is what the user typed of a prompt: its request, without the
// metadata agy adds for the model.
func userText(content string) string {
	if m := request.FindStringSubmatch(content); m != nil {
		return strings.TrimSpace(m[1])
	}
	return strings.TrimSpace(agent.DropTags(content, "ADDITIONAL_METADATA", "USER_SETTINGS_CHANGE"))
}

func (*Agent) NewUsage() agent.Usage { return usage{} }

// usage counts the tokens of each call of the model.
type usage struct{}

func (usage) Count(line []byte, total *protocol.Usage) {
	if !bytes.Contains(line, []byte(`"input_tokens"`)) {
		return
	}
	// The step's tokens, without its content.
	var s struct {
		Type   string `json:"type"`
		Input  int64  `json:"input_tokens"`
		Cached int64  `json:"cache_read_tokens"`
		Output int64  `json:"output_tokens"`
	}
	if json.Unmarshal(line, &s) != nil || s.Type != "PLANNER_RESPONSE" {
		return
	}
	total.Input += s.Input
	total.CachedInput += s.Cached
	total.Output += s.Output
	total.Context = s.Input + s.Cached
}
