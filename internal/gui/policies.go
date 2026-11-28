package gui

import (
	"context"
	"errors"
	"slices"
	"strconv"
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
	// Info is the policies of node's daemon; nil until they are read, and
	// while the window talks to no daemon or to one too old to keep any.
	Info    *ggui.StateValue[*protocol.Policies]
	Loading *ggui.StateValue[bool]
	// Busy says a save is in flight. Each change is made to the rules
	// Info holds, so none is made until the save before it is answered.
	Busy *ggui.StateValue[bool]
	Note *ggui.StateValue[string] // why the last request or rule failed; "" for none
	Old  *ggui.StateValue[bool]   // node's daemon is too old to keep policies
	// node is the node Info is of, or is asked of; "" for none. key is
	// node with its daemon's revision, which a daemon updated changes.
	// gen counts requests, so a reply to one that another outran, or that
	// was for another node, is dropped. UI goroutine only.
	node, key string
	gen       int
}

func newPoliciesState() *policiesState {
	return &policiesState{
		Info: ggui.State((*protocol.Policies)(nil)).WithEqual(func(a, b *protocol.Policies) bool {
			return proto.Equal(a, b)
		}),
		Loading: ggui.State(false),
		Busy:    ggui.State(false),
		Note:    ggui.State(""),
		Old:     ggui.State(false),
	}
}

// errPolicyNode refuses a request to a daemon other than the one the page
// shows the policies of.
var errPolicyNode = errors.New("the window now talks to another node")

// followPolicies keeps the page on the node the window talks to, as conn
// says: once it talks to another, to none, or to its daemon updated, what
// the page showed is dropped, along with any reply still to come, and the
// node's policies are asked for.
func (m *model) followPolicies(conn connState) {
	st := m.policies
	if st == nil {
		return
	}
	node, key := "", ""
	if conn.OK && conn.Self.GetId() != "" {
		node = conn.Self.GetId()
		key = node + "@" + strconv.FormatInt(conn.Self.GetRevision(), 10)
	}
	if key == st.key {
		return
	}
	st.node, st.key = node, key
	st.gen++
	st.Info.Set(nil)
	st.Loading.Set(false)
	st.Busy.Set(false)
	st.Note.Set("")
	st.Old.Set(node != "" && conn.Self.GetRevision() < protocol.RevisionPolicies)
	m.loadPolicies()
}

// loadPolicies asks the daemon for its policies, unless a save is in
// flight, whose reply holds them.
func (m *model) loadPolicies() {
	st := m.policies
	if st == nil || m.post == nil || st.node == "" || ggui.Peek(st.Old) || ggui.Peek(st.Busy) {
		return
	}
	st.gen++
	gen, node := st.gen, st.node
	st.Loading.Set(true)
	fetch(m, 15*time.Second, func(ctx context.Context, c *client.Client) (*protocol.Policies, error) {
		if c.Node().GetId() != node {
			return nil, errPolicyNode
		}
		return c.Policies(ctx)
	}, func(res *protocol.Policies, err error) {
		if gen != st.gen || node != st.node {
			return
		}
		st.Loading.Set(false)
		if err != nil {
			st.Note.Set(errorText(err))
			return
		}
		st.Note.Set("")
		st.Info.Set(res)
	})
}

// savePolicyRules has the daemon take rules in place of its own; onOK runs
// once it did. It saves nothing while another save is in flight, nor
// before the node's rules were read, and only to the node they were read
// of.
func (m *model) savePolicyRules(rules []policy.Rule, onOK func()) {
	st := m.policies
	if st == nil || m.post == nil || st.node == "" || ggui.Peek(st.Busy) || ggui.Peek(st.Info) == nil {
		return
	}
	req := &protocol.SetPolicies{}
	for _, r := range rules {
		req.Rules = append(req.Rules, policy.RuleProto(r))
	}
	st.gen++
	gen, node := st.gen, st.node
	st.Busy.Set(true)
	st.Loading.Set(false)
	st.Note.Set("")
	fetch(m, 15*time.Second, func(ctx context.Context, c *client.Client) (*protocol.Policies, error) {
		if c.Node().GetId() != node {
			return nil, errPolicyNode
		}
		return c.SetPolicies(ctx, req)
	}, func(res *protocol.Policies, err error) {
		if gen != st.gen || node != st.node {
			return
		}
		st.Busy.Set(false)
		if err != nil {
			st.Note.Set(errorText(err))
			return
		}
		st.Info.Set(res)
		if onOK != nil {
			onOK()
		}
	})
}

// policyRuleError is why a rule cannot be saved, in the window's language.
func policyRuleError(err error) string {
	switch {
	case errors.Is(err, policy.ErrAction):
		return T("Choose whether the rule allows or denies.")
	case errors.Is(err, policy.ErrNoTool):
		return T("Name the rule's tool, or * for any tool.")
	case errors.Is(err, policy.ErrBlankAgent):
		return T("An agent's name cannot be blank.")
	case errors.Is(err, policy.ErrTooLong):
		return T("A field of this rule is too long.")
	case errors.Is(err, policy.ErrTooMany):
		return Tf("A node keeps at most %d rules.", policy.MaxRules)
	}
	return errorText(err)
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
	ggui.Effect(func() ggui.Cleanup {
		m.followPolicies(m.Conn.Get())
		return nil
	})

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
			m.policies.Note.Set(policyRuleError(err))
			return
		}
		next := append(policyRules(ggui.Peek(m.policies.Info)), r)
		if len(next) > policy.MaxRules {
			m.policies.Note.Set(policyRuleError(policy.ErrTooMany))
			return
		}
		m.savePolicyRules(next, func() {
			rName.Set("")
			rPattern.Set("")
			rMessage.Set("")
		})
	}

	rulesSection := ggui.Reactive(func() ggui.Widget {
		info := m.policies.Info.Get()
		busy := m.policies.Busy.Get()
		if m.policies.Old.Get() {
			return card(widget.Box(warnLine("circle-slash", skin.DiffRemoved, T("The mako on this node is older and cannot do this. Update it there."))).Pad(14, 16))
		}
		if info == nil {
			connected := m.Conn.Get().OK
			loading := m.policies.Loading.Get()
			msg := T("Loading policies…")
			if !connected {
				msg = T("Connect to this node's daemon to manage its policies.")
			} else if !loading {
				msg = T("Could not read this node's policies.")
			}
			rows := []ggui.Widget{widget.Box(widget.Row(
				widget.Expanded(muted(widget.Text(msg).Size(12.5))),
				ui.Button(T("Retry"), func() { m.loadPolicies() }).Outline().Disabled(!connected || loading),
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
			lines := []ggui.Widget{
				widget.Text(label).Size(13).Weight(ggui.WeightMedium),
				muted(widget.Text(spec).Size(12).Font(ggui.DefaultMonoFont()).NoWrap().Ellipsis()),
			}
			// What a deny rule tells the agent.
			if r.Action == policy.ActionDeny && r.Message != "" {
				lines = append(lines, muted(widget.Text(Tf("Tells the agent: %s", r.Message)).Size(12).NoWrap().Ellipsis()))
			}
			rows = append(rows, widget.Box(widget.Row(
				dot(pick(r.Enabled, col, skin.ExtraMuted)),
				widget.Box(widget.Text(pick(r.Action == policy.ActionAllow, T("Allow"), T("Deny"))).Size(11).Weight(ggui.WeightMedium).Color(col).NoWrap()).
					Fill(col.Alpha(0.14)).Radius(6).Pad(2, 7),
				widget.Expanded(widget.Column(lines...).Gap(2).Stretch()),
				ui.Button(pick(r.Enabled, T("Disable"), T("Enable")), func() {
					cur := policyRules(ggui.Peek(m.policies.Info))
					if idx < len(cur) {
						cur[idx].Enabled = !cur[idx].Enabled
						m.savePolicyRules(cur, nil)
					}
				}).Ghost().Pad(4, 8).Radius(6).Disabled(busy),
				ui.Tooltip(ui.ButtonOf(icon("x"), func() {
					cur := policyRules(ggui.Peek(m.policies.Info))
					if idx < len(cur) {
						m.savePolicyRules(slices.Delete(cur, idx, idx+1), nil)
					}
				}).Ghost().Pad(6).Radius(6).Name(T("Delete rule")).Disabled(busy), T("Delete rule")),
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
				ui.Button(pick(busy, T("Saving…"), T("Add rule")), addRule).Disabled(busy),
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
		info := m.policies.Info.Get()
		list, auditErr := info.GetDecisions(), info.GetDecisionsError()
		if len(list) == 0 && auditErr == "" {
			return widget.Box()
		}
		shown := list[:min(len(list), 12)]
		rows := make([]ggui.Widget, 0, len(shown)+2)
		rows = append(rows, widget.Box(widget.Row(
			widget.Expanded(widget.Text(T("Policy audit log")).Size(13).Weight(ggui.WeightSemibold)),
			ui.Button(T("Refresh"), func() { m.loadPolicies() }).Ghost().Pad(4, 8).Radius(6).Disabled(m.policies.Busy.Get() || m.policies.Loading.Get()),
		).Align(widget.AlignCenter)).Pad(10, 16))
		if auditErr != "" {
			rows = append(rows, widget.Box(warnLine("circle-slash", skin.DiffRemoved, Tf("Could not read the audit log: %s", auditErr))).Pad(0, 16, 10, 16))
		}
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
