package gui

import (
	"cmp"
	"context"
	"fmt"
	"image/color"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/protocol"
)

type projectAgentsView struct {
	Root            fileRoot
	Data            *protocol.ProjectAgents
	Err             string
	Loading, Loaded bool
	Updated         time.Time
}

func (m *model) loadProjectAgents(root fileRoot) {
	if root.Path == "" {
		return
	}
	v := ggui.Peek(m.ProjectAgents)
	if v.Root == root && v.Loading {
		return
	}
	if v.Root != root {
		v = projectAgentsView{Root: root}
	}
	m.projectAgentsGen++
	gen := m.projectAgentsGen
	self := ggui.Peek(m.Conn).Self
	var node *protocol.Node
	if root.Node == "" || root.Node == self.GetId() {
		node = self
	} else {
		for _, n := range ggui.Peek(m.Nodes) {
			if n.GetId() == root.Node {
				node = n
				break
			}
		}
	}
	if node != nil && node.GetRevision() < protocol.RevisionProjectAgents {
		v.Err, v.Loading, v.Loaded = T("Update this node's daemon to monitor project agents."), false, true
		m.ProjectAgents.Set(v)
		return
	}
	v.Loading = true
	m.ProjectAgents.Set(v)
	req := &protocol.ReadProjectAgents{NodeId: m.remoteID(root.Node), Path: root.Path}
	fetch(m, 10*time.Second, func(ctx context.Context, c *client.Client) (*protocol.ProjectAgents, error) {
		return c.ProjectAgents(ctx, req)
	}, func(data *protocol.ProjectAgents, err error) {
		if gen != m.projectAgentsGen || ggui.Peek(m.ProjectAgents).Root != root {
			return
		}
		next := ggui.Peek(m.ProjectAgents)
		next.Loading, next.Loaded, next.Err = false, true, ""
		if err != nil {
			next.Err = err.Error()
		} else {
			next.Data, next.Updated = data, time.Now()
		}
		m.ProjectAgents.Set(next)
	})
}

func projectAgentsPanel(m *model) ggui.Widget {
	root := ggui.Derived(m.filesRoot)
	pollWhileShown(m, root, agentsTab, 2*time.Second, m.loadProjectAgents)
	return ggui.Key(root, func(r fileRoot) ggui.Widget {
		if r.Path == "" {
			return ggui.Box(muted(ggui.Text(T("Open a session to monitor its project agents.")).Size(13))).Pad(16)
		}
		return projectAgentsBody(m, r)
	})
}

// projectAgentsBody is the project's agents above, the messages they sent
// each other below, the divider between them moved as the user likes: a
// heading line each, a line an agent, two or so a message.
func projectAgentsBody(m *model, root fileRoot) ggui.Widget {
	filter := ggui.State("")
	expanded := ggui.State(uint64(0))
	data := ggui.Map(m.ProjectAgents, func(v projectAgentsView) *protocol.ProjectAgents {
		if v.Root != root {
			return nil
		}
		return v.Data
	}).WithEqual(protocol.Same)
	header := ggui.Reactive(func() ggui.Widget {
		v := m.ProjectAgents.Get()
		meta := T("Loading agents…")
		if !v.Updated.IsZero() {
			meta = Tf("Updated %s", v.Updated.Local().Format("15:04:05"))
		}
		return ggui.Box(ggui.Row(
			icon("users").Size(14).Color(tokMuted),
			ggui.Expanded(ggui.Text(projectName(root.Path)).Size(12.5).Weight(ggui.WeightMedium).NoWrap().Ellipsis()),
			ui.Tooltip(iconButton("refresh-cw", T("Refresh agents"), func() { m.loadProjectAgents(root) }), meta),
		).Gap(7).Align(ggui.AlignCenter)).Pad(4, 6, 4, 12)
	})
	status := ggui.Reactive(func() ggui.Widget {
		v := m.ProjectAgents.Get()
		if v.Root != root || v.Err == "" {
			return ggui.Box()
		}
		return ggui.Box(ggui.Text(v.Err).Size(12).Color(tokDotWaiting).Selectable()).Pad(6, 12)
	})
	agents := ggui.Reactive(func() ggui.Widget {
		snapshot := data.Get()
		if snapshot == nil {
			v := m.ProjectAgents.Get()
			if !v.Loaded && v.Err == "" {
				return ggui.Box(muted(ggui.Text(T("Loading agents…")).Size(12))).Pad(12)
			}
			return ggui.Box()
		}
		chosen := filter.Get()
		byID := projectAgentsByID(snapshot)
		var rows []ggui.Widget
		for _, row := range projectAgentRows(snapshot.GetAgents()) {
			rows = append(rows, projectAgentRow(m, row.Agent, row.Depth, chosen, filter, byID))
		}
		if len(rows) == 0 {
			rows = append(rows, ggui.Box(muted(ggui.Text(T("No agents in this project.")).Size(12))).Pad(8, 12))
		}
		return ggui.Column(rows...).Stretch()
	})
	agentsHead := ggui.Reactive(func() ggui.Widget {
		working, waiting, pending := projectAgentCounts(data.Get())
		var clear ggui.Widget = ggui.Box()
		if filter.Get() != "" {
			clear = ui.Button(T("All agents"), func() { filter.Set("") }).Ghost().Pad(1, 6).Name(T("All agents"))
		}
		return panelHead(T("Agents"),
			ggui.Text(Tf("%d working · %d waiting · %d pending", working, waiting, pending)).Size(11).Color(tokExtraMuted).NoWrap().Ellipsis(),
			clear)
	})
	messages := ggui.Reactive(func() ggui.Widget {
		snapshot := data.Get()
		if snapshot == nil {
			return ggui.Box()
		}
		byID := projectAgentsByID(snapshot)
		found := projectAgentMessages(snapshot.GetMessages(), filter.Get())
		rows := make([]ggui.Widget, 0, len(found))
		for i := len(found) - 1; i >= 0; i-- {
			rows = append(rows, projectAgentMessage(found[i], byID, expanded))
		}
		if len(rows) == 0 {
			rows = append(rows, ggui.Box(muted(ggui.Text(T("No messages yet.")).Size(12))).Pad(8, 12))
		}
		return ggui.Column(rows...).Stretch()
	})
	messagesHead := ggui.Reactive(func() ggui.Widget {
		title := T("Recent messages")
		if a := projectAgentsByID(data.Get())[filter.Get()]; a != nil {
			title = Tf("Messages for %s", projectAgentName(a.GetSession()))
		}
		return panelHead(title, ggui.Box(), ggui.Box())
	})
	top := ggui.Column(agentsHead, ggui.Expanded(ggui.Scroll(agents).Bar(tokSurface3))).Stretch()
	bottom := ggui.Column(messagesHead, ggui.Expanded(ggui.Scroll(messages).Bar(tokSurface3))).Stretch()
	split := ui.Resizable(m.AgentsSplit, top, bottom).Vertical().MinSizes(60, 80).Hairline().Name(T("Messages divider"))
	return ggui.Column(header, hline(), status, ggui.Expanded(split)).Stretch()
}

// panelHead is a section's heading line in the agents panel: its title,
// what it says of the section, and an action at the end.
func panelHead(title string, about, action ggui.Widget) ggui.Widget {
	return ggui.Box(ggui.Row(
		ggui.Text(title).Size(11.5).Weight(ggui.WeightSemibold).NoWrap(),
		ggui.Expanded(about),
		action,
	).Gap(8).Align(ggui.AlignCenter)).Height(28).Pad(0, 8, 0, 12)
}

// projectAgentsByID indexes the snapshot's agents by their sessions.
func projectAgentsByID(snapshot *protocol.ProjectAgents) map[string]*protocol.ProjectAgent {
	byID := map[string]*protocol.ProjectAgent{}
	for _, a := range snapshot.GetAgents() {
		byID[a.GetSession().GetId()] = a
	}
	return byID
}

// projectAgentCounts counts the agents at work and waiting on the user,
// and the messages none has read yet.
func projectAgentCounts(snapshot *protocol.ProjectAgents) (working, waiting int, pending uint32) {
	for _, a := range snapshot.GetAgents() {
		s := a.GetSession()
		if s.GetState() == protocol.StateRunning {
			if protocol.AtWork(s.GetStatus()) || (s.GetStatus() == "" && s.GetBusy()) {
				working++
			}
			if s.GetStatus() == protocol.AgentWaiting || s.GetApproval() != nil {
				waiting++
			}
		}
		pending += a.GetPendingMessages()
	}
	return working, waiting, pending
}

type projectAgentRowItem struct {
	Agent *protocol.ProjectAgent
	Depth int
}

// Put children beside their parent. Orphans and malformed cycles remain
// visible rather than disappearing or recursively rebuilding the panel.
func projectAgentRows(agents []*protocol.ProjectAgent) []projectAgentRowItem {
	byID := map[string]*protocol.ProjectAgent{}
	children := map[string][]*protocol.ProjectAgent{}
	for _, a := range agents {
		byID[a.GetSession().GetId()] = a
		children[a.GetParentSessionId()] = append(children[a.GetParentSessionId()], a)
	}
	seen := map[string]bool{}
	var rows []projectAgentRowItem
	var visit func(*protocol.ProjectAgent, int)
	visit = func(a *protocol.ProjectAgent, depth int) {
		id := a.GetSession().GetId()
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		rows = append(rows, projectAgentRowItem{a, depth})
		for _, child := range children[id] {
			visit(child, depth+1)
		}
	}
	for _, a := range agents {
		if a.GetParentSessionId() == "" || byID[a.GetParentSessionId()] == nil {
			visit(a, 0)
		}
	}
	for _, a := range agents {
		visit(a, 0)
	}
	return rows
}

func projectAgentName(s *protocol.SessionInfo) string {
	return cmp.Or(s.GetName(), s.GetTitle(), sessionLabel(s), s.GetId())
}

func projectAgentStatus(s *protocol.SessionInfo) (string, color.Color) {
	if s.GetState() != protocol.StateRunning {
		return stateLabel(s.GetState()), tokExtraMuted
	}
	switch {
	case s.GetApproval() != nil || s.GetStatus() == protocol.AgentWaiting:
		return T("Waiting for you"), tokDotWaiting
	case protocol.AtWork(s.GetStatus()) || (s.GetStatus() == "" && s.GetBusy()):
		return pick(s.GetStatus() == protocol.AgentCompacting, T("Compacting conversation…"), T("Working")), tokDotRunning
	case s.GetStatus() == protocol.AgentDone:
		return T("Finished"), tokDotDone
	}
	return T("At its prompt"), tokExtraMuted
}

// projectAgentRow is an agent on one line, under the one that started it:
// its name, its pending messages and its state, and the rest of what is
// known of it in its tooltip. A tap shows its messages alone.
func projectAgentRow(m *model, a *protocol.ProjectAgent, depth int, selected string, filter *ggui.StateValue[string], byID map[string]*protocol.ProjectAgent) ggui.Widget {
	s := a.GetSession()
	name := projectAgentName(s)
	status, tint := projectAgentStatus(s)
	about := []string{status, sessionLabel(s)}
	if a.GetPendingMessages() > 0 {
		about = append(about, Tf("%d pending messages", a.GetPendingMessages()))
	}
	if !a.GetMessaging() {
		about = append(about, T("Messaging unavailable"))
	}
	if parent := a.GetParentSessionId(); parent != "" {
		label := parent
		if p := byID[parent]; p != nil {
			label = projectAgentName(p.GetSession())
		}
		about = append(about, Tf("From %s", label))
	}
	line := []ggui.Widget{
		agentGlyph(s.GetAgent(), tokMuted).Size(13),
		ggui.Expanded(ggui.Text(name).Size(12.5).NoWrap().Ellipsis()),
	}
	if n := a.GetPendingMessages(); n > 0 {
		line = append(line, ggui.Row(icon("message-square").Size(11).Color(tokDotWaiting),
			ggui.Text(fmt.Sprint(n)).Size(10.5).Color(tokDotWaiting)).Gap(2).Align(ggui.AlignCenter))
	}
	line = append(line, dot(tint))
	button := ui.ButtonOf(ggui.Row(line...).Gap(7).Align(ggui.AlignCenter),
		func() { filter.Set(pick(selected == s.GetId(), "", s.GetId())) }).Ghost().Pad(4, 6).Radius(5).Name(Tf("Filter messages for %s", name))
	var content ggui.Widget = ui.Tooltip(button, strings.Join(about, " · "))
	if selected == s.GetId() {
		content = ggui.Box(content).Radius(5).Fill(tokSurface2)
	}
	open := ui.Tooltip(ui.ButtonOf(icon("chevron-right").Size(13), func() { m.openProjectAgent(s) }).Ghost().Pad(4).Radius(5).Name(Tf("Open session %s", name)), Tf("Open session %s", name))
	return ggui.Box(ggui.Row(ggui.Expanded(content), open).Gap(2).Align(ggui.AlignCenter)).Pad(0, 6, 0, 6+float64(min(depth, 4))*14)
}

func (m *model) openProjectAgent(s *protocol.SessionInfo) {
	if _, ok := m.session(s.GetId()); !ok {
		m.Sessions.Update(func(all []*protocol.SessionInfo) []*protocol.SessionInfo {
			return append(append([]*protocol.SessionInfo(nil), all...), s)
		})
	}
	m.selectSession(s.GetId())
}

func projectAgentMessages(messages []*protocol.AgentMessage, id string) []*protocol.AgentMessage {
	if id == "" {
		return messages
	}
	var found []*protocol.AgentMessage
	for _, msg := range messages {
		if msg.GetFromSessionId() == id || msg.GetToSessionId() == id {
			found = append(found, msg)
		}
	}
	return found
}

// projectAgentMessage is a message between agents in two lines or so: who
// to whom and when, then its words, a long message cut until opened. One
// not yet acknowledged says so.
func projectAgentMessage(msg *protocol.AgentMessage, byID map[string]*protocol.ProjectAgent, expanded *ggui.StateValue[uint64]) ggui.Widget {
	label := func(id string) string {
		if a := byID[id]; a != nil {
			return projectAgentName(a.GetSession())
		}
		return id
	}
	var state string
	var tint color.Color = tokExtraMuted
	switch msg.GetState() {
	case "acknowledged":
	case "removed":
		state = T("Recipient removed")
	default:
		state, tint = T("Awaiting acknowledgement"), tokDotWaiting
	}
	long := len([]rune(msg.GetText())) > 180 || strings.Contains(msg.GetText(), "\n")
	return ggui.Reactive(func() ggui.Widget {
		open := expanded.Get() == msg.GetId()
		head := []ggui.Widget{
			ggui.Expanded(ggui.Text(label(msg.GetFromSessionId()) + " → " + label(msg.GetToSessionId())).Size(11.5).Weight(ggui.WeightMedium).NoWrap().Ellipsis()),
		}
		if state != "" {
			head = append(head, ggui.Text(state).Size(10.5).Color(tint).NoWrap())
		}
		head = append(head, ggui.Text(protocol.TimeOf(msg.GetCreatedAt()).Local().Format("15:04")).Size(10.5).Color(tokExtraMuted))
		if long {
			head = append(head, ui.ButtonOf(icon(pick(open, "chevron-up", "chevron-down")).Size(12).Color(tokMuted),
				func() { expanded.Set(pick(open, uint64(0), msg.GetId())) }).Ghost().Pad(2).Radius(4).Name(Tf("Toggle message %d", msg.GetId())))
		}
		text := ggui.Text(msg.GetText()).Size(12).Color(tokFg).Selectable()
		if !open {
			text = ggui.Text(projectMessagePreview(msg.GetText())).Size(12).Color(tokFg).MaxLines(2).Ellipsis().Selectable()
		}
		return ggui.Box(ggui.Column(ggui.Row(head...).Gap(6).Align(ggui.AlignCenter), text).Gap(3).Stretch()).Pad(6, 12)
	})
}

func projectMessagePreview(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) > 180 {
		return string(runes[:180]) + "…"
	}
	return text
}
