package gui

import (
	"cmp"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/notify"
	"github.com/ironpark/mako/internal/protocol"
)

// An agent's hooks say when it works, waits on the user, and finishes. A
// session that finishes or asks while not in view is unread: its dot turns
// blue, or amber while it waits, until it is looked at; the Dock counts the
// unread, and the desktop is told unless the settings say not to.

// dotOf is what a session's dot shows.
func (m *model) dotOf(s *protocol.SessionInfo) dotState {
	switch {
	case s.GetState() == protocol.StateExited || s.GetState() == protocol.StateStopped:
		return dotRest
	case s.GetApproval() != nil || s.GetStatus() == protocol.AgentWaiting:
		return dotWaiting
	case protocol.AtWork(s.GetStatus()) || ((s.GetStatus() == "" || s.GetStatus() == protocol.AgentIdle) && m.busy(s.GetId())):
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
// shows, in the focused window.
func (m *model) inView(id string) bool {
	if m.window != nil && !ggui.Peek(m.window.Focused()) {
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
	for _, s := range next {
		old, known := sessionIn(prev, s.GetId())
		if known && m.noteProgram(old, s) && !unread[s.GetId()] {
			if !changed {
				unread = maps.Clone(unread)
				changed = true
			}
			unread[s.GetId()] = true
		}
		if !known || !attends(s) || (old.GetStatus() == s.GetStatus() && proto.Equal(old.GetStatusAt(), s.GetStatusAt()) && old.GetApproval().GetId() == s.GetApproval().GetId()) {
			continue
		}
		if m.inView(s.GetId()) {
			continue
		}
		if !unread[s.GetId()] {
			if !changed {
				unread = maps.Clone(unread)
				changed = true
			}
			unread[s.GetId()] = true
		}
		m.announce(s)
	}
	// A session gone takes its mark with it.
	for id := range unread {
		if _, ok := sessionIn(next, id); !ok {
			if !changed {
				unread = maps.Clone(unread)
				changed = true
			}
			delete(unread, id)
		}
	}
	if changed {
		m.Unread.Set(unread)
	}
}

// attends reports whether a session's state asks for the user.
func attends(s *protocol.SessionInfo) bool {
	return s.GetApproval() != nil || s.GetStatus() == protocol.AgentWaiting || s.GetStatus() == protocol.AgentDone
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
	}
	m.Unread.Set(unread)
}

// announce tells the desktop what a session out of view did.
func (m *model) announce(s *protocol.SessionInfo) {
	if (s.GetApproval() != nil || s.GetStatus() == protocol.AgentWaiting) && m.window != nil && !ggui.Peek(m.window.Focused()) {
		m.window.RequestAttention()
	}
	if !ggui.Peek(m.Prefs).Notify {
		return
	}
	title := sessionTitle(s, ggui.Peek(m.Titles)[s.GetId()])
	var body string
	switch {
	case s.GetApproval() != nil:
		body = Tf("Asks to run %s", strings.TrimSpace(s.GetApproval().GetTool()+" "+s.GetApproval().GetDetail()))
	case s.GetStatus() == protocol.AgentWaiting:
		body = T("Waiting for your answer")
	default:
		body = T("Finished its turn")
	}
	go notify.Post(title, projectName(s.GetProject())+" · "+body, sessionTarget(s.GetId()))
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
		go notify.Post(title, projectName(s.GetProject())+" · "+n.GetBody(), sessionTarget(s.GetId()))
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
		if m.window != nil {
			m.window.Focused().Get()
		}
		ggui.Untrack(func() struct{} { m.seen(); return struct{}{} })
		return nil
	})
	ggui.Effect(func() ggui.Cleanup {
		n := 0
		for _, s := range m.Sessions.Get() {
			if m.Unread.Get()[s.GetId()] || s.GetApproval() != nil {
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
	return s.GetApproval() != nil || s.GetDialog() != nil || s.GetStatus() == protocol.AgentWaiting
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
			return ggui.Box()
		}
		tip := Tf("%d waiting for you — go to the next (%s)", n, chordLabel("cmd+shift+a"))
		face := ggui.Row(dot(skin.DotWaiting), ggui.Text(strconv.Itoa(n)).Size(12).Weight(ggui.WeightMedium).Color(skin.DotWaiting)).Gap(5).Align(ggui.AlignCenter)
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
	m.decide(id, a, decision)()
}
