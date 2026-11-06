package gui

import "github.com/ironpark/ggui"

// answerConfirm presses the confirmation dialog's button: it closes, and
// what it asked about is done.
func answerConfirm(m *model) {
	c := ggui.Peek(m.Confirmation)
	m.Confirmation.Set(confirmation{})
	if c.Run != nil {
		c.Run()
	}
}
