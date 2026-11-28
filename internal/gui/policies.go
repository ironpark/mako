package gui

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"
	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/policy"
	"github.com/ironpark/mako/internal/protocol"
)

// policiesState is what Settings > Policies shows of the permission
// policies of the daemon the window talks through: they live in its
// configuration directory, which need not be on this machine.
type policiesState struct {
	Info    *ggui.StateValue[*protocol.Policies]
	Loading *ggui.StateValue[bool]
	Note    *ggui.StateValue[string] // why the last request or rule failed; "" for none
	Asked   bool
	Node    string // the node whose daemon Info came from
}

func newPoliciesState() *policiesState {
	return &policiesState{
		Info: ggui.State((*protocol.Policies)(nil)).WithEqual(func(a, b *protocol.Policies) bool {
			return proto.Equal(a, b)
		}),
		Loading: ggui.State(false),
		Note:    ggui.State(""),
	}
}

// ensurePoliciesLoaded asks the daemon for its policies once, and again
// once the window talks through another node's.
func (m *model) ensurePoliciesLoaded() {
	if m.policies == nil || m.post == nil || ggui.Peek(m.policies.Loading) {
		return
	}
	node := ggui.Peek(m.Conn).Self.GetId()
	if m.policies.Asked && m.policies.Node == node {
		return
	}
	m.policies.Asked, m.policies.Node = true, node
	m.loadPolicies()
}

func (m *model) loadPolicies() {
	if m.policies == nil || m.post == nil {
		return
	}
	m.policies.Loading.Set(true)
	fetch(m, 15*time.Second, func(ctx context.Context, c *client.Client) (*protocol.Policies, error) {
		return c.Policies(ctx)
	}, func(res *protocol.Policies, err error) {
		m.policies.Loading.Set(false)
		if err != nil {
			m.policies.Note.Set(errorText(err))
			return
		}
		m.policies.Note.Set("")
		m.policies.Info.Set(res)
	})
}

// savePolicyRules has the daemon take rules in place of its own; onOK runs
// once it did.
func (m *model) savePolicyRules(rules []policy.Rule, onOK func()) {
	if m.policies == nil || m.post == nil {
		return
	}
	req := &protocol.SetPolicies{}
	for _, r := range rules {
		req.Rules = append(req.Rules, policy.RuleProto(r))
	}
	m.policies.Note.Set("")
	fetch(m, 15*time.Second, func(ctx context.Context, c *client.Client) (*protocol.Policies, error) {
		return c.SetPolicies(ctx, req)
	}, func(res *protocol.Policies, err error) {
		if err != nil {
			m.policies.Note.Set(errorText(err))
			return
		}
		m.policies.Info.Set(res)
		if onOK != nil {
			onOK()
		}
	})
}

// policyRules are the rules p carries.
func policyRules(p *protocol.Policies) []policy.Rule {
	rules := make([]policy.Rule, 0, len(p.GetRules()))
	for _, r := range p.GetRules() {
		rules = append(rules, policy.RuleFromProto(r))
	}
	return rules
}

func policySettings(m *model) ggui.Widget {
	m.ensurePoliciesLoaded()

	// Rule form state
	rName := ggui.State("")
	rAction := ggui.State(string(policy.ActionAllow))
	rTool := ggui.State("Bash")
	rPattern := ggui.State("")
	rAgents := ggui.State("")
	rMessage := ggui.State("")

	addRule := func() {
		var agents []string
		for a := range strings.SplitSeq(ggui.Peek(rAgents), ",") {
			if a = strings.TrimSpace(a); a != "" {
				agents = append(agents, a)
			}
		}
		r := policy.Rule{
			Name:    ggui.Peek(rName),
			Enabled: true,
			Action:  policy.Action(ggui.Peek(rAction)),
			Agents:  agents,
			Tool:    ggui.Peek(rTool),
			Pattern: ggui.Peek(rPattern),
		}
		if r.Action == policy.ActionDeny {
			r.Message = ggui.Peek(rMessage)
		}
		r = r.Normalized()
		if err := r.Validate(); err != nil {
			m.policies.Note.Set(err.Error())
			return
		}
		next := append(policyRules(ggui.Peek(m.policies.Info)), r)
		m.savePolicyRules(next, func() {
			rName.Set("")
			rPattern.Set("")
			rMessage.Set("")
		})
	}

	rulesSection := ggui.Reactive(func() ggui.Widget {
		info := m.policies.Info.Get()
		if info == nil {
			msg := T("Loading policies…")
			if !m.policies.Loading.Get() {
				msg = T("Connect to this node's daemon to manage its policies.")
			}
			rows := []ggui.Widget{widget.Box(widget.Row(
				widget.Expanded(muted(widget.Text(msg).Size(12.5))),
				ui.Button(T("Retry"), func() { m.loadPolicies() }).Outline(),
			).Gap(12).Align(widget.AlignCenter)).Pad(14, 16)}
			if note := m.policies.Note.Get(); note != "" {
				rows = append(rows, widget.Box(widget.Text(note).Size(12).Color(skin.DotDown)).Pad(0, 16, 12, 16))
			}
			return card(rows...)
		}
		list := policyRules(info)
		rows := []ggui.Widget{
			widget.Box(widget.Column(
				widget.Text(T("Permission rules")).Size(13.5).Weight(ggui.WeightSemibold),
				muted(widget.Text(T("Evaluated before approvals are shown across every agent. Deny rules always take precedence over allow rules.")).Size(12)),
			).Gap(2).Stretch()).Pad(12, 16),
		}
		if len(list) == 0 {
			rows = append(rows, widget.Box(muted(widget.Text(T("No permission rules configured yet.")).Size(12.5))).Pad(10, 16))
		}
		for i, item := range list {
			idx := i
			r := item
			col := pick(r.Action == policy.ActionAllow, skin.DotRunning, skin.DotDown)
			label := r.Name
			if label == "" {
				label = r.Tool
			}
			spec := r.Tool
			if r.Pattern != "" {
				spec += " · " + r.Pattern
			}
			if len(r.Agents) > 0 {
				spec += " · " + strings.Join(r.Agents, ", ")
			}
			rows = append(rows, widget.Box(widget.Row(
				dot(pick(r.Enabled, col, skin.ExtraMuted)),
				widget.Box(widget.Text(pick(r.Action == policy.ActionAllow, T("Allow"), T("Deny"))).Size(11).Weight(ggui.WeightMedium).Color(col).NoWrap()).
					Fill(col.Alpha(0.14)).Radius(6).Pad(2, 7),
				widget.Expanded(widget.Column(
					widget.Text(label).Size(13).Weight(ggui.WeightMedium),
					muted(widget.Text(spec).Size(12).Font(ggui.DefaultMonoFont()).NoWrap().Ellipsis()),
				).Gap(2).Stretch()),
				ui.Button(pick(r.Enabled, T("Disable"), T("Enable")), func() {
					cur := policyRules(ggui.Peek(m.policies.Info))
					if idx < len(cur) {
						cur[idx].Enabled = !cur[idx].Enabled
						m.savePolicyRules(cur, nil)
					}
				}).Ghost().Pad(4, 8).Radius(6),
				iconButton("x", T("Delete rule"), func() {
					cur := policyRules(ggui.Peek(m.policies.Info))
					if idx < len(cur) {
						m.savePolicyRules(slices.Delete(cur, idx, idx+1), nil)
					}
				}),
			).Gap(8).Align(widget.AlignCenter)).Pad(10, 16))
		}
		rows = append(rows, widget.Box(widget.Column(
			widget.Row(
				widget.Box(ui.Select(rAction).Options([]string{string(policy.ActionAllow), string(policy.ActionDeny)}).Format(func(v string) string {
					if v == string(policy.ActionDeny) {
						return T("Deny")
					}
					return T("Allow")
				}).Name(T("Action"))).Width(110),
				widget.Box(ui.TextField(rTool).Placeholder("Bash / Edit / *").Name(T("Tool"))).Width(140),
				widget.Expanded(ui.TextField(rPattern).Placeholder(T("Pattern glob (e.g. git status*, *--force*)")).Name(T("Pattern"))),
			).Gap(8).Align(widget.AlignCenter),
			ggui.Reactive(func() ggui.Widget {
				// What a deny tells the agent; an allow tells it nothing.
				if rAction.Get() != string(policy.ActionDeny) {
					return widget.Box()
				}
				return ui.TextField(rMessage).Placeholder(T("What the agent is told when this rule denies a call (optional)")).Name(T("Deny message"))
			}),
			widget.Row(
				widget.Expanded(ui.TextField(rName).Placeholder(T("Rule name (optional)")).Name(T("Rule name"))),
				widget.Box(ui.TextField(rAgents).Placeholder(T("Agents (empty = all)")).Name(T("Agents"))).Width(180),
				ui.Button(T("Add rule"), addRule),
			).Gap(8).Align(widget.AlignCenter),
			ggui.Reactive(func() ggui.Widget {
				if e := m.policies.Note.Get(); e != "" {
					return widget.Text(e).Size(12).Color(skin.DotDown)
				}
				return widget.Box()
			}),
		).Gap(8).Stretch()).Pad(12, 16))
		return card(rows...)
	})

	auditSection := ggui.Reactive(func() ggui.Widget {
		list := m.policies.Info.Get().GetDecisions()
		if len(list) == 0 {
			return widget.Box()
		}
		shown := list[:min(len(list), 12)]
		rows := make([]ggui.Widget, 0, len(shown)+1)
		rows = append(rows, widget.Box(widget.Row(
			widget.Expanded(widget.Text(T("Policy audit log")).Size(13).Weight(ggui.WeightSemibold)),
			ui.Button(T("Refresh"), func() { m.loadPolicies() }).Ghost().Pad(4, 8).Radius(6),
		).Align(widget.AlignCenter)).Pad(10, 16))
		for _, e := range shown {
			col := pick(e.GetAction() == string(policy.ActionAllow), skin.DotRunning, skin.DotDown)
			ts := e.GetTime().AsTime().Local().Format("01-02 15:04:05")
			detail := strings.TrimSpace(e.GetAgent() + " · " + e.GetTool() + " " + firstLine(e.GetDetail()))
			rows = append(rows, widget.Box(widget.Row(
				dot(col),
				widget.Expanded(muted(widget.Text(detail).Size(12).NoWrap().Ellipsis())),
				muted(widget.Text(ts).Size(11.5).NoWrap()),
			).Gap(8).Align(widget.AlignCenter)).Pad(8, 16))
		}
		return card(rows...)
	})

	return page(T("Policies"), T("Cross-agent permission rules and the decisions they made."),
		widget.Column(rulesSection, auditSection).Gap(16).Stretch())
}
