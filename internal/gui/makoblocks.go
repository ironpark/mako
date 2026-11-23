package gui

import (
	"image/color"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/layoutdoc"
	"github.com/ironpark/mako/internal/protocol"
)

// The right panel's tabs, which the panel, a stack in the default
// document, shows one of: the details stack's active state holds one of
// these.
const (
	filesTab  = layoutdoc.BlockFiles
	gitTab    = layoutdoc.BlockGit
	agentsTab = layoutdoc.BlockAgents
	forgeTab  = layoutdoc.BlockForge

	// gitPluginBlock is the bundled Git plugin block aliased by mako.git.
	gitPluginBlock = "plugin:mako-git/panel"
)

// makoViews are what the window's own blocks draw, by name; layoutdoc
// declares the blocks themselves.
var makoViews = map[string]blockDef{
	layoutdoc.BlockSidebar: {Build: func(m *model, _ map[string]any, _ blockPlace) blockView {
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
	layoutdoc.BlockHeader: {Build: func(m *model, _ map[string]any, _ blockPlace) blockView { return blockView{Body: workspaceHeader(m)} }},
	filesTab:              {Chrome: chromePanel, Icon: "folder", Build: func(m *model, _ map[string]any, at blockPlace) blockView { return filesPanelView(m, at) }},
	gitTab: {Chrome: chromePanel, Icon: "git-branch",
		Build: func(m *model, _ map[string]any, at blockPlace) blockView {
			bv := treeBlockView(m, gitPluginBlock, blockOrigin{plugin: "mako-git"}, T("Git"), protocol.ScopeFolder, nil, at)
			bv.Body = withRootPicker(m, bv.Body)
			return bv
		}},
	agentsTab: {Chrome: chromePanel, Icon: "users", Build: func(m *model, _ map[string]any, at blockPlace) blockView { return projectAgentsPanelView(m, at) }},
	// The forge's tab is named for the project's forge.
	forgeTab: {Chrome: chromePanel, Icon: "git-pull-request", Label: func(m *model) string { return forgeLabel(m.forgeOf()) },
		Build: func(m *model, _ map[string]any, at blockPlace) blockView { return forgePanelView(m, at) }},
	layoutdoc.BlockAvatar: {Chrome: chromePanel, Icon: "bot", Build: func(m *model, _ map[string]any, _ blockPlace) blockView { return blockView{Body: avatarBlockPanel(m)} }},
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
