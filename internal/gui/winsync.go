package gui

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"maps"
	"os"
	"slices"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/layoutdoc"
	"github.com/ironpark/mako/internal/protocol"
	shared "github.com/ironpark/mako/internal/workspace"
)

// The window layout documents are the workspace's: named documents, in
// the window's document as WindowLayouts, each space naming the one it
// shows, "" for the default, which is layoutdoc.DefaultName's while the
// workspace holds one and layoutdoc.Default otherwise. The window edits
// them as it edits the rest, and its edits go to the daemon the same way
// (workspace.go), and so to the other nodes.
//
// A change an agent asks for is the daemon's until the user has it: the
// daemon tells its windows of it, staged (Workspace.stage), and the window
// shows the document staged in place of the one it changes, under a bar
// to allow it or, once allowed, to keep it (layoutbar.go). Nothing of it
// is in the workspace until it is kept.
//
// Before the workspace held them, the window read one document from
// layout.json beside gui.json. That file is taken in now: once, when the
// window first hears from its daemon and the workspace holds no default
// document, as that one, the file then moved to layout.json.imported; and
// whenever Reload window layout asks, as the document of the space shown.

const layoutFileName = "layout.json"

// shownLayoutName is the name of the document the space shown uses.
func (p spaceSet) shownLayoutName() string {
	if sp, ok := p.spaceOf(p.Space); ok && sp.Layout != "" {
		return sp.Layout
	}
	return layoutdoc.DefaultName
}

// winSource is what the window draws its layout from: the document's JSON,
// "" for the built-in default.
type winSource struct{ body string }

// followWindowLayout has the window draw the document of the space shown,
// or the change of it staged, as either changes. It runs under the app's
// owner.
func followWindowLayout(m *model) {
	src := ggui.Derived(func() winSource {
		p := m.spaces()
		name := p.shownLayoutName()
		if st := m.LayoutStage.Get(); st != nil && st.GetDocument() == name {
			return winSource{body: string(st.GetDoc())}
		}
		return winSource{body: string(p.WindowLayouts[name])}
	})
	m.showWindowLayout(ggui.Peek(src))
	ggui.Watch(src, m.showWindowLayout)
}

// showWindowLayout draws the document of src, the default for none and
// for one that cannot be read, which is told once.
func (m *model) showWindowLayout(src winSource) {
	if src.body == m.winDocBody {
		return
	}
	m.winDocBody = src.body
	d := layoutdoc.Default()
	if src.body != "" {
		read, err := layoutdoc.Parse([]byte(src.body), m.blocks)
		switch {
		case err == nil:
			d = read
		case src.body != m.winDocBad:
			m.winDocBad = src.body
			m.tellWindowDoc(err)
		}
	}
	m.setWindowDoc(d)
}

// tellWindowDoc says why the window's layout is the default rather than
// the document's.
func (m *model) tellWindowDoc(err error) {
	log.Printf("mako: window layout: %v", err)
	if m.toaster != nil {
		m.toaster.Push(ui.Toast(T("The window layout could not be read, so the default is shown"), err.Error()).Destructive())
	}
}

// setWindowLayout puts body in as the document of the name, nil to
// remove it: an edit of the workspace, which goes to the daemon.
func (m *model) setWindowLayout(name string, body []byte) {
	m.Spaces.Update(func(l spaceList) spaceList {
		l.WindowLayouts = maps.Clone(l.WindowLayouts)
		if body == nil {
			delete(l.WindowLayouts, name)
		} else {
			if l.WindowLayouts == nil {
				l.WindowLayouts = map[string]json.RawMessage{}
			}
			l.WindowLayouts[name] = body
		}
		if len(l.WindowLayouts) == 0 {
			l.WindowLayouts = nil
		}
		return l
	})
}

// resetWindowDoc is the way back from any layout, which no document can
// take away: a change staged is turned down, and the space shown goes
// back to the default document, or, showing it, the default goes back to
// the built-in one. It takes effect in the window at once, whether its
// daemon is there or not, which has the edit when it is.
func (m *model) resetWindowDoc() {
	if st := ggui.Peek(m.LayoutStage); st != nil {
		m.answerStage(st, false)
		m.LayoutStage.Set(nil)
	}
	p := m.spacesNow()
	sp, ok := p.spaceOf(p.Space)
	if ok && sp.Layout != "" {
		m.Spaces.Update(func(l spaceList) spaceList {
			l.Spaces = slices.Clone(l.Spaces)
			for i := range l.Spaces {
				if l.Spaces[i].ID == sp.ID {
					l.Spaces[i].Layout = ""
				}
			}
			return l
		})
		return
	}
	m.setWindowLayout(layoutdoc.DefaultName, nil)
}

// readLayoutFile reads layout.json as a document: the file's path, and its
// JSON as the window writes it; nil for no file.
func (m *model) readLayoutFile() (string, []byte, error) {
	path, err := configFile(layoutFileName)
	if err != nil {
		return "", nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return path, nil, err
	}
	body, _, err := layoutdoc.Canonical(data, m.blocks)
	return path, body, err
}

// reloadWindowDoc takes layout.json in as the document of the space shown.
func (m *model) reloadWindowDoc() {
	_, body, err := m.readLayoutFile()
	if err != nil {
		m.tellWindowDoc(err)
		return
	}
	m.setWindowLayout(m.spacesNow().shownLayoutName(), body)
}

// importLayoutFile takes layout.json in as the default document, once,
// when the workspace st, the first the window takes, holds none; and
// moves it aside, taken. UI goroutine.
func (m *model) importLayoutFile(st *protocol.WorkspaceState) {
	if m.ws.imported {
		return
	}
	m.ws.imported = true
	if shared.WindowLayout(st, layoutdoc.DefaultName) != nil {
		return
	}
	path, body, err := m.readLayoutFile()
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return
	case err != nil:
		m.tellWindowDoc(err)
		return
	}
	m.setWindowLayout(layoutdoc.DefaultName, body)
	if err := os.Rename(path, path+".imported"); err != nil {
		log.Printf("mako: window layout: %v", err)
	}
}

// answerStage answers the change staged: allows or keeps it, or turns it
// down, as its phase takes it. Turned down, it leaves the window at once.
func (m *model) answerStage(st *protocol.LayoutStage, yes bool) {
	if m.post == nil {
		return // not started, as in a test
	}
	if st.GetPhase() == protocol.LayoutApprove {
		m.answerLayoutApproval(st.GetSessionId(), st.GetId(), pick(yes, protocol.DecisionAllow, protocol.DecisionDeny), "")
	} else {
		decision := pick(yes, protocol.LayoutKeep, protocol.LayoutRevert)
		fetch(m, 5*time.Second, func(ctx context.Context, c *client.Client) (*protocol.Workspace, error) {
			return c.DecideLayout(ctx, &protocol.LayoutDecision{StageId: st.GetId(), Decision: decision})
		}, func(ws *protocol.Workspace, err error) {
			if err != nil {
				m.connErr(err)
				return
			}
			m.takeWorkspace(ws)
		})
	}
	if !yes {
		m.LayoutStage.Set(nil)
	}
}

// answerLayoutApproval answers the approval of a layout change on session
// id's agent, as any approval is answered, with option: "" allows it once.
func (m *model) answerLayoutApproval(id, approval, decision, option string) {
	m.answerApproval(id, &protocol.AnswerApproval{ApprovalId: approval, Decision: decision, Option: option})
	if decision != protocol.DecisionAllow {
		m.LayoutStage.Set(nil)
	}
}

// forgetLayoutAllowances has agents ask again before every change of the
// window layout.
func (m *model) forgetLayoutAllowances() {
	fetch(m, 5*time.Second, func(ctx context.Context, c *client.Client) (*protocol.Workspace, error) {
		return c.DecideLayout(ctx, &protocol.LayoutDecision{Decision: protocol.LayoutForget})
	}, func(_ *protocol.Workspace, err error) {
		if err != nil {
			m.connErr(err)
		}
	})
}
