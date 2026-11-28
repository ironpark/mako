package claude

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/protocol"
)

// hookEvents are the events mako hooks. Notification says when Claude asks
// the user something.
var hookEvents = []agent.HookEvent{
	{Name: "SessionStart", Timeout: agent.HookTimeout}, {Name: "UserPromptSubmit", Timeout: agent.HookTimeout},
	{Name: "PostToolUse", Timeout: agent.HookTimeout}, {Name: "PermissionRequest", Timeout: agent.ApprovalTimeout},
	{Name: "Notification", Timeout: agent.HookTimeout}, {Name: "PreCompact", Timeout: agent.HookTimeout}, {Name: "Stop", Timeout: agent.HookTimeout},
	{Name: "SessionEnd", Timeout: agent.HookTimeout},
	{Name: "SubagentStart", Timeout: agent.HookTimeout}, {Name: "SubagentStop", Timeout: agent.HookTimeout},
	{Name: "StopFailure", Timeout: agent.HookTimeout},
}

// settingsFlag takes settings for one launch, hooks and a status line among
// them, alongside the user's own.
const settingsFlag = "--settings"

// WithCommitSignature configures Claude Code's git commit attribution
// signature. When enabled is false, it sets attribution.commit to "" in
// --settings so Claude Code omits its Co-Authored-By trailer. When enabled is
// true, Claude Code's own default or user settings apply.
func (*Agent) WithCommitSignature(l *agent.Launch, enabled bool) {
	if enabled {
		return
	}
	mergeSettings(l, func(doc map[string]any) {
		if b, ok := doc["attribution"].(bool); ok && !b {
			return
		}
		attr, _ := doc["attribution"].(map[string]any)
		if attr == nil {
			attr = map[string]any{}
		}
		attr["commit"] = ""
		doc["attribution"] = attr
	})
}

// WithHooks gives the hooks, and the status line, as a --settings document.
// If the command already passes --settings pointing to readable JSON or an
// inline JSON object, mako merges its hooks (preserving existing matchers) and
// sets statusLine unless already configured; an unreadable --settings is left
// untouched and WithHooks returns false.
func (*Agent) WithHooks(l *agent.Launch, hook, status, _ string) bool {
	type handler struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Timeout int    `json:"timeout,omitempty"`
	}
	type matcher struct {
		Hooks []handler `json:"hooks"`
	}
	makoHooks := map[string][]matcher{}
	for _, e := range hookEvents {
		h := handler{Type: "command", Command: hook}
		if e.Timeout != agent.HookTimeout {
			h.Timeout = e.Timeout
		}
		makoHooks[e.Name] = []matcher{{Hooks: []handler{h}}}
	}

	return mergeSettings(l, func(doc map[string]any) {
		existingHooks, _ := doc["hooks"].(map[string]any)
		if existingHooks == nil {
			existingHooks = map[string]any{}
		}
		for name, added := range makoHooks {
			var list []any
			if cur, ok := existingHooks[name].([]any); ok {
				list = append(list, cur...)
			}
			for _, m := range added {
				list = append(list, m)
			}
			existingHooks[name] = list
		}
		doc["hooks"] = existingHooks
		if status != "" {
			if _, has := doc["statusLine"]; !has {
				doc["statusLine"] = map[string]any{"type": "command", "command": status}
			}
		}
	})
}

// mergeSettings updates an existing --settings JSON document in l.Command in
// place, or appends a new --settings flag when none is present. It returns
// false without modifying l.Command if an existing --settings value cannot be
// read or parsed as a JSON object.
func mergeSettings(l *agent.Launch, mutate func(doc map[string]any)) bool {
	for i := 1; i < len(l.Command); i++ {
		arg := l.Command[i]
		var raw string
		inlineEq := false
		switch {
		case arg == settingsFlag && i+1 < len(l.Command):
			raw = l.Command[i+1]
		case strings.HasPrefix(arg, settingsFlag+"="):
			raw = strings.TrimPrefix(arg, settingsFlag+"=")
			inlineEq = true
		default:
			continue
		}
		doc, ok := loadSettingsJSON(raw, l.Env)
		if !ok {
			return false
		}
		mutate(doc)
		merged, err := json.Marshal(doc)
		if err != nil {
			return false
		}
		if inlineEq {
			l.Command[i] = settingsFlag + "=" + string(merged)
		} else {
			l.Command[i+1] = string(merged)
		}
		return true
	}

	doc := map[string]any{}
	mutate(doc)
	settings, err := json.Marshal(doc)
	if err != nil {
		return false
	}
	l.Command = append(l.Command, settingsFlag, string(settings))
	return true
}

func loadSettingsJSON(raw string, env map[string]string) (map[string]any, bool) {
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

// hookInput extends agent.HookInput with Claude-specific StopFailure fields.
type hookInput struct {
	agent.HookInput
	Error                string          `json:"error"`
	ErrorType            string          `json:"error_type"`
	ErrorMessage         string          `json:"error_message"`
	ErrorDetails         json.RawMessage `json:"error_details"`
	LastAssistantMessage string          `json:"last_assistant_message"`
}

// Event also reads a Notification, by which Claude says it asks the user
// something, and StopFailure, which may report a provider authentication error.
func (*Agent) Event(input []byte) (agent.Event, bool) {
	var in hookInput
	if json.Unmarshal(input, &in) != nil || in.Event == "" {
		return agent.Event{}, false
	}
	// A subagent's own session is not the terminal's; only its lifecycle,
	// as the parent reports it, is followed.
	if in.AgentID != "" {
		switch in.Event {
		case "SubagentStart":
			return agent.Event{Kind: agent.ChildStarted, SessionID: in.SessionID, AgentID: in.AgentID}, true
		case "SubagentStop":
			return agent.Event{Kind: agent.ChildStopped, SessionID: in.SessionID, AgentID: in.AgentID}, true
		}
		return agent.Event{}, false
	}
	if in.Event == "Notification" {
		if in.NotificationType == "idle_prompt" {
			return agent.Event{Kind: agent.Idle, SessionID: in.SessionID}, true
		}
		// A permission it asks through mako is already an approval.
		if in.NotificationType != "permission_prompt" && in.NotificationType != "elicitation_dialog" {
			return agent.Event{}, false
		}
		return agent.Event{Kind: agent.Waiting, SessionID: in.SessionID}, true
	}
	if in.Event == "StopFailure" {
		return agent.Event{
			Kind:       agent.Idle,
			SessionID:  in.SessionID,
			Transcript: in.TranscriptPath,
			AuthError:  classifyStopFailure(in.Error, in.ErrorType, in.ErrorMessage, in.ErrorDetails, in.LastAssistantMessage),
		}, true
	}
	return in.Base()
}

// Status reads what the status line's input says of the session's tokens:
// the model's context window, and the account's limits.
func (*Agent) Status(input []byte) *protocol.Usage {
	type limit struct {
		Used   float64 `json:"used_percentage"`
		Resets int64   `json:"resets_at"`
	}
	var state struct {
		Context struct {
			Size int64 `json:"context_window_size"`
		} `json:"context_window"`
		Limits struct {
			FiveHour *limit `json:"five_hour"`
			SevenDay *limit `json:"seven_day"`
		} `json:"rate_limits"`
	}
	if json.Unmarshal(input, &state) != nil {
		return &protocol.Usage{}
	}
	u := &protocol.Usage{Window: state.Context.Size}
	for minutes, l := range map[int64]*limit{protocol.FiveHours: state.Limits.FiveHour, protocol.Week: state.Limits.SevenDay} {
		if l != nil {
			u.SetLimit(&protocol.RateLimit{Minutes: minutes, Used: l.Used, Resets: l.Resets})
		}
	}
	return u
}

// UserStatusLine is the status line command Claude's own settings name,
// "" for none: the project's local settings, then its shared ones, then
// the user's, as Claude ranks them.
func (*Agent) UserStatusLine(input []byte) string {
	var state struct {
		Cwd       string `json:"cwd"`
		Workspace struct {
			ProjectDir string `json:"project_dir"`
		} `json:"workspace"`
	}
	_ = json.Unmarshal(input, &state)
	project := state.Workspace.ProjectDir
	if project == "" {
		project = state.Cwd
	}
	config := place.Root(map[string]string{place.RootEnv: os.Getenv(place.RootEnv), "HOME": os.Getenv("HOME")})
	var files []string
	if project != "" {
		files = append(files, filepath.Join(project, ".claude", "settings.local.json"), filepath.Join(project, ".claude", "settings.json"))
	}
	files = append(files, filepath.Join(config, "settings.json"))
	for _, path := range files {
		body, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var settings struct {
			StatusLine *struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"statusLine"`
		}
		if json.Unmarshal(body, &settings) != nil || settings.StatusLine == nil {
			continue
		}
		if settings.StatusLine.Type != "command" {
			return ""
		}
		return settings.StatusLine.Command
	}
	return ""
}
