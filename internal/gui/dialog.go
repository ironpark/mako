package gui

import (
	"context"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// A dialog of options an agent's terminal shows where no hook holds it, as
// a folder's trust, is answered from Chat as well: what it asks, and a
// button for each option, which its node picks there as the user would.

// dialogOfSession follows the dialog a session's agent waits on in its
// terminal.
func (m *model) dialogOfSession(id string) ggui.Readable[*protocol.ScreenDialog] {
	return ggui.Derived(func() *protocol.ScreenDialog {
		s, _ := sessionIn(m.Sessions.Get(), id)
		if s.GetApproval() != nil {
			return nil
		}
		return s.GetDialog()
	}).WithEqual(func(a, b *protocol.ScreenDialog) bool { return a.GetId() == b.GetId() })
}

// dialogCard is the dialog at the foot of Chat.
func dialogCard(m *model, id string) ggui.Widget {
	return ggui.View(m.dialogOfSession(id), func(d *protocol.ScreenDialog) ggui.Widget {
		if d == nil {
			return ggui.Box()
		}
		head := ggui.Row(
			icon("terminal").Size(16).Color(skin.DotWaiting),
			ggui.Text(T("The agent waits in its terminal")).Size(13.5).Weight(ggui.WeightMedium).NoWrap(),
			ggui.Spacer(),
			ui.Button(T("Show terminal"), func() { m.setChatMode(id, false) }).Ghost(),
		).Gap(8).Align(ggui.AlignCenter)
		rows := []ggui.Widget{head}
		if d.GetText() != "" {
			rows = append(rows, scrolled(toolBlock(d.GetText(), false)))
		}
		var options []ggui.Widget
		// Alike, none put forward; the cursor's is marked, as the terminal
		// marks it.
		for i, o := range d.GetOptions() {
			if i == int(d.GetSelected()) {
				o = "❯ " + o
			}
			options = append(options, ui.Button(o, func() { m.answerDialog(id, d.GetId(), i) }).Outline())
		}
		rows = append(rows, ggui.Wrap(options...).Gap(8).RunGap(8))
		return approvalFrame(ggui.Column(rows...).Gap(10).Stretch())
	})
}

// answerDialog picks option of the dialog session id's agent shows.
func (m *model) answerDialog(id, dialog string, option int) {
	info, ok := m.session(id)
	if !ok {
		return
	}
	req := &protocol.AnswerDialog{NodeId: m.remoteID(info.GetNodeId()), SessionId: id, DialogId: dialog, Option: int32(option)}
	m.editSession(id, func(s *protocol.SessionInfo) {
		if s.GetDialog().GetId() == dialog {
			s.Dialog = nil
		}
	})
	fetch(m, 10*time.Second, func(ctx context.Context, c *client.Client) (*protocol.SessionInfo, error) {
		return c.AnswerDialog(ctx, req)
	}, func(_ *protocol.SessionInfo, err error) {
		if err != nil {
			m.connErr(err)
		}
		m.link.pollNow()
	})
}
