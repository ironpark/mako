package gui

import (
	"context"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// goalBanner stands over the composer in Chat while a running session has an
// active goal (Claude Code goal_status attachment or Codex thread goal), with
// edit and clear controls when supported by the session.
func goalBanner(m *model, id string) ggui.Widget {
	goal := ggui.Derived(func() *protocol.GoalStatus {
		s, ok := sessionIn(m.Sessions.Get(), id)
		if !ok || s.GetState() != protocol.StateRunning || !s.GetGoal().Active() {
			return nil
		}
		return s.GetGoal()
	}).WithEqual(protocol.Same[*protocol.GoalStatus])

	return ggui.View(goal, func(g *protocol.GoalStatus) ggui.Widget {
		if g == nil {
			return widget.Box()
		}
		items := []ggui.Widget{
			icon("circle-dot").Size(13).Color(skin.AccentBright),
			widget.Text(goalLabel(g)).Size(12.5).Weight(ggui.WeightMedium).Color(skin.AccentBright).NoWrap(),
			widget.Expanded(ui.Tooltip(widget.Text(firstLine(g.GetText())).Size(12.5).NoWrap().Ellipsis(), g.GetText())),
		}
		if g.GetCanEdit() {
			items = append(items, ui.Tooltip(
				ui.ButtonOf(icon("pencil").Size(13).Color(skin.Muted), func() { m.askEditGoal(id) }).
					Ghost().Pad(4).Radius(6).Name(T("Edit goal")),
				T("Edit goal"),
			))
		}
		if g.GetCanClear() {
			items = append(items, ui.Tooltip(
				ui.ButtonOf(icon("x").Size(13).Color(skin.Muted), func() { m.clearGoal(id) }).
					Ghost().Pad(4).Radius(6).Name(T("Clear goal")),
				T("Clear goal"),
			))
		}
		row := widget.Row(items...).Gap(8).Align(widget.AlignCenter)
		return &lane{child: widget.Box(row).Fill(skin.Surface1).Border(1, skin.Border).Radius(10).Pad(6, 12)}
	})
}

func goalLabel(g *protocol.GoalStatus) string {
	switch g.GetProviderStatus() {
	case "paused":
		return T("Goal") + " · " + T("Paused")
	case "blocked":
		return T("Goal") + " · " + T("Blocked")
	case "usageLimited":
		return T("Goal") + " · " + T("Usage limited")
	case "budgetLimited":
		return T("Goal") + " · " + T("Budget limited")
	default:
		return T("Goal")
	}
}

// askEditGoal opens the goal editor dialog over session id.
func (m *model) askEditGoal(id string) {
	s, ok := m.session(id)
	if !ok {
		return
	}
	m.goalSession = id
	m.GoalText.Set(s.GetGoal().GetText())
	m.GoalErr.Set("")
	m.GoalOpen.Set(true)
}

// submitGoal applies the edited goal objective to m.goalSession.
func (m *model) submitGoal() {
	id := m.goalSession
	s, ok := m.session(id)
	if !ok {
		m.GoalOpen.Set(false)
		return
	}
	objective := strings.TrimSpace(ggui.Peek(m.GoalText))
	if objective == "" {
		m.GoalErr.Set(T("Goal objective cannot be empty"))
		return
	}
	action := protocol.GoalActionSet
	if s.GetGoal().Active() {
		action = protocol.GoalActionEdit
	}
	req := &protocol.GoalAction{
		NodeId:    m.remoteID(s.GetNodeId()),
		SessionId: id,
		Action:    action,
		Objective: objective,
	}
	ggui.Async(func() (*protocol.SessionInfo, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		c, err := m.link.client(ctx)
		if err != nil {
			return nil, err
		}
		return c.GoalAction(ctx, req)
	}, func(info *protocol.SessionInfo, err error) {
		if err != nil {
			m.GoalErr.Set(err.Error())
			return
		}
		m.GoalOpen.Set(false)
		m.editSession(id, func(cur *protocol.SessionInfo) { cur.Goal = info.GetGoal() })
		m.link.pollNow()
	})
}

// clearGoal clears the active goal of session id.
func (m *model) clearGoal(id string) {
	info, ok := m.session(id)
	if !ok {
		return
	}
	req := &protocol.GoalAction{
		NodeId:    m.remoteID(info.GetNodeId()),
		SessionId: id,
		Action:    protocol.GoalActionClear,
	}
	fetch(m, 15*time.Second, func(ctx context.Context, c *client.Client) (*protocol.SessionInfo, error) {
		return c.GoalAction(ctx, req)
	}, func(updated *protocol.SessionInfo, err error) {
		if err != nil {
			m.connErr(err)
			return
		}
		m.editSession(id, func(cur *protocol.SessionInfo) { cur.Goal = updated.GetGoal() })
		m.link.pollNow()
	})
}

func goalDialog(m *model) ggui.Widget {
	return nameDialog(nameAsk{
		Open: m.GoalOpen, Text: m.GoalText, Err: m.GoalErr,
		Title: T("Edit goal"), Label: T("Goal"), Field: T("Goal objective"), Action: T("Save"),
		Placeholder: T("Describe the objective to work toward"), Submit: m.submitGoal,
	})
}
