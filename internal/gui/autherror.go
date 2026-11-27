package gui

import (
	"cmp"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// authErrorOfSession follows the provider authentication error active on
// session id, if any.
func (m *model) authErrorOfSession(id string) ggui.Readable[*protocol.AuthError] {
	return ggui.Derived(func() *protocol.AuthError {
		s, _ := sessionIn(m.Sessions.Get(), id)
		return s.GetAuthError()
	}).WithEqual(protocol.Same)
}

// authErrorCard surfaces a host provider authentication failure at the foot
// of Chat with actionable re-login guidance and a one-click command copy.
func authErrorCard(m *model, id string) ggui.Widget {
	return ggui.View(m.authErrorOfSession(id), func(err *protocol.AuthError) ggui.Widget {
		if err == nil {
			return widget.Box()
		}
		title := cmp.Or(err.GetSummary(), T("Provider authentication failed"))
		head := widget.Row(
			icon("key-round").Size(16).Color(skin.DotDown),
			widget.Expanded(widget.Text(title).Size(13.5).Weight(ggui.WeightMedium)),
		).Gap(8).Align(widget.AlignCenter)
		rows := []ggui.Widget{head}
		if g := err.GetGuidance(); g != "" {
			rows = append(rows, muted(widget.Text(g).Size(12.5).Selectable()))
		}
		var actions []ggui.Widget
		if cmd := err.GetCommand(); cmd != "" {
			pill := widget.Box(widget.Text(cmd).Font(ggui.DefaultMonoFont()).Size(12.5).Selectable()).
				Fill(skin.Surface2).Radius(6).Pad(5, 9)
			actions = append(actions,
				pill,
				ui.Button(T("Copy command"), func() {
					ggui.CurrentClipboard().Write(cmd)
					if m.toaster != nil {
						m.toaster.Push(ui.Toast(T("Command copied"), cmd))
					}
				}).Outline(),
			)
		}
		actions = append(actions, ui.Button(T("Show terminal"), func() { m.setChatMode(id, false) }).Ghost())
		rows = append(rows, widget.Wrap(actions...).Gap(8).RunGap(8).Align(widget.AlignCenter))
		card := widget.Box(widget.Column(rows...).Gap(10).Stretch()).
			Fill(skin.Surface1).Border(1, skin.DotDown).Radius(12).Pad(12, 14)
		return &lane{child: arrive(widget.Box(card).Pad(0, 0, 8, 0))}
	})
}

// authErrorBar surfaces a host provider authentication failure in one line
// over the terminal view.
func authErrorBar(m *model, id string) ggui.Widget {
	return ggui.View(m.authErrorOfSession(id), func(err *protocol.AuthError) ggui.Widget {
		if err == nil {
			return widget.Box()
		}
		title := cmp.Or(err.GetSummary(), T("Provider authentication failed"))
		row := []ggui.Widget{
			icon("key-round").Size(15).Color(skin.DotDown),
			widget.Text(title).Size(12.5).Weight(ggui.WeightMedium).NoWrap(),
		}
		if cmd := err.GetCommand(); cmd != "" {
			row = append(row,
				widget.Expanded(widget.Text(Tf("Run %s on the host", cmd)).Size(12.5).Font(ggui.DefaultMonoFont()).Color(skin.Muted).NoWrap().Ellipsis()),
				ui.Button(T("Copy command"), func() {
					ggui.CurrentClipboard().Write(cmd)
					if m.toaster != nil {
						m.toaster.Push(ui.Toast(T("Command copied"), cmd))
					}
				}).Outline(),
			)
		} else {
			row = append(row, widget.Expanded(widget.Text(err.GetGuidance()).Size(12.5).Color(skin.Muted).NoWrap().Ellipsis()))
		}
		return widget.Column(
			widget.Box(widget.Row(row...).Gap(10).Align(widget.AlignCenter)).Pad(6, 12).Fill(skin.Surface2),
			hline(),
		).Stretch()
	})
}
