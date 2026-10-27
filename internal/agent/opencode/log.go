package opencode

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"time"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/protocol"
)

// The logs are the plugin's (plugin.go), in OpenCode's data folder:
// $XDG_DATA_HOME, else ~/.local/share, on every OS.
var place = agent.Place{RootEnv: "XDG_DATA_HOME", HomeDir: filepath.Join(".local", "share"), Dirs: []string{"opencode", "mako"}}

func (*Agent) Place() agent.Place { return place }

func (*Agent) LogName(id string) string { return id + ".jsonl" }

// record is a log's line, as the plugin writes it.
type record struct {
	Type   string          `json:"type"`
	ID     string          `json:"id"`
	Cwd    string          `json:"cwd"`
	Time   int64           `json:"time"` // Unix milliseconds
	Text   string          `json:"text"`
	Tool   string          `json:"tool"`
	Input  json.RawMessage `json:"input"`
	Output string          `json:"output"`
	Error  bool            `json:"error"`
	Exit   *int32          `json:"exit"`
}

// Identity is the header's, the log's first line.
func (*Agent) Identity(line []byte) (string, string, time.Time, bool) {
	var r record
	if json.Unmarshal(line, &r) != nil || r.Type != "session" {
		return "", "", time.Time{}, true
	}
	return r.ID, r.Cwd, time.UnixMilli(r.Time), true
}

func (a *Agent) NewDecoder() agent.Decoder { return decoder{tools: a} }

type decoder struct{ tools agent.Tools }

func (d decoder) Line(line []byte) []*protocol.ChatMessage {
	var r record
	if json.Unmarshal(line, &r) != nil {
		return nil
	}
	switch r.Type {
	case "user":
		return []*protocol.ChatMessage{{Role: "user", Text: r.Text}}
	case "text":
		return []*protocol.ChatMessage{{Role: "assistant", Text: r.Text}}
	case "summary":
		return []*protocol.ChatMessage{agent.Summary(r.Text)}
	case "tool":
		// The plugin writes a call once it is done: the call and its
		// result at once.
		call := agent.Call(d.tools, r.ID, r.Tool, r.Input)
		call.Result, call.Error, call.ExitCode = r.Output, r.Error, r.Exit
		return []*protocol.ChatMessage{call}
	}
	return nil
}

func (*Agent) NewUsage() agent.Usage { return usage{} }

// usage counts the tokens of each assistant message.
type usage struct{}

func (usage) Count(line []byte, total *protocol.Usage) {
	if !bytes.HasPrefix(line, []byte(`{"type":"usage"`)) {
		return
	}
	var r struct {
		Type  string       `json:"type"`
		Usage agent.Tokens `json:"usage"`
	}
	if json.Unmarshal(line, &r) == nil && r.Type == "usage" {
		r.Usage.Add(total, true)
	}
}
