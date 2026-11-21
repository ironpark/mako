package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/ironpark/mako/internal/protocol"
)

// ContributedTool is an MCP tool declared by an active, permitted plugin and
// exposed to agents running on this node.
type ContributedTool struct {
	PluginID    string
	ID          string
	MCPName     string
	Description string
	Schema      map[string]any
}

// MCPToolName builds a stable, valid MCP tool identifier for a plugin tool.
func MCPToolName(pluginID, toolID string) string {
	return "plugin_" + strings.ReplaceAll(pluginID, "-", "_") + "_" + toolID
}

// toolSchema converts a manifest's ToolDecl parameters into a strict JSON Schema.
func toolSchema(t ToolDecl) map[string]any {
	props := map[string]any{}
	var req []string
	for _, p := range t.Params {
		prop := map[string]any{"type": p.Type}
		if p.Description != "" {
			prop["description"] = p.Description
		}
		props[p.Name] = prop
		if p.Required {
			req = append(req, p.Name)
		}
	}
	schema := map[string]any{
		"type":                 "object",
		"properties":           props,
		"additionalProperties": false,
	}
	if len(req) > 0 {
		schema["required"] = req
	}
	return schema
}

// Tools lists every MCP tool declared by plugins that the user has allowed and
// enabled on this node.
func (m *Manager) Tools() []ContributedTool {
	var out []ContributedTool
	for _, p := range m.all() {
		state, _ := p.state()
		if state != protocol.PluginReady && state != protocol.PluginRunning {
			continue
		}
		v := p.current()
		if v == nil || len(v.man.Tools) == 0 {
			continue
		}
		for _, tl := range v.man.Tools {
			out = append(out, ContributedTool{
				PluginID:    p.id,
				ID:          tl.ID,
				MCPName:     MCPToolName(p.id, tl.ID),
				Description: fmt.Sprintf("[%s plugin] %s", p.id, tl.Description),
				Schema:      toolSchema(tl),
			})
		}
	}
	return out
}

// CallTool invokes a contributed MCP tool on pluginID, ensuring the plugin and
// its blocks are initialized and re-rendering any watched blocks afterwards.
func (m *Manager) CallTool(ctx context.Context, pluginID, toolID string, args json.RawMessage) (string, error) {
	p, ok := m.plugin(pluginID)
	if !ok {
		return "", noPlugin(pluginID)
	}
	v := p.current()
	if v == nil || !slices.ContainsFunc(v.man.Tools, func(t ToolDecl) bool { return t.ID == toolID }) {
		return "", fmt.Errorf("plugin %s declares no tool %q", pluginID, toolID)
	}
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	if !json.Valid(args) {
		return "", fmt.Errorf("invalid tool arguments JSON")
	}
	var out string
	err := p.do(ctx, func() error {
		if err := p.ensure(); err != nil {
			return err
		}
		// Ensure declared blocks have loaded their initial persisted state so
		// tools mutating mako.blocks.state(...) operate on live state.
		p.mu.Lock()
		var unloaded []*instance
		for _, in := range p.inst {
			if !in.loaded {
				unloaded = append(unloaded, in)
			}
		}
		p.mu.Unlock()
		for _, in := range unloaded {
			p.load(in)
		}
		res, err := p.invoke("callTool", false, toolID, string(args))
		if err != nil {
			return err
		}
		out = res
		p.afterCall()
		return nil
	})
	return out, err
}
