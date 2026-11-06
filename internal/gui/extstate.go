package gui

import (
	"maps"
	"reflect"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/mcp"
	"github.com/ironpark/mako/internal/zed"
)

// extState is the Zed extensions' and the MCP servers': those installed,
// the store's search, and the settings page's. Its fields are the UI
// goroutine's.
type extState struct {
	// Installed are the extensions installed; langGen counts the changes
	// to the languages they give highlighting.
	Installed *ggui.StateValue[[]zed.Extension]
	langGen   int
	// Open shows the extensions dialog over the window.
	Open    *ggui.StateValue[bool]
	Results *ggui.StateValue[extSearch]
	Busy    *ggui.StateValue[map[string]bool] // extensions being installed
	Err     *ggui.StateValue[string]
	// What the extensions list is searched for and narrowed to.
	Query *ggui.StateValue[string]
	Show  *ggui.StateValue[string]
	Kind  *ggui.StateValue[string]
	// MCPServers mirrors cfg.ContextServers for the settings page, and
	// MCPInfo is what the extensions say about setting each up.
	MCPServers *ggui.StateValue[map[string]mcp.Server]
	MCPInfo    *ggui.StateValue[map[string]mcpInfo]
	MCPNote    *ggui.StateValue[string]
	mcpTexts   map[string]*ggui.StateValue[string]
}

func newExtState(cfg *config.Config) *extState {
	return &extState{
		Installed:  ggui.State([]zed.Extension(nil)).WithEqual(func(a, b []zed.Extension) bool { return reflect.DeepEqual(a, b) }),
		Open:       ggui.State(false),
		Results:    ggui.State(extSearch{}).WithEqual(func(a, b extSearch) bool { return reflect.DeepEqual(a, b) }),
		Busy:       ggui.State(map[string]bool{}).WithEqual(maps.Equal),
		Err:        ggui.State(""),
		Query:      ggui.State(""),
		Show:       ggui.State(extShowAll),
		Kind:       ggui.State(""),
		MCPServers: ggui.State(maps.Clone(cfg.ContextServers)).WithEqual(func(a, b map[string]mcp.Server) bool { return reflect.DeepEqual(a, b) }),
		MCPInfo:    ggui.State(map[string]mcpInfo(nil)).WithEqual(func(a, b map[string]mcpInfo) bool { return reflect.DeepEqual(a, b) }),
		MCPNote:    ggui.State(""),
	}
}
