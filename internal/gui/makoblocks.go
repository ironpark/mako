package gui

import (
	"image/color"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/gui/i18n"
	"github.com/ironpark/mako/internal/gui/skin"
)

// The window's own blocks. The panes, the tab groups' split tree, are not
// one: a document places them with a panes node. Sessions' chat and
// terminal views, and the composer in a chat, are drawn in the panes' tabs,
// so they are not blocks of their own either; the window has no status
// bar.
const (
	blockSidebar = "mako.sidebar"
	blockHeader  = "mako.header"
	// The right panel's tabs, which the panel, a stack in the default
	// document, shows one of: the details stack's active state holds one
	// of these.
	filesTab  = "mako.files"
	gitTab    = "mako.git"
	agentsTab = "mako.agents"
	forgeTab  = "mako.forge"
)

// builtinBlocks is a registry of the window's own blocks.
func builtinBlocks() *blockRegistry {
	r := newBlockRegistry()
	for _, d := range []blockDef{
		{Name: blockSidebar, Title: i18n.N("Sidebar"), Build: func(m *model, _ map[string]any) blockView {
			// The space's colour washes the sidebar and the edge beside it
			// as one.
			tint := spaceTint(m)
			return blockView{
				Body: sidebarIn(m, tint),
				Edge: func(line ggui.Widget, vertical bool) ggui.Widget {
					return ggui.View(tint, func(c color.NRGBA) ggui.Widget {
						return edgeBox(line, vertical).Fill(skin.Sidebar).Gradient(spaceWash(c))
					})
				},
			}
		}},
		{Name: blockHeader, Title: i18n.N("Session header"), Build: func(m *model, _ map[string]any) blockView {
			return blockView{Body: workspaceHeader(m)}
		}},
		{Name: filesTab, Title: i18n.N("Files"), Build: func(m *model, _ map[string]any) blockView {
			return blockView{Body: filesPanel(m)}
		}},
		{Name: gitTab, Title: i18n.N("Git"), Build: func(m *model, _ map[string]any) blockView {
			return blockView{Body: gitPanel(m)}
		}},
		{Name: agentsTab, Title: i18n.N("Agents"), Build: func(m *model, _ map[string]any) blockView {
			return blockView{Body: projectAgentsPanel(m)}
		}},
		// The forge's tab is named for the project's forge.
		{Name: forgeTab, Title: i18n.N("Forge"), Label: func(m *model) string { return forgeLabel(m.forgeOf()) },
			Build: func(m *model, _ map[string]any) blockView { return blockView{Body: forgePanel(m)} }},
	} {
		if err := r.register(d); err != nil {
			panic(err) // the window's own names are the code's
		}
	}
	return r
}
