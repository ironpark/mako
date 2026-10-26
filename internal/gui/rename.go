package gui

import (
	"context"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/sanbo/internal/protocol"
)

// A session can be named, from its tab's or its sidebar row's context
// menu, the header's menu or the palette. The name is kept by the daemon
// of the session's node, so every window and `sanbo ls` show it.

// askRename opens the rename dialog over a session.
func (m *model) askRename(id string) {
	s, ok := m.session(id)
	if !ok {
		return
	}
	m.renaming = id
	m.RenameText.Set(s.GetName())
	m.RenameErr.Set("")
	m.RenameOpen.Set(true)
}

// submitRename names the session the dialog is over; a blank name gives
// it back its own.
func (m *model) submitRename() {
	id := m.renaming
	s, ok := m.session(id)
	if !ok {
		m.RenameOpen.Set(false)
		return
	}
	name := strings.TrimSpace(ggui.Peek(m.RenameText))
	ggui.Async(func() (*protocol.SessionInfo, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		c, err := m.client(ctx)
		if err != nil {
			return nil, err
		}
		return c.Rename(ctx, m.key(), &protocol.RenameSession{SessionId: id, NodeId: s.GetNodeId(), Name: name})
	}, func(info *protocol.SessionInfo, err error) {
		if err != nil {
			m.RenameErr.Set(err.Error())
			return
		}
		m.RenameOpen.Set(false)
		m.editSession(id, func(s *protocol.SessionInfo) { s.Name = info.GetName() })
	})
}

func renameDialog(m *model) ggui.Widget {
	return nameDialog(nameAsk{
		Open: m.RenameOpen, Text: m.RenameText, Err: m.RenameErr,
		Title: T("Rename session"), Field: T("Session name"), Action: T("Rename"),
		Placeholder: T("Its program's title, or its agent"), Submit: m.submitRename,
	})
}

// nameAsk is a dialog asking for a name: whether it is open, the name
// typed, and why the last one was refused.
type nameAsk struct {
	Open                                    *ggui.StateValue[bool]
	Text, Err                               *ggui.StateValue[string]
	Title, Field, Action, Placeholder, Note string
	Submit                                  func()
}

// nameDialog asks for a name, submitting it with Enter or the action.
func nameDialog(a nameAsk) ggui.Widget {
	hasErr := ggui.Map(a.Err, func(s string) bool { return s != "" })
	field := ui.TextField(a.Text).Name(a.Field).OnSubmit(func(string) { a.Submit() })
	if a.Placeholder != "" {
		field = field.Placeholder(a.Placeholder)
	}
	rows := []ggui.Widget{ui.Field(T("Name"), field)}
	if a.Note != "" {
		rows = append(rows, muted(ggui.Text(a.Note).Size(12)))
	}
	rows = append(rows,
		ggui.If(hasErr, func() ggui.Widget { return ggui.TextOf(a.Err).Size(12).Color(tokDotDown) }),
		ggui.Row(
			ui.Button(T("Cancel"), func() { a.Open.Set(false) }).Outline(),
			ui.Button(a.Action, a.Submit),
		).Gap(8).Justify(ggui.JustifyEnd),
	)
	return ui.Dialog(a.Open, ggui.Column(rows...).Gap(16).Stretch()).Title(a.Title).Width(420)
}

// sessionMenu gives a session's tab or sidebar row a context menu; a tab's
// has Detach tab too.
func sessionMenu(m *model, w ggui.Widget, id, label string, isTab bool) ggui.Widget {
	pin := T("Pin to top")
	if m.pinned(id) {
		pin = T("Unpin")
	}
	items := []ggui.Widget{ui.MenuItem(T("Rename…"), func() { m.askRename(id) }), ui.MenuItem(pin, func() { m.togglePin(id) })}
	if isTab {
		items = append(items, ui.MenuItem(T("Detach tab"), func() { m.closeTab(id) }))
	}
	items = append(items, ui.MenuDivider(), ui.MenuItem(T("Kill session"), func() { m.askKill(id) }))
	return ui.ContextMenu(w, items...).Name(Tf("%s actions", label))
}
