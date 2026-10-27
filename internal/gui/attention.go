package gui

import (
	"maps"
	"strconv"
	"strings"
	"unicode"

	"github.com/ironpark/ggui"
	"google.golang.org/protobuf/proto"

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
	case m.Unread.Get()[s.GetId()]:
		return dotDone
	case s.GetStatus() == protocol.AgentWorking || m.busy(s.GetId()):
		return dotWorking
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
	go notify(title, projectName(s.GetProject())+" · "+body)
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
		return nil
	})
}

// isSpinner reports whether r is a glyph agents animate in their titles:
// Braille dots, half circles, stars and the like, never a letter or digit.
func isSpinner(r rune) bool {
	switch {
	case r >= 0x2800 && r <= 0x28FF: // Braille
		return true
	case r >= 0x25A0 && r <= 0x25FF: // geometric shapes: ◐ ◑ ◒ ◓ ● ○
		return true
	case r >= 0x2700 && r <= 0x27BF: // dingbats: ✳ ✶ ✻ ✽ ✢
		return true
	case strings.ContainsRune("·•∙⋅*✱✲✳✴✵✶✷✸✹✺✻✼✽✾✿❀⁕⁎", r):
		return true
	}
	return false
}

// cleanTitle drops the spinner an agent leads its title with, which says
// it is at work: the dot says that, and the title stays put.
func cleanTitle(title string) string {
	return strings.TrimSpace(strings.TrimLeftFunc(title, func(r rune) bool { return isSpinner(r) || unicode.IsSpace(r) }))
}
