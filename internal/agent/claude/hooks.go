package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
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

// WithHooks gives the hooks, and the status line, as a --settings
// document; a command with settings of its own keeps them, without mako's.
func (*Agent) WithHooks(l *agent.Launch, hook, status, _ string) bool {
	if slices.ContainsFunc(l.Command[1:], func(arg string) bool {
		name, _, _ := strings.Cut(arg, "=")
		return name == settingsFlag
	}) {
		return false
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
	for _, e := range hookEvents {
		h := handler{Type: "command", Command: hook}
		if e.Timeout != agent.HookTimeout {
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
		return false
	}
	l.Command = append(l.Command, settingsFlag, string(settings))
	return true
}

// Event also reads a Notification, by which Claude says it asks the user
// something.
func (*Agent) Event(input []byte) (agent.Event, bool) {
	var in agent.HookInput
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
		return agent.Event{Kind: agent.Idle, SessionID: in.SessionID}, true
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
