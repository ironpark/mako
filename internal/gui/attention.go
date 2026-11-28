package gui

import (
	"cmp"
	"context"
	"maps"
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
	"github.com/ironpark/mako/internal/protocol"
)

// An agent's hooks say when it works, waits on the user, and finishes. A
// session that finishes or asks while not in view is unread: its dot turns
// blue, or amber while it waits, until it is looked at; the Dock counts the
// unread, and the desktop is told unless the settings say not to.
//
// The daemon that runs a session numbers what asks for the user
// (SessionInfo.attention_seq) and keeps what the user has looked at
// (seen_seq), which a window showing the session says with SeeSession: so
// every window and the phone show it unread the same, and none misses a
// turn ended between two of its polls. A session of an older daemon, which
// numbers nothing, is followed as before, by comparing one poll's statuses
// with the next.

// dotOf is what a session's dot shows.
func (m *model) dotOf(s *protocol.SessionInfo) dotState {
	switch {
	case s.GetState() == protocol.StateStopped && m.Unread.Get()[s.GetId()]:
		return dotDone // put to sleep before it was looked at
	case s.GetState() == protocol.StateExited || s.GetState() == protocol.StateStopped:
		return dotRest
	case s.GetAuthError() != nil:
		return dotError
	case s.GetApproval() != nil || s.GetStatus() == protocol.AgentWaiting:
		return dotWaiting
	// Work its terminal reports, as the daemon read it: not as this
	// window's own terminal did, which a window without it would not show.
	case protocol.AtWork(s.GetStatus()) || ((s.GetStatus() == "" || s.GetStatus() == protocol.AgentIdle) && s.GetBusy()):
		return dotWorking
	case m.Unread.Get()[s.GetId()]:
		return dotDone
	}
	return dotRest
}

// dotFor follows a session's dot as the polls and the terminal change it.
func (m *model) dotFor(id string) func() dotState {
	return func() dotState {
		s, ok := m.session(id)
		if !ok {
			return dotRest
		}
		return m.dotOf(s)
	}
}

// inView reports whether the user can see a session now: the tab a pane
// shows, in the focused window, not covered by a sidebar page such as Needs You.
func (m *model) inView(id string) bool {
	if m.window != nil && !ggui.Peek(m.window.Focused()) {
		return false
	}
	if m.SidebarPage != nil && ggui.Peek(m.SidebarPage) != "" {
		return false
	}
	for _, g := range ggui.Peek(m.Layout).groups() {
		if g.Active == id {
			return ggui.Peek(m.Page) == "workspace"
		}
	}
	return false
}

// noteStatus marks what the next poll's sessions did out of view: run
// before they replace the last, which it compares them with.
func (m *model) noteStatus(next []*protocol.SessionInfo) {
	prev := ggui.Peek(m.Sessions)
	unread := ggui.Peek(m.Unread)
	changed := false
	// set marks id unread or not, on a copy of the marks taken once.
	set := func(id string, on bool) {
		if unread[id] == on {
			return
		}
		if !changed {
			unread = maps.Clone(unread)
			changed = true
		}
		if on {
			unread[id] = true
		} else {
			delete(unread, id)
		}
	}
	for _, s := range next {
		old, known := sessionIn(prev, s.GetId())
		if known && m.noteProgram(old, s) {
			set(s.GetId(), true)
		}
		if numbered(s) {
			m.noteAttention(old, known, s, func(on bool) { set(s.GetId(), on) })
			continue
		}
		if !known || !attends(s) || (old.GetStatus() == s.GetStatus() && proto.Equal(old.GetStatusAt(), s.GetStatusAt()) && old.GetApproval().GetId() == s.GetApproval().GetId() && protocol.Same(old.GetAuthError(), s.GetAuthError())) {
			continue
		}
		if m.inView(s.GetId()) {
			continue
		}
		set(s.GetId(), true)
		m.announce(s)
	}
	// A session gone takes its mark with it.
	for id := range unread {
		if _, ok := sessionIn(next, id); !ok {
			set(id, false)
		}
	}
	m.pruneInboxDrafts(next)
	if changed {
		m.Unread.Set(unread)
	}
}

// numbered reports whether the daemon running s numbers its status, which
// says then what is unread.
func numbered(s *protocol.SessionInfo) bool { return s.GetStateSeq() > 0 }

// noteAttention marks s, of a daemon that numbers its status, unread as
// that daemon says, and tells the desktop of an attention new since the
// last poll; one in view the user is seeing, which the daemon is told.
func (m *model) noteAttention(old *protocol.SessionInfo, known bool, s *protocol.SessionInfo, mark func(bool)) {
	unseen := s.GetAttentionSeq() > s.GetSeenSeq()
	switch {
	case unseen && m.inView(s.GetId()):
		m.see(s)
		mark(false)
		return
	case unseen:
		mark(true)
	case !known || s.GetSeenSeq() > old.GetSeenSeq():
		mark(false) // seen, here or in another window
	}
	if !known || !unseen {
		return
	}
	// The numbers start again with the session's program.
	before := old.GetAttentionSeq()
	if !old.GetStartedAt().AsTime().Equal(s.GetStartedAt().AsTime()) {
		before = 0
	}
	if s.GetAttentionSeq() > before {
		m.announce(s)
	}
}

// see tells the daemon running s that the user has looked at its
// attention, once for each.
func (m *model) see(s *protocol.SessionInfo) {
	seq := s.GetAttentionSeq()
	if m.post == nil || seq <= s.GetSeenSeq() || m.seeing[s.GetId()] >= seq {
		return
	}
	if m.seeing == nil {
		m.seeing = map[string]uint64{}
	}
	id := s.GetId()
	m.seeing[id] = seq
	req := &protocol.SeeSession{NodeId: m.remoteID(s.GetNodeId()), SessionId: id, Seq: seq}
	fetch(m, 10*time.Second, func(ctx context.Context, c *client.Client) (*protocol.SessionInfo, error) {
		return c.SeeSession(ctx, req)
	}, func(_ *protocol.SessionInfo, err error) {
		if err != nil && m.seeing[id] == seq {
			delete(m.seeing, id) // asked again when next seen
		}
	})
}

// attends reports whether a session's state asks for the user.
func attends(s *protocol.SessionInfo) bool {
	return s.GetAuthError() != nil || s.GetApproval() != nil || s.GetStatus() == protocol.AgentWaiting || s.GetStatus() == protocol.AgentDone
}

// seen clears the marks of the sessions in view.
func (m *model) seen() {
	unread := ggui.Peek(m.Unread)
	var drop []string
	for id := range unread {
		if m.inView(id) {
			drop = append(drop, id)
		}
	}
	if len(drop) == 0 {
		return
	}
	unread = maps.Clone(unread)
	for _, id := range drop {
		delete(unread, id)
		if s, ok := m.session(id); ok && numbered(s) {
			m.see(s)
		}
	}
	m.Unread.Set(unread)
}

// announce tells the desktop what a session out of view did.
func (m *model) announce(s *protocol.SessionInfo) {
	if (s.GetAuthError() != nil || s.GetApproval() != nil || s.GetStatus() == protocol.AgentWaiting) && m.window != nil && !ggui.Peek(m.window.Focused()) {
		m.window.RequestAttention()
	}
	if !ggui.Peek(m.Prefs).Notify {
		return
	}
	title := sessionTitle(s, ggui.Peek(m.Titles)[s.GetId()])
	var body string
	switch {
	case s.GetAuthError() != nil:
		body = cmp.Or(s.GetAuthError().GetSummary(), T("Provider authentication failed"))
	case s.GetApproval() != nil:
		body = Tf("Asks to run %s", strings.TrimSpace(s.GetApproval().GetTool()+" "+s.GetApproval().GetDetail()))
	case s.GetStatus() == protocol.AgentWaiting:
		body = T("Waiting for your answer")
	default:
		body = T("Finished its turn")
	}
	m.notify(title, projectName(s.GetProject())+" · "+body, sessionTarget(s.GetId()))
}

// noteProgram passes on what a session's program asked of the terminal
// since the last poll, out of view: a bell asks for the user's attention,
// as Ghostty's does, and a notification it posted goes to the desktop. An
// agent whose hooks mako follows is announced by mako, and its own
// notifications would say the same again. It reports whether the session
// has something new to read.
func (m *model) noteProgram(old, s *protocol.SessionInfo) bool {
	rang := s.GetBells() > old.GetBells()
	posted := s.GetNotice().GetSeq() > old.GetNotice().GetSeq() && s.GetCaps()&protocol.CapHooks == 0
	if (!rang && !posted) || m.inView(s.GetId()) {
		return false
	}
	if m.window != nil && !ggui.Peek(m.window.Focused()) {
		m.window.RequestAttention()
	}
	if !posted {
		return false
	}
	if ggui.Peek(m.Prefs).Notify {
		n := s.GetNotice()
		title := cmp.Or(n.GetTitle(), sessionTitle(s, ggui.Peek(m.Titles)[s.GetId()]))
		m.notify(title, projectName(s.GetProject())+" · "+n.GetBody(), sessionTarget(s.GetId()))
	}
	return true
}

// sessionTarget and settingsTarget name what a notification is about, for
// reveal to show when it is clicked.
func sessionTarget(id string) string       { return "session:" + id }
func settingsTarget(section string) string { return "settings:" + section }

// reveal brings the window forward onto what a notification the user
// clicked was about: a session, or a section of the settings.
func (m *model) reveal(target string) {
	m.showWindow()
	if section, ok := strings.CutPrefix(target, "settings:"); ok {
		m.openSettings(section)
	} else if run, ok := strings.CutPrefix(target, "forge:"); ok {
		m.revealForge(run)
	} else if id, ok := strings.CutPrefix(target, "session:"); ok {
		m.Page.Set("workspace")
		m.selectSession(id)
	}
}

// followAttention keeps the Dock's count and the marks in step with what is
// in view: it runs under the window's owner.
func followAttention(m *model) {
	ggui.Effect(func() ggui.Cleanup {
		m.Selected.Get()
		m.Layout.Get()
		m.Page.Get()
		if m.SidebarPage != nil {
			m.SidebarPage.Get()
		}
		if m.window != nil {
			m.window.Focused().Get()
		}
		ggui.Untrack(func() struct{} { m.seen(); return struct{}{} })
		return nil
	})
	ggui.Effect(func() ggui.Cleanup {
		n := 0
		for _, s := range m.Sessions.Get() {
			if m.Unread.Get()[s.GetId()] || s.GetApproval() != nil || s.GetAuthError() != nil {
				n++
			}
		}
		label := ""
		if n > 0 {
			label = strconv.Itoa(n)
		}
		setBadge(label)
		m.setTrayTitle(label)
		return nil
	})
}

// asksNow reports whether a session waits on the user for an answer now:
// an approval, a question, a dialog in its terminal.
func asksNow(s *protocol.SessionInfo) bool {
	if s.GetState() != protocol.StateRunning {
		return false
	}
	return s.GetAuthError() != nil || s.GetApproval() != nil || s.GetDialog() != nil || s.GetStatus() == protocol.AgentWaiting
}

// waitingCount is how many sessions wait on the user.
func (m *model) waitingCount() int {
	n := 0
	for _, s := range m.Sessions.Get() {
		if asksNow(s) {
			n++
		}
	}
	return n
}

// nextWaiting shows the next session that waits on the user, after the one
// selected, round to the first: answered one after another, each brings the
// next.
func (m *model) nextWaiting() {
	sessions := ggui.Peek(m.Sessions)
	at := slices.IndexFunc(sessions, func(s *protocol.SessionInfo) bool { return s.GetId() == ggui.Peek(m.Selected) })
	for i := 1; i <= len(sessions); i++ {
		if s := sessions[(at+i+len(sessions))%len(sessions)]; asksNow(s) && s.GetId() != ggui.Peek(m.Selected) {
			m.selectSession(s.GetId())
			return
		}
	}
}

// waitingPill counts the sessions that wait on the user, in the sidebar's
// heading, and goes to the next of them; nothing while none waits.
func waitingPill(m *model) ggui.Widget {
	return ggui.View(ggui.Derived(m.waitingCount), func(n int) ggui.Widget {
		if n == 0 {
			return widget.Box()
		}
		tip := Tf("%d waiting for you — go to the next (%s)", n, chordLabel("cmd+shift+a"))
		face := widget.Row(dot(skin.DotWaiting), widget.Text(strconv.Itoa(n)).Size(12).Weight(ggui.WeightMedium).Color(skin.DotWaiting)).Gap(5).Align(widget.AlignCenter)
		return arrive(ui.Tooltip(ui.ButtonOf(face, m.nextWaiting).Ghost().Pad(3, 7).Radius(9).Name(T("Next waiting session")), tip))
	})
}

// answerSelected answers the approval the selected session waits on, from
// the keyboard: a call allowed or denied. A plan, questions and a dialog
// want reading, and are answered where they show.
func (m *model) answerSelected(decision string) {
	id := ggui.Peek(m.Selected)
	s, ok := m.session(id)
	a := s.GetApproval()
	if !ok || a == nil || len(a.GetQuestions()) > 0 || a.GetKind() == protocol.ToolExitPlan {
		return
	}
	if a.GetKind() == protocol.ToolTemplate && decision == protocol.DecisionAllow {
		// Allowed, it is this window's to save.
		if r := readTemplateRequest(a); r.err == nil && !r.taken() {
			m.saveTemplateApproval(id, a)
		}
		return
	}
	m.decide(id, a, decision)()
}
