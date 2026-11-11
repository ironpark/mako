package claude

import (
	"encoding/json"
	"slices"
	"strconv"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/protocol"
)

// Claude Code offers, with a permission it asks for, what else than once
// would allow the call: rules to allow such calls from then on, or a mode
// to switch to, as its PermissionRequest hook's input suggests them and
// the hook's decision takes them back as updatedPermissions. A plan
// (ExitPlanMode) is approved by the mode the work goes on in; the hook
// allows it only with its input given back.

// planModes are the modes a plan is approved into, as its own dialog
// offers them.
var planModes = []string{"auto", "acceptEdits", "default"}

// suggestion is one of a permission's suggestions, as Claude writes it.
type suggestion struct {
	Type     string `json:"type"`
	Mode     string `json:"mode"`
	Behavior string `json:"behavior"`
	Rules    []struct {
		Tool    string `json:"toolName"`
		Content string `json:"ruleContent"`
	} `json:"rules"`
}

// Options are the plan's modes for a plan, else the suggestions mako can
// say: a mode, or rules that allow; each by its place among them.
func (*Agent) Options(kind string, ev agent.Event) []*protocol.ApprovalOption {
	if kind == protocol.ToolExitPlan {
		out := make([]*protocol.ApprovalOption, len(planModes))
		for i, mode := range planModes {
			out[i] = &protocol.ApprovalOption{Id: mode, Mode: mode}
		}
		return out
	}
	var out []*protocol.ApprovalOption
	for i, s := range suggestions(ev) {
		opt := &protocol.ApprovalOption{Id: strconv.Itoa(i)}
		switch {
		case s.Type == "setMode" && s.Mode != "":
			opt.Mode = s.Mode
		case s.Type == "addRules" && s.Behavior == "allow" && len(s.Rules) > 0:
			for _, r := range s.Rules {
				rule := r.Tool
				if r.Content != "" {
					rule += "(" + r.Content + ")"
				}
				opt.Rules = append(opt.Rules, rule)
			}
		default:
			continue
		}
		out = append(out, opt)
	}
	return out
}

// AllowReply allows the call, with the suggestion option names given back
// as it came; a plan with its input, into the mode option names, or into
// the default mode, where each edit is asked for, if none.
func (*Agent) AllowReply(kind string, ev agent.Event, option string) []byte {
	decision := map[string]any{"behavior": protocol.DecisionAllow}
	if kind == protocol.ToolExitPlan {
		mode := "default"
		if slices.Contains(planModes, option) {
			mode = option
		}
		in := ev.Input
		if !json.Valid(in) {
			in = json.RawMessage(`{}`)
		}
		decision["updatedInput"] = in
		decision["updatedPermissions"] = []any{map[string]any{"type": "setMode", "mode": mode, "destination": "session"}}
		return agent.PermissionDecision(decision)
	}
	raw := rawSuggestions(ev)
	if i, err := strconv.Atoi(option); err == nil && i >= 0 && i < len(raw) {
		decision["updatedPermissions"] = []json.RawMessage{raw[i]}
	}
	return agent.PermissionDecision(decision)
}

// DenyMessage tells Claude a plan denied with no word of the user's is not
// refused, only not yet approved.
func (*Agent) DenyMessage(kind string) string {
	if kind == protocol.ToolExitPlan {
		return "The user has not approved the plan yet; keep planning."
	}
	return ""
}

// rawSuggestions are ev's suggestions as they came, each in its place.
func rawSuggestions(ev agent.Event) []json.RawMessage {
	var raw []json.RawMessage
	_ = json.Unmarshal(ev.Suggestions, &raw)
	return raw
}

// suggestions are ev's suggestions, as far as they read; each in its place.
func suggestions(ev agent.Event) []suggestion {
	raw := rawSuggestions(ev)
	out := make([]suggestion, len(raw))
	for i, r := range raw {
		_ = json.Unmarshal(r, &out[i])
	}
	return out
}
