package agy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/pkg/fsutil"
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

// hooksFile is agy's global hooks.json, honoring HOME in env when set.
func hooksFile(env map[string]string) (string, error) {
	if home := env["HOME"]; home != "" {
		return filepath.Join(home, ".gemini", "config", "hooks.json"), nil
	}
	home, err := os.UserHomeDir()
	return filepath.Join(home, ".gemini", "config", "hooks.json"), err
}

// WithHooks keeps mako's entry, for hook, in agy's global hooks.json, and
// attaches mako's statusLine via --settings when status is given, merging into
// any existing --settings JSON/file (unless it already sets statusLine) so agy
// reports its context window, state, and remaining quota without modifying
// settings.json on disk.
func (*Agent) WithHooks(l *agent.Launch, hook, status, _ string) bool {
	var env map[string]string
	if l != nil {
		env = l.Env
	}
	path, err := hooksFile(env)
	if err != nil || writeHooks(path, hook) != nil {
		return false
	}
	if l != nil && status != "" {
		statusCfg := map[string]any{
			"type":               "command",
			"command":            status,
			"enabled":            true,
			"stack_with_default": true,
		}
		l.Command = attachSettingsStatusLine(l.Command, statusCfg, env)
	}
	return true
}

func attachSettingsStatusLine(command []string, statusCfg map[string]any, env map[string]string) []string {
	if len(command) == 0 {
		return command
	}
	for i := 1; i < len(command); i++ {
		arg := command[i]
		var raw string
		inlineEq := false
		switch {
		case arg == "--settings" && i+1 < len(command):
			raw = command[i+1]
		case strings.HasPrefix(arg, "--settings="):
			raw = strings.TrimPrefix(arg, "--settings=")
			inlineEq = true
		default:
			continue
		}
		doc, ok := loadSettingsObject(raw, env)
		if !ok {
			return command // leave unrecognized/unreadable --settings untouched
		}
		if _, has := doc["statusLine"]; has {
			return command // respect explicit user statusLine in --settings
		}
		doc["statusLine"] = statusCfg
		merged, err := json.Marshal(doc)
		if err != nil {
			return command
		}
		out := slices.Clone(command)
		if inlineEq {
			out[i] = "--settings=" + string(merged)
		} else {
			out[i+1] = string(merged)
		}
		return out
	}
	doc := map[string]any{"statusLine": statusCfg}
	b, err := json.Marshal(doc)
	if err != nil {
		return command
	}
	return append(command, "--settings", string(b))
}

func loadSettingsObject(raw string, env map[string]string) (map[string]any, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, false
	}
	var body []byte
	if strings.HasPrefix(trimmed, "{") {
		body = []byte(trimmed)
	} else {
		p := trimmed
		if strings.HasPrefix(p, "~/") {
			home := env["HOME"]
			if home == "" {
				home, _ = os.UserHomeDir()
			}
			if home != "" {
				p = filepath.Join(home, p[2:])
			}
		}
		var err error
		body, err = os.ReadFile(p)
		if err != nil {
			return nil, false
		}
	}
	var doc map[string]any
	if err := json.Unmarshal(bytes.TrimPrefix(body, []byte("\xef\xbb\xbf")), &doc); err != nil || doc == nil {
		return nil, false
	}
	return doc, true
}

var hooksMu sync.Mutex

// writeHooks writes mako's entry for hook into the hooks.json at path,
// keeping the rest, unless it is there already.
func writeHooks(path, hook string) error {
	hooksMu.Lock()
	defer hooksMu.Unlock()

	doc := map[string]json.RawMessage{}
	body, err := os.ReadFile(path)
	switch {
	case err == nil:
		// agy reads a file saved with a byte-order mark too.
		trimmed := bytes.TrimSpace(bytes.TrimPrefix(body, []byte("\xef\xbb\xbf")))
		if len(trimmed) > 0 {
			if err := json.Unmarshal(trimmed, &doc); err != nil {
				return err // the user's, which mako will not overwrite
			}
			if doc == nil {
				doc = map[string]json.RawMessage{}
			}
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
	pruned := false
	for k := range doc {
		if k != name && strings.HasPrefix(k, "mako-") {
			delete(doc, k)
			pruned = true
		}
	}
	// As written, the entry is indented.
	var was bytes.Buffer
	if !pruned && len(body) > 0 && json.Compact(&was, doc[name]) == nil && bytes.Equal(was.Bytes(), entry) {
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
	return fsutil.Write(path, append(out, '\n'))
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
	Event                string `json:"hook_event_name"`
	CamelEvent           string `json:"hookEventName"`
	ConversationID       string `json:"conversationId"`
	TranscriptPath       string `json:"transcriptPath"`
	AgentID              string `json:"agent_id"`
	CamelAgentID         string `json:"agentId"`
	SubagentID           string `json:"subagentId"`
	ParentConversationID string `json:"parentConversationId"`
	SnakeParentID        string `json:"parent_conversation_id"`
	IsSubagent           bool   `json:"isSubagent"`
	ToolCall             struct {
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	} `json:"toolCall"`
}

func (*Agent) Event(input []byte) (agent.Event, bool) {
	var in hookInput
	if json.Unmarshal(input, &in) != nil {
		return agent.Event{}, false
	}
	eventName := in.Event
	if eventName == "" {
		eventName = in.CamelEvent
	}
	// If agy marks the hook payload as belonging to a subagent, only surface
	// explicit subagent lifecycle events and never let it overwrite parent state.
	agentID := in.AgentID
	if agentID == "" {
		agentID = in.CamelAgentID
	}
	if agentID == "" {
		agentID = in.SubagentID
	}
	parentID := in.ParentConversationID
	if parentID == "" {
		parentID = in.SnakeParentID
	}
	if in.IsSubagent || agentID != "" || (parentID != "" && parentID != in.ConversationID) {
		if agentID == "" {
			agentID = in.ConversationID
		}
		if parentID == "" {
			parentID = in.ConversationID
		}
		switch eventName {
		case "SubagentStart":
			return agent.Event{Kind: agent.ChildStarted, SessionID: parentID, AgentID: agentID}, true
		case "SubagentStop":
			return agent.Event{Kind: agent.ChildStopped, SessionID: parentID, AgentID: agentID}, true
		}
		return agent.Event{}, false
	}
	ev := agent.Event{SessionID: in.ConversationID, Transcript: in.TranscriptPath, Tool: in.ToolCall.Name, Input: in.ToolCall.Args}
	switch eventName {
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

// IsSubAgent reports whether childID is a subagent or swarm worker spawned by
// parentID so that its lifecycle hooks never hijack the parent session's bound
// conversation or working/idle state.
func (*Agent) IsSubAgent(parentID, parentLog, childID, childLog string) bool {
	if parentID == "" || childID == "" || childID == parentID {
		return false
	}
	if !agent.ValidID.MatchString(parentID) || !agent.ValidID.MatchString(childID) {
		return false
	}
	brainDir := brainDirFromLog(parentLog)
	if brainDir == "" {
		brainDir = brainDirFromLog(childLog)
	}
	if brainDir != "" {
		// 1. Primary signal: agy writes .system_generated/subagents/<childID>.json
		//    under the parent conversation's brain directory for every subagent
		//    and swarm worker (including workers inheriting the parent workspace).
		subMeta := filepath.Join(brainDir, parentID, ".system_generated", "subagents", childID+".json")
		if _, err := os.Stat(subMeta); err == nil {
			return true
		}
		// 2. Secondary signal: swarm/members.json lists every swarm member's
		//    conversation_id under the parent conversation.
		membersPath := filepath.Join(brainDir, parentID, ".system_generated", "swarm", "members.json")
		if body, err := os.ReadFile(membersPath); err == nil && bytes.Contains(body, []byte(`"`+childID+`"`)) {
			return true
		}
	}
	// 3. Fallback: a worker/subagent transcript starts with a SYSTEM_MESSAGE
	//    step delivered by sender=<parentID> rather than a USER_INPUT step.
	if childLog != "" {
		if firstLineHasParentSender(childLog, parentID) {
			return true
		}
	}
	return false
}

// brainDirFromLog extracts <root>/brain from an agy transcript path of the form
// <root>/brain/<id>/.system_generated/logs/<file>.
func brainDirFromLog(logPath string) string {
	if logPath == "" {
		return ""
	}
	logsDir := filepath.Dir(filepath.Clean(logPath))
	if filepath.Base(logsDir) != "logs" {
		return ""
	}
	sysDir := filepath.Dir(logsDir)
	if filepath.Base(sysDir) != ".system_generated" {
		return ""
	}
	convDir := filepath.Dir(sysDir)
	brainDir := filepath.Dir(convDir)
	if filepath.Base(brainDir) != "brain" {
		return ""
	}
	return brainDir
}

func firstLineHasParentSender(path, parentID string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 8192)
	n, _ := f.Read(buf)
	if n == 0 {
		return false
	}
	line := buf[:n]
	if i := bytes.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	var first struct {
		Source  string `json:"source"`
		Type    string `json:"type"`
		Content string `json:"content"`
	}
	if json.Unmarshal(line, &first) != nil {
		return false
	}
	return (first.Type == "SYSTEM_MESSAGE" || first.Source == "SYSTEM") &&
		strings.Contains(first.Content, "sender="+parentID)
}

// Reply: mako does not hook agy's approvals, so it never answers one.
func (*Agent) Reply(string, string) []byte { return []byte("{}\n") }
