package gui

import (
	"strings"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// A document shared between nodes may place a plugin's block on a node
// that lacks the plugin: installing is each node's own. The placeholder
// that stands for it there offers to install it, from each registry of
// this node that lists it, naming the registry and the publisher's key as
// the store does; what the plugin then asks for is asked as for any.

// missingPluginBlock stands for a plugin's block this node lacks.
func missingPluginBlock(m *model, name string) ggui.Widget {
	rest, ok := strings.CutPrefix(name, "plugin:")
	if !ok {
		return missingBlock(name)
	}
	id, block, ok := strings.Cut(rest, "/")
	if !ok || id == "" || block == "" || !m.storeOpen() {
		return missingBlock(name)
	}
	m.lookUpPlugin(id)
	return ggui.View(m.store.Known, func(known map[string][]*protocol.PluginListing) ggui.Widget {
		var offers []ggui.Widget
		for _, l := range known[id] {
			if l.GetBlocked() != "" || l.GetOtherKey() || l.GetInstalled() != "" || len(offers) >= 3 {
				continue
			}
			registry := l.GetRegistry()
			offers = append(offers, widget.Column(
				ggui.Reactive(func() ggui.Widget {
					if m.store.Busy.Get()[id] {
						return ui.Spinner()
					}
					return ui.ButtonOf(widget.Row(icon("download").Size(14), widget.Text(T("Install on this node")).Size(13)).Gap(6).Align(widget.AlignCenter),
						func() { m.installPlugin(protocol.InstallOp, id, registry) }).Secondary().Name(Tf("Install %s on this node", id))
				}),
				muted(widget.Text(Tf("%s %s from %s, key %s", id, l.GetVersion(), registryLabel(registry), shortFingerprint(l.GetFingerprint()))).Size(11.5)),
			).Gap(4).Align(widget.AlignCenter))
		}
		if len(offers) == 0 {
			return missingBlock(name)
		}
		rows := []ggui.Widget{
			icon("package").Size(22).Color(skin.ExtraMuted),
			widget.Text(T("Not installed on this node")).Size(13).Weight(ggui.WeightMedium),
			muted(widget.Text(name).Size(12).Font(ggui.DefaultMonoFont())),
		}
		rows = append(rows, offers...)
		return widget.Box(widget.Center(widget.Column(rows...).Gap(8).Align(widget.AlignCenter))).Pad(12).Fill(skin.Surface0)
	})
}
