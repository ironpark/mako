package gui

import (
	"cmp"
	"image/color"
	"slices"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// Left sidebar plugin pages (revision 49).
//
// Any plugin block whose manifest includes "sidebar" in its slots appears as a
// page in the left sidebar alongside the default Projects/Agents view. When no
// plugin contributes a sidebar page, the switcher bar renders nothing and the
// sidebar is unchanged.

type sidebarPageDef struct {
	Block    string
	PluginID string
	Title    string
	Icon     string
	Scope    string
}

// sidebarPluginBlocks returns all active plugin blocks that declare the
// "sidebar" slot, in plugin listing order.
func sidebarPluginBlocks(list []*protocol.PluginInfo) []sidebarPageDef {
	var out []sidebarPageDef
	for _, p := range list {
		if st := p.GetState(); st == protocol.PluginDisabled || st == protocol.PluginBlocked {
			continue
		}
		for _, b := range p.GetBlocks() {
			if !slices.Contains(b.GetSlots(), "sidebar") {
				continue
			}
			out = append(out, sidebarPageDef{
				Block:    b.GetName(),
				PluginID: p.GetId(),
				Title:    cmp.Or(b.GetTitle(), b.GetName()),
				Icon:     cmp.Or(b.GetIcon(), "package"),
				Scope:    b.GetScope(),
			})
		}
	}
	return out
}

// findSidebarPage looks up blockName among pages.
func findSidebarPage(pages []sidebarPageDef, blockName string) (sidebarPageDef, bool) {
	if blockName == "" {
		return sidebarPageDef{}, false
	}
	for _, pg := range pages {
		if pg.Block == blockName {
			return pg, true
		}
	}
	return sidebarPageDef{}, false
}

// openSidebarPage reveals the left sidebar and switches it to blockName
// ("" returns to the default Projects/Agents view).
func (m *model) openSidebarPage(blockName string) {
	m.SidebarOpen.Set(true)
	m.SidebarPage.Set(blockName)
}

// sidebarPageBar renders the compact icon switcher strip right below the
// sidebar header when at least one plugin contributes a sidebar page.
func sidebarPageBar(m *model, active *ggui.StateValue[string]) ggui.Widget {
	return ggui.Reactive(func() ggui.Widget {
		pages := sidebarPluginBlocks(m.plugins.List.Get())
		if len(pages) == 0 {
			return widget.Box()
		}
		cur := active.Get()
		if cur != "" {
			if _, ok := findSidebarPage(pages, cur); !ok {
				cur = ""
			}
		}
		btn := func(glyphName, title, target string, selected bool) ggui.Widget {
			fg := pick[color.Color](selected, skin.AccentBright, skin.Muted)
			b := ui.ButtonOf(pluginIcon(glyphName).Size(14).Color(fg), func() {
				active.Set(target)
			}).Ghost().Pad(5, 8).Radius(6).Name(title)
			var w ggui.Widget = b
			if selected {
				w = widget.Box(b).Fill(skin.Surface2).Radius(6)
			}
			return ui.Tooltip(w, title)
		}
		items := make([]ggui.Widget, 0, 1+len(pages))
		items = append(items, btn("folder-kanban", T("Projects"), "", cur == ""))
		for _, pg := range pages {
			p := pg
			items = append(items, btn(p.Icon, p.Title, p.Block, cur == p.Block))
		}
		strip := looseHeight{
			widget.Scroll(widget.Row(items...).Gap(2).Align(widget.AlignCenter)).
				Horizontal().Shrink().Bar(color.Transparent),
		}
		return widget.Column(
			widget.Box(strip).Pad(4, 8),
			hline(),
		).Stretch()
	})
}

// sidebarPluginPageView renders one plugin block as a full-height left sidebar
// page, lifting any leading toolbar row into a slim sub-header and preserving
// third-party provenance badges.
func sidebarPluginPageView(m *model, pg sidebarPageDef) ggui.Widget {
	bv := treeBlockView(m, pg.Block, blockOrigin{plugin: pg.PluginID}, pg.Title, pg.Scope, nil, blockPlace{Solo: true})
	header := ggui.Reactive(func() ggui.Widget {
		hasTools := bv.hasTools != nil && bv.hasTools.Get()
		titleInTools := bv.titleInToolsOf != nil && bv.titleInToolsOf.Get()
		var row []ggui.Widget
		if !titleInTools {
			row = append(row,
				pluginIcon(pg.Icon).Size(13).Color(skin.Muted),
				widget.Expanded(widget.Text(pg.Title).Size(12.5).Weight(ggui.WeightMedium).NoWrap().Ellipsis()),
			)
		}
		if bv.Tools != nil && hasTools {
			if titleInTools {
				row = append(row, widget.Expanded(bv.Tools))
			} else {
				row = append(row, bv.Tools)
			}
		}
		if bv.Accessory != nil {
			row = append(row, bv.Accessory)
		}
		if bv.Menu != nil {
			if items := bv.Menu(); len(items) > 0 {
				row = append(row, iconMenu(ui.MenuOf(icon("ellipsis"), items...).Name(Tf("%s actions", pg.Title)), 4))
			}
		}
		if len(row) == 0 {
			return widget.Box()
		}
		return widget.Column(
			widget.Box(widget.Row(row...).Gap(6).Align(widget.AlignCenter)).Pad(5, 8),
			hline(),
		).Stretch()
	})
	return widget.Column(
		header,
		widget.Expanded(bv.Body),
	).Stretch()
}
