package agent

import (
	"encoding/json"

	"github.com/ironpark/mako/internal/protocol"
)

// EventKind is what a hook's event says the agent is doing, whatever the
// agent calls the event.
type EventKind int

const (
	// Started: a conversation began or was switched to, Event.SessionID
	// logged at Event.Transcript.
	Started  EventKind = iota + 1
	Prompted           // the user gave it a turn
	ToolDone           // a tool it ran finished
	Approval           // it asks leave to run Event.Tool with Event.Input
	Waiting            // it waits on the user
	Stopped            // it finished its turn
	Ended              // its session ended
)

// Event is what a hook reports.
type Event struct {
	Kind       EventKind
	SessionID  string
	Transcript string
	Tool       string
	Input      json.RawMessage
	// Question is an Approval for a tool that asks the user something
	// rather than for leave: only the agent's own prompt takes the answer.
	Question bool
}

// ApprovalTimeout is how long an agent waits on the hook for an approval:
// as long as the user may take to answer it.
const ApprovalTimeout = 24 * 60 * 60

// HookTimeout is how long an agent waits on any other event's hook, which
// only reports.
const HookTimeout = 10

// HookEvent is an event a driver hooks, by the agent's name for it, and how
// long the agent waits on the hook, in seconds.
type HookEvent struct {
	Name    string
	Timeout int
}

// HookInput is what the agents that hook as Claude Code does hand a hook.
type HookInput struct {
	Event          string `json:"hook_event_name"`
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	// AgentID is set in a subagent's session, which is not the terminal's.
	AgentID string `json:"agent_id"`
	// NotificationType is Claude's kind of Notification: permission_prompt,
	// idle_prompt, elicitation_dialog…
	NotificationType string          `json:"notification_type"`
	Tool             string          `json:"tool_name"`
	ToolInput        json.RawMessage `json:"tool_input"`
}

// ParseHookInput reads a hook's input as HookInput, false for input it
// cannot read or a subagent's.
func ParseHookInput(input []byte) (HookInput, bool) {
	var in HookInput
	if json.Unmarshal(input, &in) != nil || in.Event == "" || in.AgentID != "" {
		return HookInput{}, false
	}
	return in, true
}

// Base is the event in the names every agent hooking this way shares; false for
// one mako does not follow.
func (in HookInput) Base() (Event, bool) {
	ev := Event{SessionID: in.SessionID, Transcript: in.TranscriptPath, Tool: in.Tool, Input: in.ToolInput}
	switch in.Event {
	case "SessionStart":
		ev.Kind = Started
	case "UserPromptSubmit":
		ev.Kind = Prompted
	case "PostToolUse":
		ev.Kind = ToolDone
	case "PermissionRequest":
		ev.Kind = Approval
	case "Stop":
		ev.Kind = Stopped
	case "SessionEnd":
		ev.Kind = Ended
	default:
		return Event{}, false
	}
	return ev, true
}

// CommonHooks reads and answers hooks as Claude Code and Codex hand them
// over: a driver whose agent hooks that way embeds it, for Hooks' Event and
// Reply.
type CommonHooks struct{}

func (CommonHooks) Event(input []byte) (Event, bool) {
	in, ok := ParseHookInput(input)
	if !ok {
		return Event{}, false
	}
	return in.Base()
}

func (CommonHooks) Reply(decision, message string) []byte { return PermissionReply(decision, message) }

// PermissionReply is the answer to a PermissionRequest hook, as Claude Code
// and Codex read it.
func PermissionReply(decision, message string) []byte {
	d := map[string]any{"behavior": decision}
	if decision == protocol.DecisionDeny {
		d["message"] = message
	}
	out, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName": "PermissionRequest",
		"decision":      d,
	}})
	return append(out, '\n')
}
