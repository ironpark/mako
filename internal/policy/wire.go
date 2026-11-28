package policy

import (
	"slices"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ironpark/mako/internal/protocol"
)

// RuleProto is r as the protocol carries it.
func RuleProto(r Rule) *protocol.PolicyRule {
	return &protocol.PolicyRule{
		Id: r.ID, Name: r.Name, Enabled: r.Enabled, Action: string(r.Action),
		Agents: slices.Clone(r.Agents), Tool: r.Tool, Pattern: r.Pattern, Message: r.Message,
	}
}

// RuleFromProto is the rule p carries.
func RuleFromProto(p *protocol.PolicyRule) Rule {
	return Rule{
		ID: p.GetId(), Name: p.GetName(), Enabled: p.GetEnabled(), Action: Action(p.GetAction()),
		Agents: slices.Clone(p.GetAgents()), Tool: p.GetTool(), Pattern: p.GetPattern(), Message: p.GetMessage(),
	}
}

// AuditProto is e as the protocol carries it.
func AuditProto(e AuditEntry) *protocol.PolicyDecision {
	return &protocol.PolicyDecision{
		Time: timestamppb.New(time.Unix(e.Time, 0)), Session: e.Session, Agent: e.Agent, Tool: e.Tool,
		Detail: e.Detail, Action: string(e.Action), RuleId: e.RuleID, Rule: e.Rule,
	}
}
