package gui

import (
	"context"
	"slices"
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
	m.RenameText.Set(s.Name)
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
	ggui.Async(func() (protocol.SessionInfo, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		c, err := m.client(ctx)
		if err != nil {
			return protocol.SessionInfo{}, err
		}
		return c.Rename(ctx, m.key(), protocol.RenameSession{SessionID: id, NodeID: s.NodeID, Name: name})
	}, func(info protocol.SessionInfo, err error) {
		if err != nil {
			m.RenameErr.Set(err.Error())
			return
		}
		m.RenameOpen.Set(false)
		sessions := slices.Clone(ggui.Peek(m.Sessions))
		for i := range sessions {
			if sessions[i].ID == id {
				sessions[i].Name = info.Name
			}
		}
		m.Sessions.Set(sessions)
	})
}

func renameDialog(m *model) ggui.Widget {
	hasErr := ggui.Map(m.RenameErr, func(s string) bool { return s != "" })
	return ui.Dialog(m.RenameOpen, ggui.Column(
		ui.Field(T("Name"), ui.TextField(m.RenameText).Placeholder(T("Its program's title, or its agent")).
			Name(T("Session name")).OnSubmit(func(string) { m.submitRename() })),
		ggui.If(hasErr, func() ggui.Widget {
			return ggui.TextOf(m.RenameErr).Size(12).Color(tokDotDown)
		}),
		ggui.Row(
			ui.Button(T("Cancel"), func() { m.RenameOpen.Set(false) }).Outline(),
			ui.Button(T("Rename"), m.submitRename),
		).Gap(8).Justify(ggui.JustifyEnd),
	).Gap(16).Stretch()).Title(T("Rename session")).Width(420)
}

// sessionMenu gives a session's tab or sidebar row a context menu; a tab's
// has Detach tab too.
func sessionMenu(m *model, w ggui.Widget, id, label string, isTab bool) ggui.Widget {
	items := []ggui.Widget{ui.MenuItem(T("Rename…"), func() { m.askRename(id) })}
	if isTab {
		items = append(items, ui.MenuItem(T("Detach tab"), func() { m.closeTab(id) }))
	}
	items = append(items, ui.MenuDivider(), ui.MenuItem(T("Kill session"), func() { m.askKill(id) }))
	return ui.ContextMenu(w, items...).Name(Tf("%s actions", label))
}
