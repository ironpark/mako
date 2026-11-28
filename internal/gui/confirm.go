package gui

import (
	"strings"

	"github.com/ironpark/ggui"
)

// confirmation is an operation that loses work, asking to be sure.
type confirmation struct {
	Title, Body, Action string
	Run                 func()
	Safe                bool   // the action loses nothing, and is not marked as one that does
	Owner               string // "" for the main window, or a Space window's spaceID
}

// askConfirm asks to be sure of c, in the window's confirmation dialog.
func (m *model) askConfirm(c confirmation) { m.Confirmation.Set(c) }

// confirmPaste asks before pasting text whose lines would run as typed,
// one after another, the program not holding them as a paste.
func (m *model) confirmPaste(text string, paste func()) {
	lines := len(strings.FieldsFunc(text, func(r rune) bool { return r == '\n' || r == '\r' }))
	m.askConfirm(confirmation{
		Title:  Tf("Paste %d lines?", max(lines, 1)),
		Body:   T("The program in the terminal does not take pastes as such: each line runs as soon as it is pasted, as if typed."),
		Action: T("Paste"),
		Run:    paste,
	})
}

// confirmDialog asks to be sure of what askConfirm was given in the main window.
func confirmDialog(m *model) ggui.Widget {
	return confirmDialogFor(m, "")
}

// confirmDialogFor renders the confirmation dialog only in the window matching
// owner ("" for the main window, or sw.spaceID for a Space window), preventing
// modals from duplicating across windows.
func confirmDialogFor(m *model, owner string) ggui.Widget {
	open := ggui.Bind(func() bool {
		c := m.Confirmation.Get()
		return c.Title != "" && c.Owner == owner
	}, func(v bool) {
		if !v && ggui.Peek(m.Confirmation).Owner == owner {
			m.Confirmation.Set(confirmation{})
		}
	})
	return ggui.View(m.Confirmation, func(c confirmation) ggui.Widget {
		if c.Owner != owner {
			return alertDialog(alertAsk{Open: open})
		}
		return alertDialog(alertAsk{
			Open:        open,
			Title:       c.Title,
			Body:        c.Body,
			Action:      c.Action,
			Destructive: !c.Safe,
			Confirm:     c.Run,
		})
	})
}
