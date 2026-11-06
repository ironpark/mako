package gui

import (
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// A node's link from this daemon drops as a laptop sleeps or a network
// changes, and the daemon makes it again: at once, then waiting longer
// each time it fails. The node's dot and line say where that stands, and
// Reconnect tries it now.

// linkDown reports whether the daemon's own link to n is down or being
// made again; a daemon too old to say reads as linked.
func linkDown(n *protocol.Node) bool {
	return n.GetLink() == protocol.LinkDown || n.GetLink() == protocol.LinkLinking
}

// nodeDot is n's dot: green while the window can drive it, amber while
// its link is being made, red otherwise, as for a node no longer listed.
func nodeDot(n *protocol.Node) ggui.Widget {
	switch {
	case n.GetLink() == protocol.LinkLinking:
		return dot(skin.DotWaiting)
	case reachable(n):
		return dot(skin.DotRunning)
	}
	return dot(skin.DotDown)
}

// nodeLinkLine says how a link down stands, the time to the next try
// counting down, with why the last failed as its tooltip; nothing for a
// link that is up.
func nodeLinkLine(m *model, n *protocol.Node) ggui.Widget {
	if !linkDown(n) {
		return ggui.Box()
	}
	at := protocol.TimeOf(n.GetRelinkAt())
	now := ggui.State(time.Now())
	if n.GetLink() == protocol.LinkDown && at.After(time.Now()) { // a countdown to show
		ggui.Effect(func() ggui.Cleanup { return m.every(time.Second, func() { now.Set(time.Now()) }) })
	}
	line := ggui.Reactive(func() ggui.Widget {
		text := T("Connecting…")
		if n.GetLink() == protocol.LinkDown {
			if wait := at.Sub(now.Get()).Round(time.Second); wait > 0 {
				text = Tf("Offline · trying again in %s", wait)
			} else {
				text = T("Offline · trying again")
			}
		}
		return ggui.Text(text).Size(11.5).Color(pick(n.GetLink() == protocol.LinkLinking, skin.DotWaiting, skin.DotDown)).NoWrap().Ellipsis()
	})
	if n.GetLinkError() == "" {
		return line
	}
	return ui.Tooltip(line, n.GetLinkError())
}

// relink has the daemon try its link to node now.
func (m *model) relink(node *protocol.Node) {
	m.answerAccess(&protocol.Access{Op: "relink", NodeId: node.GetId()}, "", nil)
}

// relinkButton tries the link to n now, while it is down.
func relinkButton(m *model, n *protocol.Node) ggui.Widget {
	return ui.ButtonOf(ggui.Row(icon("refresh-cw").Size(13), ggui.Text(T("Reconnect")).Size(12.5)).Gap(6),
		func() { m.relink(n) }).Outline().Disabled(n.GetLink() == protocol.LinkLinking).Name(Tf("Reconnect to %s", n.GetName()))
}
