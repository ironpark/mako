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

// Left sidebar navigation menu for plugin main views (revision 49).
//
// Any active plugin block whose manifest includes "sidebar" in its slots adds a
// navigation menu item to the left sidebar above Projects. Activating an item
// fills the central workspace content area with that plugin's view while the
// left sidebar (and its Projects/Agents lists) stays visible for instant
// navigation back. Clicking the active item again, selecting any project or
// session, or opening a tab returns the central area to the workspace panes.

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

// activeSidebarPageDef resolves the currently active sidebar plugin page for
// active, returning false when none is selected or its plugin is no longer
// active. Reactive: reads active and m.plugins.List.
func activeSidebarPageDef(m *model, active *ggui.StateValue[string]) (sidebarPageDef, bool) {
	if active == nil {
		return sidebarPageDef{}, false
	}
	cur := active.Get()
	if cur == "" {
		return sidebarPageDef{}, false
	}
	return findSidebarPage(sidebarPluginBlocks(m.plugins.List.Get()), cur)
}

// activeSidebarPageDerived derives the validated active block name ("", or
// "plugin:<id>/<block>") so callers can key on page transitions cleanly.
func activeSidebarPageDerived(m *model, active *ggui.StateValue[string]) *ggui.DerivedValue[string] {
	return ggui.Derived(func() string {
		if pg, ok := activeSidebarPageDef(m, active); ok {
			return pg.Block
		}
		return ""
	})
}

// openSidebarPage activates blockName in the central content area ("" returns
// to the workspace panes) and exits Settings if open.
func (m *model) openSidebarPage(blockName string) {
	if m.Page != nil && ggui.Peek(m.Page) != "workspace" {
		m.Page.Set("workspace")
	}
	if m.SidebarPage != nil {
		m.SidebarPage.Set(blockName)
	}
}

// clearSidebarPage returns the central content area to the workspace panes.
func (m *model) clearSidebarPage() {
	if m.SidebarPage != nil && ggui.Peek(m.SidebarPage) != "" {
		m.SidebarPage.Set("")
	}
}

// sidebarPluginMenu renders the left-sidebar navigation menu items contributed
// by active "sidebar"-slotted plugin blocks. Renders nothing when no plugin
// contributes one so the default sidebar stays untouched.
func sidebarPluginMenu(m *model, active *ggui.StateValue[string]) ggui.Widget {
	return ggui.Reactive(func() ggui.Widget {
		pages := sidebarPluginBlocks(m.plugins.List.Get())
		if len(pages) == 0 {
			return widget.Box()
		}
		cur := ""
		if active != nil {
			cur = active.Get()
		}
		if cur != "" {
			if _, ok := findSidebarPage(pages, cur); !ok {
				cur = ""
			}
		}
		rows := make([]ggui.Widget, 0, len(pages))
		for _, pg := range pages {
			p := pg
			selected := cur == p.Block
			iconCol := pick[color.Color](selected, skin.AccentBright, skin.Muted)
			textCol := pick[color.Color](selected, skin.Fg, skin.Muted)
			weight := pick(selected, ggui.WeightSemibold, ggui.WeightMedium)
			item := ui.ButtonOf(widget.Row(
				pluginIcon(p.Icon).Size(14).Color(iconCol),
				widget.Expanded(widget.Text(p.Title).Size(13).Weight(weight).Color(textCol).NoWrap().Ellipsis()),
			).Gap(9).Align(widget.AlignCenter), func() {
				if active == nil {
					return
				}
				if ggui.Peek(active) == p.Block {
					active.Set("")
				} else {
					if m.Page != nil && ggui.Peek(m.Page) != "workspace" {
						m.Page.Set("workspace")
					}
					active.Set(p.Block)
				}
			}).Ghost().Pad(6, 10).Radius(7).Name(p.Title)
			var row ggui.Widget = item
			if selected {
				row = widget.Box(item).Fill(skin.SidebarSel).Radius(7)
			}
			rows = append(rows, row)
		}
		list := ggui.Widget(widget.Column(rows...).Gap(2).Stretch())
		if len(pages) > 5 {
			list = widget.Box(widget.Scroll(list).Bar(skin.Surface3)).MaxHeight(180)
		}
		return looseHeight{widget.Column(
			widget.Box(list).Pad(6, 8),
			hline(),
		).Stretch()}
	})
}

// workspaceCenterView renders either the active sidebar plugin page filling
// the central workspace area or the normal workspace panes when none is active.
func workspaceCenterView(m *model) ggui.Widget {
	active := activeSidebarPageDerived(m, m.SidebarPage)
	return crossfade(active, func(blockName string) ggui.Widget {
		if blockName == "" {
			return layoutView(m)
		}
		pg, ok := findSidebarPage(sidebarPluginBlocks(ggui.Peek(m.plugins.List)), blockName)
		if !ok {
			return layoutView(m)
		}
		return mainPluginPageView(m, pg)
	})
}

// pluginMainPageHeaderBar renders the central workspace header bar when a
// sidebar plugin view is active.
func pluginMainPageHeaderBar(m *model, pg sidebarPageDef, onClose func()) ggui.Widget {
	var items []ggui.Widget
	if pg.PluginID != "" {
		pid := pg.PluginID
		if p := m.pluginOf(pid); p != nil && !p.GetInstalled() && !p.GetBundled() {
			items = append(items, ui.MenuItem(T("Reload plugin"), func() {
				m.actOnPlugin(&protocol.PluginAction{PluginId: pid, Op: protocol.PluginReload})
			}))
		}
	}
	if len(items) > 0 {
		items = append(items, ui.MenuDivider())
	}
	items = append(items, ui.MenuItem(T("Close view"), onClose))

	row := []ggui.Widget{
		iconButton("arrow-left", T("Back to workspace"), onClose),
		pluginIcon(pg.Icon).Size(14).Color(skin.AccentBright),
		widget.Text(pg.Title).Size(13.5).Weight(ggui.WeightSemibold).NoWrap().Ellipsis(),
		widget.Spacer(),
	}
	if badge := provenanceBadge(m, blockOrigin{plugin: pg.PluginID}); badge != nil {
		row = append(row, badge)
	}
	row = append(row, iconMenu(ui.MenuOf(icon("ellipsis"), items...).Name(T("View actions")), 6))
	return widget.Row(row...).Gap(8).Align(widget.AlignCenter)
}

// mainPluginPageView renders one sidebar-activated plugin block filling the
// full central workspace area, lifting any leading toolbar row into a sleek
// top action strip over the full-size body.
func mainPluginPageView(m *model, pg sidebarPageDef) ggui.Widget {
	return mainPluginPageViewIn(m, pg, nil)
}

func mainPluginPageViewIn(m *model, pg sidebarPageDef, sw *spaceWindowState) ggui.Widget {
	bv := treeBlockView(m, pg.Block, blockOrigin{plugin: pg.PluginID}, pg.Title, pg.Scope, nil, blockPlace{Solo: true, SpaceWindow: sw})
	showScope := isScoped(pg.Scope)
	toolbar := ggui.Reactive(func() ggui.Widget {
		hasBar := bv.hasToolbar != nil && bv.hasToolbar.Get()
		if !hasBar && !showScope {
			return widget.Box()
		}
		var row []ggui.Widget
		if hasBar && bv.Tools != nil {
			row = append(row, widget.Expanded(bv.Tools))
		} else {
			row = append(row, widget.Spacer())
		}
		if showScope && bv.Accessory != nil {
			row = append(row, bv.Accessory)
		}
		return widget.Column(
			widget.Box(widget.Row(row...).Gap(8).Align(widget.AlignCenter)).Pad(6, 12).Fill(skin.Surface1),
			hline(),
		).Stretch()
	})
	return widget.Box(widget.Column(
		toolbar,
		widget.Expanded(bv.Body),
	).Stretch()).Fill(skin.Surface0)
}
