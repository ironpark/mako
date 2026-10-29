package gui

import (
	"strings"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
)

// confirmation is an operation that loses work, asking to be sure.
type confirmation struct {
	Title, Body, Action string
	Run                 func()
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

// confirmDialog asks to be sure of what askConfirm was given, a
// destructive button carrying it out.
func confirmDialog(m *model) ggui.Widget {
	open := ggui.Bind(func() bool { return m.Confirmation.Get().Title != "" }, func(v bool) {
		if !v {
			m.Confirmation.Set(confirmation{})
		}
	})
	return ggui.View(m.Confirmation, func(c confirmation) ggui.Widget {
		return ui.AlertDialog(open, c.Title, c.Body).Confirm(c.Action, func() {
			if c.Run != nil {
				c.Run()
			}
		}).Destructive()
	})
}
