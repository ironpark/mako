package agy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/ironpark/mako/internal/agent"
)

// agy takes hooks from no command line, only from the hooks.json files of
// its customization roots: mako keeps an entry of its own in the global
// one, which runs its hook in every agy, and the hook passes on only what
// a session of mako's runs. agy's input does not name its event, so each
// event's command names it after the hook's own arguments.

// hookEvents are the events mako hooks. PreInvocation comes before each
// call of the model, so it says the agent works; agy says nothing when it
// waits on the user, nor hooks its own approvals.
var hookEvents = []agent.HookEvent{
	{Name: "SessionStart", Timeout: agent.HookTimeout}, {Name: "PreInvocation", Timeout: agent.HookTimeout},
	{Name: "PostToolUse", Timeout: agent.HookTimeout}, {Name: "Stop", Timeout: agent.HookTimeout},
}

// hooksFile is agy's global hooks.json.
func hooksFile() (string, error) {
	home, err := os.UserHomeDir()
	return filepath.Join(home, ".gemini", "config", "hooks.json"), err
}

// WithHooks keeps mako's entry, for hook, in agy's global hooks.json, and
// leaves the command as it is. Each hook command has an entry of its own,
// so the daemons of several nodes on one machine each hear their own
// sessions. The status line is Claude's alone.
func (*Agent) WithHooks(command []string, hook, _ string) ([]string, bool) {
	path, err := hooksFile()
	if err != nil {
		return command, false
	}
	return command, writeHooks(path, hook) == nil
}

// writeHooks writes mako's entry for hook into the hooks.json at path,
// keeping the rest, unless it is there already.
func writeHooks(path, hook string) error {
	doc := map[string]json.RawMessage{}
	body, err := os.ReadFile(path)
	switch {
	case err == nil:
		// agy reads a file saved with a byte-order mark too.
		if err := json.Unmarshal(bytes.TrimPrefix(body, []byte("\xef\xbb\xbf")), &doc); err != nil {
			return err // the user's, which mako will not overwrite
		}
	case !os.IsNotExist(err):
		return err
	}
	entry, err := json.Marshal(hookEntry(hook))
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(hook))
	name := "mako-" + hex.EncodeToString(sum[:4])
	// As written, the entry is indented.
	var was bytes.Buffer
	if json.Compact(&was, doc[name]) == nil && bytes.Equal(was.Bytes(), entry) {
		return nil
	}
	doc[name] = entry
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".hooks-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(out, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// hookEntry is mako's entry in hooks.json: hook on each event, the event
// named after it.
func hookEntry(hook string) map[string]any {
	type handler struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	}
	type group struct {
		Matcher string    `json:"matcher"`
		Hooks   []handler `json:"hooks"`
	}
	entry := map[string]any{}
	for _, e := range hookEvents {
		h := handler{Type: "command", Command: hook + " " + e.Name, Timeout: e.Timeout}
		// A tool's event groups its handlers under a matcher.
		if e.Name == "PostToolUse" {
			entry[e.Name] = []group{{Matcher: "*", Hooks: []handler{h}}}
		} else {
			entry[e.Name] = []handler{h}
		}
	}
	return entry
}

// hookInput is what agy hands a hook, with the event the hook's command
// names, which the hook adds.
type hookInput struct {
	Event          string `json:"hook_event_name"`
	ConversationID string `json:"conversationId"`
	TranscriptPath string `json:"transcriptPath"`
	ToolCall       struct {
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	} `json:"toolCall"`
}

func (*Agent) Event(input []byte) (agent.Event, bool) {
	var in hookInput
	if json.Unmarshal(input, &in) != nil {
		return agent.Event{}, false
	}
	ev := agent.Event{SessionID: in.ConversationID, Transcript: in.TranscriptPath, Tool: in.ToolCall.Name, Input: in.ToolCall.Args}
	switch in.Event {
	case "SessionStart":
		ev.Kind = agent.Started
	case "PreInvocation":
		ev.Kind = agent.Prompted
	case "PostToolUse":
		ev.Kind = agent.ToolDone
	case "Stop":
		ev.Kind = agent.Stopped
	default:
		return agent.Event{}, false
	}
	return ev, true
}

// Reply: mako does not hook agy's approvals, so it never answers one.
func (*Agent) Reply(string, string) []byte { return []byte("{}\n") }
