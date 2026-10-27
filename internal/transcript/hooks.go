package transcript

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"unicode"
)

// An agent's hooks tell mako what its conversation is and what the agent
// is doing: which conversation it started or switched to, when it takes a
// turn, asks for an approval, and finishes. Claude takes them as settings
// for one launch, and Codex as config overrides it trusts by their hash.

// hookStyle is how an agent takes hooks for one launch.
type hookStyle int

const (
	noHooks       hookStyle = iota
	settingsHooks           // a --settings JSON document, alongside the user's own
	configHooks             // -c overrides of the hooks table, with their trust
)

// hookEvent is an event the hook is run for, and how long the agent waits
// on it, in seconds.
type hookEvent struct {
	Name    string
	Timeout int
}

// ApprovalTimeout is how long an agent waits on the hook for an approval:
// as long as the user may take to answer it.
const ApprovalTimeout = 24 * 60 * 60

// hookTimeout is how long an agent waits on any other event's hook, which
// only reports.
const hookTimeout = 10

// hookEvents are the events each style hooks. Claude's Notification says
// when it asks the user something; Codex has none, and asks only for
// approvals.
var hookEvents = map[hookStyle][]hookEvent{
	settingsHooks: {
		{"SessionStart", hookTimeout}, {"UserPromptSubmit", hookTimeout}, {"PostToolUse", hookTimeout},
		{"PermissionRequest", ApprovalTimeout}, {"Notification", hookTimeout}, {"Stop", hookTimeout},
		{"SessionEnd", hookTimeout},
	},
	configHooks: {
		{"SessionStart", hookTimeout}, {"UserPromptSubmit", hookTimeout}, {"PostToolUse", hookTimeout},
		{"PermissionRequest", ApprovalTimeout}, {"Stop", hookTimeout}, {"SessionEnd", 3},
	},
}

// HasHooks reports whether the agent takes hooks for a launch.
func (a *Agent) HasHooks() bool { return a.hooks != noHooks }

// HookFeature is the agent's command that lists its features, and the
// feature hooks need: not every version has them. Both are empty when every
// version does.
func (a *Agent) HookFeature() (args []string, feature string) {
	if a.hooks == configHooks {
		return []string{"features", "list"}, "hooks"
	}
	return nil, ""
}

// Hook makes command run the shell command hook on the events mako
// follows, alongside the user's own hooks. The hook reads the event, as
// JSON, on stdin. An agent with a status line runs the shell command status
// as its status line, unless status is "": it reads the session's state,
// the account's limits among it, and prints the user's own. It reports
// false for an agent without hooks, or a command that brings its own
// settings.
func (a *Agent) Hook(command []string, hook, status string) ([]string, bool) {
	switch a.hooks {
	case settingsHooks:
		if slices.ContainsFunc(command[1:], func(arg string) bool {
			name, _, _ := strings.Cut(arg, "=")
			return name == a.settings
		}) {
			return command, false
		}
		type handler struct {
			Type    string `json:"type"`
			Command string `json:"command"`
			Timeout int    `json:"timeout,omitempty"`
		}
		type matcher struct {
			Hooks []handler `json:"hooks"`
		}
		hooks := map[string][]matcher{}
		for _, e := range hookEvents[a.hooks] {
			h := handler{Type: "command", Command: hook}
			if e.Timeout != hookTimeout {
				h.Timeout = e.Timeout
			}
			hooks[e.Name] = []matcher{{Hooks: []handler{h}}}
		}
		doc := map[string]any{"hooks": hooks}
		if status != "" {
			doc["statusLine"] = map[string]any{"type": "command", "command": status}
		}
		settings, err := json.Marshal(doc)
		if err != nil {
			return command, false
		}
		return append(command, a.settings, string(settings)), true
	case configHooks:
		// Overrides go before any subcommand, as the program's own options.
		args := []string{command[0]}
		var trust []string
		for _, e := range hookEvents[a.hooks] {
			args = append(args, "-c", fmt.Sprintf("hooks.%s=[{hooks=[{type=%s,command=%s,timeout=%d}]}]",
				e.Name, tomlString("command"), tomlString(hook), e.Timeout))
			key := fmt.Sprintf("%s:%s:0:0", sessionFlagsSource(), eventKey(e.Name))
			trust = append(trust, fmt.Sprintf("%s={trusted_hash=%s}", tomlString(key), tomlString(codexHookHash(e, hook))))
		}
		// The hooks come from this command line, which Codex trusts only by
		// each one's hash, as /hooks would record it.
		args = append(args, "-c", "hooks.state={"+strings.Join(trust, ",")+"}")
		return append(args, command[1:]...), true
	}
	return command, false
}

// sessionFlagsSource is where Codex says hooks from -c come from, as the
// keys of their trust name it.
func sessionFlagsSource() string {
	if runtime.GOOS == "windows" {
		return `C:\<session-flags>\config.toml`
	}
	return "/<session-flags>/config.toml"
}

// eventKey is Codex's name for an event in a hook's key: snake case.
func eventKey(name string) string {
	var b strings.Builder
	for i, r := range name {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('_')
			}
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}

// codexHookHash is the hash Codex trusts a hook by: of its event and
// handler, normalized, as JSON with sorted keys.
func codexHookHash(e hookEvent, command string) string {
	identity := map[string]any{
		"event_name": eventKey(e.Name),
		"hooks": []map[string]any{{
			"type": "command", "command": command, "timeout": e.Timeout, "async": false,
		}},
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(identity); err != nil {
		panic(err) // plain values
	}
	sum := sha256.Sum256(bytes.TrimRight(buf.Bytes(), "\n"))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// tomlString quotes s as a TOML basic string.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// HookEvent is what the agent hands the hook Hook installs.
type HookEvent struct {
	Event          string `json:"hook_event_name"`
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	// AgentID is set in a subagent's session, which is not the terminal's.
	AgentID string `json:"agent_id"`
	Source  string `json:"source"` // SessionStart's: startup, resume, clear…
	// NotificationType is Claude's kind of Notification: permission_prompt,
	// idle_prompt, elicitation_dialog…
	NotificationType string          `json:"notification_type"`
	Message          string          `json:"message"`
	Tool             string          `json:"tool_name"`
	ToolInput        json.RawMessage `json:"tool_input"`
}

// ParseHook reads what the agent hands the hook Hook installs. It reports
// false for an agent without hooks, input it cannot read, or a subagent's
// event.
func (a *Agent) ParseHook(input []byte) (HookEvent, bool) {
	var ev HookEvent
	if a.hooks == noHooks || json.Unmarshal(input, &ev) != nil || ev.Event == "" || ev.AgentID != "" {
		return HookEvent{}, false
	}
	return ev, true
}
