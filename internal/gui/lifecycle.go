package gui

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/protocol"
)

// forceCloseTab tracks the tab whose close press was started with Ctrl held,
// keyed by model so a tab bar rebuild between press and release keeps it.
var forceCloseTab sync.Map // *model -> string

func (m *model) takeForceClose(id string) bool {
	v, ok := forceCloseTab.LoadAndDelete(m)
	return ok && v == id
}

// tabCloseMod wraps a tab widget so a left-click started with Ctrl held
// records that tab for closeTabButton to close without confirmation, while
// any other press, wheel or keyboard input clears the pending state.
func tabCloseMod(m *model, id string, child ggui.Widget) ggui.Widget {
	return &tabCloseWatcher{m: m, id: id, child: child}
}

type tabCloseWatcher struct {
	m     *model
	id    string
	child ggui.Widget
}

func (w *tabCloseWatcher) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	return w.child.Layout(c, env)
}

func (w *tabCloseWatcher) Baseline() (float64, bool) { return ggui.BaselineOf(w.child) }

func (w *tabCloseWatcher) Paint(dst *ggui.Canvas, r ggui.Rect) {
	dst.ObserveInput(r, func() { forceCloseTab.Delete(w.m) })
	dst.HitWithin(r, w)
	dst.Paint(w.child, r)
}

func (w *tabCloseWatcher) HandlePointerWithin(ev ggui.PointerEvent) bool {
	if ev.Kind == ggui.PointerDown && ev.Button == ggui.MouseButtonLeft && ev.Mods.Ctrl {
		forceCloseTab.Store(w.m, w.id)
	}
	return false
}

// closeTabButton closes a tab from its close button: holding Ctrl skips any
// confirmation dialog and closes the tab (and kills its session) at once.
func (m *model) closeTabButton(id string) {
	if m.takeForceClose(id) {
		m.forceCloseTab(id)
		return
	}
	m.askKill(id)
}

// forceCloseTab closes tab id immediately without asking for confirmation.
func (m *model) forceCloseTab(id string) {
	switch {
	case isDocTab(id):
		m.closeDoc(id)
	case isDiffTab(id):
		m.closeDiff(id)
	case isLaunchTab(id):
		m.closeLaunch(id)
	default:
		m.kill(id)
	}
}

// Killing sessions and tidying the tabs they leave.

// askKill confirms before killing a session, unless the settings say not
// to ask, its program has already exited, or it is a shell with nothing
// running. Closing a tab does this: the
// tab and its session go together, and "Detach tab" keeps one running.
func (m *model) askKill(id string) {
	switch {
	case isDocTab(id):
		m.askCloseDoc(id)
		return
	case isDiffTab(id):
		m.closeDiff(id)
		return
	case isLaunchTab(id):
		m.closeLaunch(id)
		return
	}
	s, ok := m.session(id)
	if !ggui.Peek(m.Prefs).ConfirmKill || (ok && (s.GetState() == protocol.StateExited || s.GetState() == protocol.StateStopped)) {
		m.kill(id)
		return
	}
	confirm := func() {
		m.askConfirm(confirmation{
			Title:  T("Kill this session?"),
			Body:   T("The program is stopped on its node. Its output is lost."),
			Action: T("Kill session"),
			Run:    func() { m.kill(id) },
		})
	}
	if !ok || s.GetAgent() != "shell" {
		confirm()
		return
	}
	// A shell at its prompt loses nothing; whether one is, is asked now
	// rather than taken from the last poll, as a command may have started
	// since.
	fetch(m, 5*time.Second, func(ctx context.Context, c *client.Client) ([]*protocol.SessionInfo, error) {
		return c.Sessions(ctx)
	}, func(list []*protocol.SessionInfo, err error) {
		if now, ok := sessionIn(list, id); err == nil && ok && now.GetIdle() {
			m.kill(id)
			return
		}
		confirm()
	})
}

// kill terminates a session and closes its tab.
func (m *model) kill(id string) { m.killThen(id, nil) }

// killAll kills sessions without asking, and once every kill has gone
// through, or failed, calls done if it is not nil. UI goroutine.
func (m *model) killAll(ids []string, done func()) {
	left := len(ids)
	if left == 0 && done != nil {
		done()
	}
	for _, id := range ids {
		m.killThen(id, func() {
			if left--; left == 0 && done != nil {
				done()
			}
		})
	}
}

// killThen is kill, calling then, if not nil, once it has gone through
// or failed.
func (m *model) killThen(id string, then func()) {
	info, ok := m.session(id)
	if !ok {
		// A tab left over from a session that is gone has nothing to
		// kill, but still has to leave.
		m.closeTab(id)
		if then != nil {
			then()
		}
		return
	}
	m.closeTab(id)
	ggui.Async(func() (struct{}, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		c, err := m.link.client(ctx)
		if err != nil {
			return struct{}{}, err
		}
		return struct{}{}, c.Kill(ctx, &protocol.CloseSession{SessionId: id, NodeId: info.GetNodeId()})
	}, func(_ struct{}, err error) {
		if err == nil {
			m.killed[id] = time.Now()
			m.Sessions.Set(slices.DeleteFunc(slices.Clone(ggui.Peek(m.Sessions)), func(s *protocol.SessionInfo) bool { return s.GetId() == id }))
		}
		if then != nil {
			then()
		}
	})
}

// killedLinger is how long a killed session is kept out of the list while
// the daemon still lists it: past it, one the kill did not end shows again.
const killedLinger = 15 * time.Second

// dropKilled leaves the sessions killed here out of sessions, a poll's,
// and forgets each once the daemon no longer lists it or killedLinger has
// passed: a poll that began before a kill went through would otherwise
// bring the session back until the next, and the list blinks.
func (m *model) dropKilled(sessions []*protocol.SessionInfo, now time.Time) []*protocol.SessionInfo {
	if len(m.killed) == 0 {
		return sessions
	}
	listed := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		listed[s.GetId()] = true
	}
	for id, at := range m.killed {
		if !listed[id] || now.Sub(at) > killedLinger {
			delete(m.killed, id)
		}
	}
	return slices.DeleteFunc(sessions, func(s *protocol.SessionInfo) bool { _, gone := m.killed[s.GetId()]; return gone })
}

// tidyable tells whether closing a tab loses nothing, as the sessions'
// last poll has it: a file without unsaved changes, a diff, or a shell at
// its prompt or exited. Agents' tabs are never tidied away.
func tidyable(id string, sessions []*protocol.SessionInfo, docs map[string]docInfo) bool {
	switch {
	case isDocTab(id):
		return !docs[id].Dirty
	case isDiffTab(id), isLaunchTab(id):
		return true
	}
	s, ok := sessionIn(sessions, id)
	return ok && s.GetAgent() == "shell" && (s.GetIdle() || s.GetState() == protocol.StateExited || s.GetState() == protocol.StateStopped)
}

// anyTidyable tells whether any open tab is tidyable.
func (m *model) anyTidyable() bool {
	sessions, docs := ggui.Peek(m.Sessions), ggui.Peek(m.Docs)
	return slices.ContainsFunc(ggui.Peek(m.Tabs), func(id string) bool { return tidyable(id, sessions, docs) })
}

// tidyTabs closes the tabs among ids that lose nothing by closing, killing
// the shells among them. Whether a shell is at its prompt is asked now
// rather than taken from the last poll, as askKill does.
func (m *model) tidyTabs(ids []string) {
	docs := ggui.Peek(m.Docs)
	var shells []string
	for _, id := range ids {
		switch {
		case isDocTab(id):
			if tidyable(id, nil, docs) {
				m.closeDoc(id)
			}
		case isDiffTab(id):
			m.closeDiff(id)
		case isLaunchTab(id):
			m.closeLaunch(id)
		default:
			if s, ok := m.session(id); ok && s.GetAgent() == "shell" {
				shells = append(shells, id)
			}
		}
	}
	if len(shells) == 0 {
		return
	}
	fetch(m, 5*time.Second, func(ctx context.Context, c *client.Client) ([]*protocol.SessionInfo, error) {
		return c.Sessions(ctx)
	}, func(list []*protocol.SessionInfo, err error) {
		if err != nil {
			return
		}
		for _, id := range shells {
			if tidyable(id, list, nil) {
				m.kill(id)
			}
		}
	})
}
