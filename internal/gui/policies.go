package gui

import (
	"slices"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/policy"
)

func policySettings(m *model) ggui.Widget {
	dir, _ := config.Dir()
	loadRules := func() []policy.Rule {
		list, _ := policy.LoadRules(dir)
		return list
	}
	loadAudit := func() []policy.AuditEntry {
		list, _ := policy.LoadAudit(dir)
		return list
	}

	rules := ggui.State(loadRules())
	audit := ggui.State(loadAudit())
	ruleErr := ggui.State("")

	// Rule form state
	rName := ggui.State("")
	rAction := ggui.State(string(policy.ActionAllow))
	rTool := ggui.State("Bash")
	rPattern := ggui.State("")
	rAgents := ggui.State("")
	rMessage := ggui.State("")

	saveRules := func(next []policy.Rule) bool {
		if err := policy.SaveRules(dir, next); err != nil {
			ruleErr.Set(err.Error())
			return false
		}
		ruleErr.Set("")
		rules.Set(loadRules())
		return true
	}

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
			Message: ggui.Peek(rMessage),
		}.Normalized()
		if err := r.Validate(); err != nil {
			ruleErr.Set(err.Error())
			return
		}
		next := append(slices.Clone(ggui.Peek(rules)), r)
		if saveRules(next) {
			rName.Set("")
			rPattern.Set("")
			rMessage.Set("")
		}
	}

	rulesSection := ggui.Reactive(func() ggui.Widget {
		list := rules.Get()
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
					cur := slices.Clone(ggui.Peek(rules))
					if idx < len(cur) {
						cur[idx].Enabled = !cur[idx].Enabled
						saveRules(cur)
					}
				}).Ghost().Pad(4, 8).Radius(6),
				iconButton("x", T("Delete rule"), func() {
					cur := slices.Clone(ggui.Peek(rules))
					if idx < len(cur) {
						saveRules(slices.Delete(cur, idx, idx+1))
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
			widget.Row(
				widget.Expanded(ui.TextField(rName).Placeholder(T("Rule name (optional)")).Name(T("Rule name"))),
				widget.Box(ui.TextField(rAgents).Placeholder(T("Agents (empty = all)")).Name(T("Agents"))).Width(180),
				ui.Button(T("Add rule"), addRule),
			).Gap(8).Align(widget.AlignCenter),
			ggui.Reactive(func() ggui.Widget {
				if e := ruleErr.Get(); e != "" {
					return widget.Text(e).Size(12).Color(skin.DotDown)
				}
				return widget.Box()
			}),
		).Gap(8).Stretch()).Pad(12, 16))
		return card(rows...)
	})

	auditSection := ggui.Reactive(func() ggui.Widget {
		list := audit.Get()
		if len(list) == 0 {
			return widget.Box()
		}
		shown := list[:min(len(list), 12)]
		rows := make([]ggui.Widget, 0, len(shown)+1)
		rows = append(rows, widget.Box(widget.Row(
			widget.Expanded(widget.Text(T("Policy audit log")).Size(13).Weight(ggui.WeightSemibold)),
			ui.Button(T("Refresh"), func() { audit.Set(loadAudit()) }).Ghost().Pad(4, 8).Radius(6),
		).Align(widget.AlignCenter)).Pad(10, 16))
		for _, e := range shown {
			col := pick(e.Action == policy.ActionAllow, skin.DotRunning, skin.DotDown)
			ts := time.Unix(e.Time, 0).Local().Format("01-02 15:04:05")
			detail := strings.TrimSpace(e.Agent + " · " + e.Tool + " " + firstLine(e.Detail))
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
