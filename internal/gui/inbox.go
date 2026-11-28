package gui

import (
	"cmp"
	"fmt"
	"image/color"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// sidebarPageInbox is the built-in SidebarPage ID for the desktop Needs You
// triage view: one command center across every node and space for pending
// approvals, questions, auth errors, waiting agents, and unread turns.
const sidebarPageInbox = "builtin:inbox"

type inboxKind int

const (
	inboxApproval inboxKind = iota
	inboxAuthError
	inboxWaiting
	inboxUnread
)

type inboxFilter string

const (
	inboxFilterAll       inboxFilter = "all"
	inboxFilterApprovals inboxFilter = "approvals"
	inboxFilterWaiting   inboxFilter = "waiting"
	inboxFilterUnread    inboxFilter = "unread"
)

type inboxItem struct {
	Session *protocol.SessionInfo
	Kind    inboxKind
}

// collectInboxItems gathers every session that needs attention or has an
// unread finished turn, ordered by urgency (Approvals → Auth Errors → Waiting →
// Unread) and then by most recent activity.
func collectInboxItems(sessions []*protocol.SessionInfo, unread map[string]bool) []inboxItem {
	var out []inboxItem
	for _, s := range sessions {
		if s == nil {
			continue
		}
		var kind inboxKind
		switch {
		case s.GetState() == protocol.StateRunning && s.GetApproval() != nil:
			kind = inboxApproval
		case s.GetState() == protocol.StateRunning && s.GetAuthError() != nil:
			kind = inboxAuthError
		case s.GetState() == protocol.StateRunning && (s.GetDialog() != nil || s.GetStatus() == protocol.AgentWaiting):
			kind = inboxWaiting
		case unread[s.GetId()]:
			kind = inboxUnread
		default:
			continue
		}
		out = append(out, inboxItem{Session: s, Kind: kind})
	}
	slices.SortStableFunc(out, func(a, b inboxItem) int {
		if a.Kind != b.Kind {
			return cmp.Compare(a.Kind, b.Kind)
		}
		at := protocol.TimeOf(a.Session.GetStatusAt()).UnixNano()
		bt := protocol.TimeOf(b.Session.GetStatusAt()).UnixNano()
		if at != bt {
			return cmp.Compare(bt, at)
		}
		return cmp.Compare(a.Session.GetId(), b.Session.GetId())
	})
	return out
}

func filterInboxItems(items []inboxItem, f inboxFilter) []inboxItem {
	if f == inboxFilterAll {
		return items
	}
	var out []inboxItem
	for _, it := range items {
		switch f {
		case inboxFilterApprovals:
			if it.Kind == inboxApproval {
				out = append(out, it)
			}
		case inboxFilterWaiting:
			if it.Kind == inboxAuthError || it.Kind == inboxWaiting {
				out = append(out, it)
			}
		case inboxFilterUnread:
			if it.Kind == inboxUnread {
				out = append(out, it)
			}
		}
	}
	return out
}

// isBatchSafeApproval reports whether approval a is a standard tool permission
// request that can be batch-approved from the inbox. Questions, plans, layout
// changes, and templates require reading and are excluded from batch approve.
func isBatchSafeApproval(a *protocol.Approval) bool {
	if a == nil || len(a.GetQuestions()) > 0 {
		return false
	}
	switch a.GetKind() {
	case protocol.ToolExitPlan, protocol.ToolLayout, protocol.ToolTemplate:
		return false
	}
	return true
}

func batchApprovableItems(items []inboxItem) []inboxItem {
	var out []inboxItem
	for _, it := range items {
		if it.Kind == inboxApproval && isBatchSafeApproval(it.Session.GetApproval()) {
			out = append(out, it)
		}
	}
	return out
}

func (m *model) openInbox() {
	m.openSidebarPage(sidebarPageInbox)
}

func (m *model) toggleInbox() {
	if m.SidebarPage != nil && ggui.Peek(m.SidebarPage) == sidebarPageInbox && ggui.Peek(m.Page) == "workspace" {
		m.clearSidebarPage()
		return
	}
	m.openInbox()
}

// markSessionSeen clears the unread badge of session id without switching tabs.
func (m *model) markSessionSeen(id string) {
	unread := ggui.Peek(m.Unread)
	if unread[id] {
		next := maps.Clone(unread)
		delete(next, id)
		m.Unread.Set(next)
	}
	if s, ok := m.session(id); ok && numbered(s) {
		m.see(s)
	}
}

// markAllUnreadSeen clears all unread-only items in the inbox.
func (m *model) markAllUnreadSeen() {
	items := collectInboxItems(ggui.Peek(m.Sessions), ggui.Peek(m.Unread))
	unread := maps.Clone(ggui.Peek(m.Unread))
	changed := false
	for _, it := range items {
		if it.Kind != inboxUnread {
			continue
		}
		id := it.Session.GetId()
		if unread[id] {
			delete(unread, id)
			changed = true
		}
		if numbered(it.Session) {
			m.see(it.Session)
		}
	}
	if changed {
		m.Unread.Set(unread)
	}
}

// askBatchApproveAll confirms and approves every standard tool approval in items.
func (m *model) askBatchApproveAll(items []inboxItem) {
	targets := batchApprovableItems(items)
	if len(targets) == 0 {
		return
	}
	titles := ggui.Peek(m.Titles)
	var lines []string
	for _, it := range targets[:min(len(targets), 12)] {
		a := it.Session.GetApproval()
		detail := strings.TrimSpace(a.GetTool() + " " + firstLine(a.GetDetail()))
		lines = append(lines, "• "+sessionTitle(it.Session, titles[it.Session.GetId()])+" — "+detail)
	}
	if len(targets) > 12 {
		lines = append(lines, Tf("and %d more", len(targets)-12))
	}
	m.askConfirm(confirmation{
		Title:  Tf("Allow %d pending tool approvals?", len(targets)),
		Body:   strings.Join(lines, "\n"),
		Action: T("Allow all"),
		Safe:   true,
		Run: func() {
			for _, it := range targets {
				a := it.Session.GetApproval()
				if a == nil {
					continue
				}
				m.answerApproval(it.Session.GetId(), &protocol.AnswerApproval{
					ApprovalId: a.GetId(),
					Decision:   protocol.DecisionAllow,
				})
			}
		},
	})
}

// inboxSidebarRow is the always-available Needs You entry above Projects in
// the left sidebar, showing live urgent/unread counts across every space.
func inboxSidebarRow(m *model, active *ggui.StateValue[string]) ggui.Widget {
	return ggui.Reactive(func() ggui.Widget {
		items := collectInboxItems(m.Sessions.Get(), m.Unread.Get())
		urgent := 0
		for _, it := range items {
			if it.Kind != inboxUnread {
				urgent++
			}
		}
		total := len(items)
		selected := active != nil && active.Get() == sidebarPageInbox
		iconCol := pick[color.Color](selected, skin.AccentBright, pick[color.Color](urgent > 0, skin.DotWaiting, skin.Muted))
		textCol := pick[color.Color](selected, skin.Fg, skin.Muted)
		weight := pick(selected, ggui.WeightSemibold, ggui.WeightMedium)

		cols := []ggui.Widget{
			icon("shield-alert").Size(14).Color(iconCol),
			widget.Expanded(widget.Text(T("Needs you")).Size(13).Weight(weight).Color(textCol).NoWrap().Ellipsis()),
		}
		if urgent > 0 {
			cols = append(cols, widget.Box(widget.Text(strconv.Itoa(urgent)).Size(11).Weight(ggui.WeightSemibold).Color(skin.DotWaiting).NoWrap()).
				Fill(skin.DotWaiting.Alpha(0.16)).Radius(9).Pad(1, 7))
		} else if total > 0 {
			cols = append(cols, widget.Box(widget.Text(strconv.Itoa(total)).Size(11).Weight(ggui.WeightMedium).Color(skin.AccentBright).NoWrap()).
				Fill(skin.AccentBright.Alpha(0.14)).Radius(9).Pad(1, 7))
		}
		item := ui.ButtonOf(widget.Row(cols...).Gap(9).Align(widget.AlignCenter), func() {
			if active == nil {
				return
			}
			if ggui.Peek(active) == sidebarPageInbox {
				active.Set("")
			} else {
				if m.Page != nil && ggui.Peek(m.Page) != "workspace" {
					m.Page.Set("workspace")
				}
				active.Set(sidebarPageInbox)
			}
		}).Ghost().Pad(6, 10).Radius(7).Name(T("Needs you"))
		if selected {
			return widget.Box(item).Fill(skin.SidebarSel).Radius(7)
		}
		return item
	})
}

func inboxHeaderBar(m *model, onClose func()) ggui.Widget {
	return ggui.Reactive(func() ggui.Widget {
		items := collectInboxItems(m.Sessions.Get(), m.Unread.Get())
		urgent := 0
		for _, it := range items {
			if it.Kind != inboxUnread {
				urgent++
			}
		}
		row := []ggui.Widget{
			iconButton("arrow-left", T("Back to workspace"), onClose),
			icon("shield-alert").Size(14).Color(pick[color.Color](urgent > 0, skin.DotWaiting, skin.AccentBright)),
			widget.Text(T("Needs you")).Size(13.5).Weight(ggui.WeightSemibold).NoWrap(),
		}
		if len(items) > 0 {
			row = append(row, muted(widget.Text(Tf("%d items across all nodes", len(items))).Size(12.5).NoWrap()))
		}
		row = append(row, widget.Spacer())
		return widget.Row(row...).Gap(8).Align(widget.AlignCenter)
	})
}

func inboxView(m *model) ggui.Widget {
	filter := ggui.State(inboxFilterAll)
	return ggui.Reactive(func() ggui.Widget {
		allItems := collectInboxItems(m.Sessions.Get(), m.Unread.Get())
		curFilter := filter.Get()
		shown := filterInboxItems(allItems, curFilter)
		batchable := batchApprovableItems(allItems)

		var nApprovals, nWaiting, nUnread int
		for _, it := range allItems {
			switch it.Kind {
			case inboxApproval:
				nApprovals++
			case inboxAuthError, inboxWaiting:
				nWaiting++
			case inboxUnread:
				nUnread++
			}
		}

		pill := func(f inboxFilter, label string, count int) ggui.Widget {
			on := curFilter == f
			text := label
			if count > 0 {
				text = fmt.Sprintf("%s (%d)", label, count)
			}
			b := ui.Button(text, func() { filter.Set(f) }).Pad(4, 10).Radius(8)
			if !on {
				b = b.Ghost()
			}
			return b
		}

		toolbarLeft := widget.Row(
			pill(inboxFilterAll, T("All"), len(allItems)),
			pill(inboxFilterApprovals, T("Approvals"), nApprovals),
			pill(inboxFilterWaiting, T("Waiting"), nWaiting),
			pill(inboxFilterUnread, T("Unread"), nUnread),
		).Gap(6).Align(widget.AlignCenter)

		var actions []ggui.Widget
		if len(batchable) > 0 {
			actions = append(actions, ui.Button(Tf("Allow all approvals (%d)", len(batchable)), func() {
				m.askBatchApproveAll(allItems)
			}).Pad(4, 10).Radius(8))
		}
		if nUnread > 0 {
			actions = append(actions, ui.Button(T("Mark unread seen"), m.markAllUnreadSeen).Outline().Pad(4, 10).Radius(8))
		}
		toolbarRow := []ggui.Widget{toolbarLeft, widget.Spacer()}
		toolbarRow = append(toolbarRow, actions...)
		toolbar := widget.Column(
			widget.Box(widget.Row(toolbarRow...).Gap(8).Align(widget.AlignCenter)).Pad(8, 16).Fill(skin.Surface1),
			hline(),
		).Stretch()

		var body ggui.Widget
		if len(shown) == 0 {
			msg := T("All caught up — no agents need your attention right now.")
			if len(allItems) > 0 {
				msg = T("No items match this filter.")
			}
			body = widget.Center(widget.Column(
				icon("circle-check").Size(28).Color(skin.DotRunning),
				widget.Text(msg).Size(14).Weight(ggui.WeightMedium).Color(skin.Muted),
			).Gap(10).Align(widget.AlignCenter))
		} else {
			nodes := m.Nodes.Get()
			titles := m.Titles.Get()
			cards := make([]ggui.Widget, 0, len(shown))
			for _, it := range shown {
				cards = append(cards, inboxCard(m, it, nodes, titles))
			}
			body = widget.Scroll(widget.Box(widget.Column(cards...).Gap(12).Stretch()).Pad(16, 20)).Bar(skin.Surface3)
		}
		return widget.Box(widget.Column(toolbar, widget.Expanded(body)).Stretch()).Fill(skin.Surface0)
	})
}

func inboxCard(m *model, it inboxItem, nodes []*protocol.Node, titles map[string]string) ggui.Widget {
	s := it.Session
	id := s.GetId()
	title := sessionTitle(s, titles[id])
	proj := projectName(s.GetProject())
	host := nodeName(nodes, s.GetNodeId())
	target := agentspec.Target(s.GetAgent(), s.GetProfile())

	borderCol := skin.Border
	badgeText := T("Unread")
	badgeCol := skin.AccentBright
	switch it.Kind {
	case inboxApproval:
		borderCol = skin.DotWaiting
		badgeText = T("Approval")
		badgeCol = skin.DotWaiting
	case inboxAuthError:
		borderCol = skin.DotDown
		badgeText = T("Auth error")
		badgeCol = skin.DotDown
	case inboxWaiting:
		borderCol = skin.DotWaiting
		badgeText = T("Waiting")
		badgeCol = skin.DotWaiting
	}

	openSession := func() {
		m.clearSidebarPage()
		m.selectSession(id)
	}
	openChat := func() {
		m.clearSidebarPage()
		m.selectSession(id)
		m.setChatMode(id, true)
	}

	metaParts := []string{proj, host, target}
	if br := cmp.Or(s.GetWorktree().GetBranch(), s.GetBranch()); br != "" {
		metaParts = append(metaParts, br)
	}
	metaLine := strings.Join(slices.DeleteFunc(metaParts, func(v string) bool { return strings.TrimSpace(v) == "" }), " · ")

	header := widget.Row(
		agentGlyph(s.GetAgent(), skin.Muted).Size(15),
		widget.Text(title).Size(14).Weight(ggui.WeightSemibold).NoWrap().Ellipsis(),
		widget.Box(widget.Text(badgeText).Size(11).Weight(ggui.WeightMedium).Color(badgeCol).NoWrap()).
			Fill(badgeCol.Alpha(0.14)).Radius(6).Pad(2, 7),
		widget.Spacer(),
		ui.Button(T("Open session"), openSession).Ghost().Pad(4, 8).Radius(6),
	).Gap(8).Align(widget.AlignCenter)

	rows := []ggui.Widget{
		header,
		muted(widget.Text(metaLine).Size(12).NoWrap().Ellipsis()),
	}

	switch it.Kind {
	case inboxApproval:
		a := s.GetApproval()
		if len(a.GetQuestions()) > 0 {
			rows = append(rows,
				widget.Text(a.GetQuestions()[0].GetQuestion()).Size(13).Selectable(),
				widget.Wrap(
					ui.Button(T("Answer in Chat"), openChat),
					inTerminalButton(m.decide(id, a, protocol.DecisionTerminal)),
					ui.Button(T("Decline"), m.decide(id, a, protocol.DecisionDeny)).Outline(),
				).Gap(8).RunGap(8),
			)
		} else if a.GetKind() == protocol.ToolExitPlan {
			rows = append(rows,
				widget.Text(T("Approve the plan?")).Size(13).Weight(ggui.WeightMedium),
				widget.Wrap(
					ui.Button(T("Review in Chat"), openChat),
					inTerminalButton(m.decide(id, a, protocol.DecisionTerminal)),
				).Gap(8).RunGap(8),
			)
		} else if a.GetKind() == protocol.ToolTemplate {
			rows = append(rows,
				widget.Text(T("Save a template?")).Size(13).Weight(ggui.WeightMedium),
				widget.Wrap(
					ui.Button(T("Review in Chat"), openChat),
					ui.Button(T("Decline"), m.decide(id, a, protocol.DecisionDeny)).Outline(),
				).Gap(8).RunGap(8),
			)
		} else if a.GetKind() == protocol.ToolLayout {
			rows = append(rows,
				widget.Text(T("Allow this change of the window layout?")).Size(13).Weight(ggui.WeightMedium),
				muted(widget.Text(a.GetDetail()).Size(12.5)),
				widget.Wrap(layoutApprovalButtons(m, id, a.GetId())...).Gap(8).RunGap(8),
			)
		} else {
			rows = append(rows, widget.Text(Tf("Allow %s?", a.GetTool())).Size(13).Weight(ggui.WeightMedium))
			if body := approvalBody(m, a); body != nil {
				rows = append(rows, widget.Box(body).MaxHeight(200))
			} else if a.GetDetail() != "" {
				rows = append(rows, toolBlock(a.GetDetail(), false))
			}
			why := ggui.State("")
			allow := func(option string) func() {
				return func() {
					m.answerApproval(id, &protocol.AnswerApproval{ApprovalId: a.GetId(), Decision: protocol.DecisionAllow, Option: option})
				}
			}
			deny := func() { denyWith(m, id, a, ggui.Peek(why)) }
			btns := []ggui.Widget{ui.Button(T("Allow"), allow(""))}
			for _, o := range a.GetOptions() {
				btns = append(btns, ui.Button(optionLabel(o), allow(o.GetId())).Outline())
			}
			btns = append(btns, inTerminalButton(m.decide(id, a, protocol.DecisionTerminal)))
			rows = append(rows,
				widget.Wrap(btns...).Gap(8).RunGap(8),
				widget.Row(
					widget.Expanded(ui.TextField(why).Placeholder(T("Tell the agent what to do instead")).Name(T("Why not")).OnSubmit(func(string) { deny() })),
					ui.Button(T("Deny"), deny).Outline(),
				).Gap(8).Align(widget.AlignCenter),
			)
		}
	case inboxAuthError:
		ae := s.GetAuthError()
		rows = append(rows, widget.Text(cmp.Or(ae.GetSummary(), T("Provider authentication failed"))).Size(13).Color(skin.DotDown).Selectable())
		if g := strings.TrimSpace(ae.GetGuidance()); g != "" {
			rows = append(rows, muted(widget.Text(g).Size(12).Selectable()))
		}
	case inboxWaiting:
		rows = append(rows, muted(widget.Text(T("Waiting for your answer")).Size(13)))
		if m.conns[id] != nil {
			reply := ggui.State("")
			send := func() {
				txt := strings.TrimSpace(ggui.Peek(reply))
				if txt == "" {
					return
				}
				state := m.chatState(id)
				m.sendPrompt(id, state, m.composerDraft(id), txt)
				reply.Set("")
				m.markSessionSeen(id)
			}
			rows = append(rows, widget.Row(
				widget.Expanded(ui.TextField(reply).Placeholder(T("Send a quick reply…")).Name(T("Quick reply")).OnSubmit(func(string) { send() })),
				ui.Button(T("Send"), send),
			).Gap(8).Align(widget.AlignCenter))
		}
	case inboxUnread:
		rows = append(rows,
			muted(widget.Text(T("Finished its turn")).Size(13)),
			widget.Row(
				ui.Button(T("Review in Chat"), openChat).Outline(),
				ui.Button(T("Dismiss"), func() { m.markSessionSeen(id) }).Ghost(),
			).Gap(8).Align(widget.AlignCenter),
		)
	}

	return widget.Box(widget.Column(rows...).Gap(8).Stretch()).
		Fill(skin.Surface1).Border(1, borderCol).Radius(10).Pad(12, 14)
}
