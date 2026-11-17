package gui

import (
	"image/color"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/layoutdoc"
)

// The right panel's tabs, which the panel, a stack in the default
// document, shows one of: the details stack's active state holds one of
// these.
const (
	filesTab  = layoutdoc.BlockFiles
	gitTab    = layoutdoc.BlockGit
	agentsTab = layoutdoc.BlockAgents
	forgeTab  = layoutdoc.BlockForge
)

// makoViews are what the window's own blocks draw, by name; layoutdoc
// declares the blocks themselves.
var makoViews = map[string]blockDef{
	layoutdoc.BlockSidebar: {Build: func(m *model, _ map[string]any) blockView {
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
	layoutdoc.BlockHeader: {Build: func(m *model, _ map[string]any) blockView { return blockView{Body: workspaceHeader(m)} }},
	filesTab:              {Build: func(m *model, _ map[string]any) blockView { return blockView{Body: filesPanel(m)} }},
	gitTab:                {Build: func(m *model, _ map[string]any) blockView { return blockView{Body: gitPanel(m)} }},
	agentsTab:             {Build: func(m *model, _ map[string]any) blockView { return blockView{Body: projectAgentsPanel(m)} }},
	// The forge's tab is named for the project's forge.
	forgeTab: {Label: func(m *model) string { return forgeLabel(m.forgeOf()) },
		Build: func(m *model, _ map[string]any) blockView { return blockView{Body: forgePanel(m)} }},
}

// builtinBlocks is a registry of the window's own blocks.
func builtinBlocks() *blockRegistry {
	r := newBlockRegistry()
	for _, b := range layoutdoc.Builtin() {
		d := makoViews[b.Name]
		d.Name, d.Title, d.Props = b.Name, b.Title, b.Props
		if err := r.register(d); err != nil {
			panic(err) // the window's own names are the code's
		}
	}
	return r
}
