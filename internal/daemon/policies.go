package daemon

import (
	"errors"

	"github.com/ironpark/mako/internal/policy"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transport"
)

var errPolicyPeer = errors.New("a node's permission policies are its own front-ends' alone")

// policies are the rules in the daemon's configuration directory and the
// approvals they decided, as list_policies answers.
func (d *Daemon) policies() (*protocol.Policies, error) {
	rules, err := policy.LoadRules(d.dir)
	if err != nil {
		return nil, err
	}
	audit, err := policy.LoadAudit(d.dir)
	if err != nil {
		return nil, err
	}
	out := &protocol.Policies{}
	for _, r := range rules {
		out.Rules = append(out.Rules, policy.RuleProto(r))
	}
	for _, e := range audit {
		out.Decisions = append(out.Decisions, policy.AuditProto(e))
	}
	return out, nil
}

func (d *Daemon) handleListPolicies(stream *transport.Stream, env *protocol.Envelope, fromPeer bool) error {
	if fromPeer {
		return errPolicyPeer
	}
	out, err := d.policies()
	if err != nil {
		return err
	}
	return stream.Send(protocol.TypePolicies, env.GetId(), out)
}

func (d *Daemon) handleSetPolicies(stream *transport.Stream, env *protocol.Envelope, fromPeer bool) error {
	if fromPeer {
		return errPolicyPeer
	}
	req, err := protocol.Body[*protocol.SetPolicies](env)
	if err != nil {
		return err
	}
	rules := make([]policy.Rule, 0, len(req.GetRules()))
	for _, r := range req.GetRules() {
		rules = append(rules, policy.RuleFromProto(r))
	}
	if err := policy.SaveRules(d.dir, rules); err != nil {
		return err
	}
	out, err := d.policies()
	if err != nil {
		return err
	}
	return stream.Send(protocol.TypePolicies, env.GetId(), out)
}
