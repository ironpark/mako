package gui

import (
	"math"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// A change of the window layout an agent asked for is answered in a bar
// over the window, which the window draws itself, outside every document,
// so that no layout can hide it: to allow the change, once, for the
// document the space shows, or for every one; and, a whole document set
// and shown, to keep it before it reverts. The agent's session shows the
// approval too, as it shows any (approval.go).

// layoutApprovalButtons are the answers to the approval of a layout change
// on session id's agent.
func layoutApprovalButtons(m *model, id, approval string) []ggui.Widget {
	answer := func(decision, option string) func() {
		return func() { m.answerLayoutApproval(id, approval, decision, option) }
	}
	return []ggui.Widget{
		ui.Button(T("Deny"), answer(protocol.DecisionDeny, "")).Outline(),
		ui.Button(T("Always allow"), answer(protocol.DecisionAllow, protocol.LayoutAllowAlways)).Outline(),
		ui.Button(T("Allow in this space"), answer(protocol.DecisionAllow, protocol.LayoutAllowSpace)).Outline(),
		ui.Button(T("Allow"), answer(protocol.DecisionAllow, "")),
	}
}

// layoutBar is the change staged, over the window: nothing while there is
// none.
func layoutBar(m *model) ggui.Widget {
	return ggui.View(m.LayoutStage, func(st *protocol.LayoutStage) ggui.Widget {
		if st == nil {
			return widget.Box()
		}
		var title, detail string
		var buttons []ggui.Widget
		if st.GetPhase() == protocol.LayoutKeep {
			title = T("Keep this window layout?")
			left := ggui.State(keepLeft(st))
			ggui.Effect(func() ggui.Cleanup {
				if m.post == nil {
					return nil
				}
				return m.every(250*time.Millisecond, func() { left.Set(keepLeft(st)) })
			})
			buttons = []ggui.Widget{
				ui.Button(T("Revert"), func() { m.answerStage(st, false) }).Outline(),
				ui.Button(T("Keep"), func() { m.answerStage(st, true) }),
			}
			body := widget.Column(
				widget.Text(title).Size(13.5).Weight(ggui.WeightMedium),
				ggui.View(left, func(n int) ggui.Widget {
					return muted(widget.Text(Tf("It goes back in %d s unless kept", n)).Size(12.5))
				}),
			).Gap(3).Stretch()
			return layoutBarFrame(body, buttons)
		}
		title = Tf("%s asks to change the window layout", st.GetAgent())
		detail = st.GetDocument() + ": " + st.GetSummary()
		buttons = layoutApprovalButtons(m, st.GetSessionId(), st.GetId())
		body := widget.Column(
			widget.Text(title).Size(13.5).Weight(ggui.WeightMedium),
			muted(widget.Text(detail).Size(12.5).NoWrap().Ellipsis()),
			muted(widget.Text(T("The window shows the change until you answer.")).Size(12.5)),
		).Gap(3).Stretch()
		return layoutBarFrame(body, buttons)
	})
}

// keepLeft is how many seconds are left before the change reverts.
func keepLeft(st *protocol.LayoutStage) int {
	return max(int(math.Ceil(float64(time.Until(time.UnixMilli(st.GetDeadline())))/float64(time.Second))), 0)
}

// layoutBarFrame frames the bar, at the top of the window under the
// headers.
func layoutBarFrame(body ggui.Widget, buttons []ggui.Widget) ggui.Widget {
	row := widget.Row(
		icon("layout-grid").Size(18).Color(skin.DotWaiting),
		widget.Expanded(body),
		widget.Row(buttons...).Gap(8),
	).Gap(12).Align(widget.AlignCenter)
	card := widget.Box(row).Fill(skin.Surface1).Border(1, skin.DotWaiting).Radius(12).Pad(12, 16).MaxWidth(760)
	return widget.Padding(widget.Align(card).Top(), headerHeight+10, 16, 0, 16)
}
