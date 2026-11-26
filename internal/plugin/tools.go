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
	Scope       string
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
			if tl.Scope == protocol.ScopeFolder {
				continue
			}
			out = append(out, ContributedTool{
				PluginID:    p.id,
				ID:          tl.ID,
				MCPName:     MCPToolName(p.id, tl.ID),
				Description: fmt.Sprintf("[%s plugin] %s", p.id, tl.Description),
				Scope:       tl.Scope,
				Schema:      toolSchema(tl),
			})
		}
	}
	return out
}

// CallTool invokes a contributed MCP tool on pluginID with an empty/global
// ScopeContext, ensuring the plugin and its blocks are initialized and
// re-rendering watched blocks afterwards.
func (m *Manager) CallTool(ctx context.Context, pluginID, toolID string, args json.RawMessage) (string, error) {
	return m.CallToolScoped(ctx, pluginID, toolID, args, ScopeContext{})
}

// CallToolScoped invokes a contributed MCP tool on pluginID within the given
// ScopeContext, validating the tool's declared scope, ensuring matching block
// instances are loaded, and re-rendering watched instances for that scope (and
// global blocks) afterwards.
func (m *Manager) CallToolScoped(ctx context.Context, pluginID, toolID string, argsJSON []byte, scope ScopeContext) (string, error) {
	p, ok := m.plugin(pluginID)
	if !ok {
		return "", noPlugin(pluginID)
	}
	v := p.current()
	var decl *ToolDecl
	if v != nil {
		for i := range v.man.Tools {
			if v.man.Tools[i].ID == toolID {
				decl = &v.man.Tools[i]
				break
			}
		}
	}
	if decl == nil {
		return "", fmt.Errorf("plugin %s declares no tool %q", pluginID, toolID)
	}
	resolvedScope, err := m.resolveToolScope(decl.Scope, scope)
	if err != nil {
		return "", err
	}
	if len(argsJSON) == 0 {
		argsJSON = []byte(`{}`)
	}
	if !json.Valid(argsJSON) {
		return "", fmt.Errorf("invalid tool arguments JSON")
	}
	scopeKey := resolvedScope.ScopeKey
	ctxJSON := marshalScopeContext(resolvedScope)
	var out string
	err = p.do(ctx, func() error {
		if err := p.ensure(); err != nil {
			return err
		}
		// Ensure declared blocks matching this tool's scope (plus global blocks)
		// have instances and have loaded their persisted state so tools mutating
		// mako.blocks.state(...) operate on live state.
		var unloaded []*instance
		var evicted []string
		p.mu.Lock()
		if cur := p.v; cur != nil {
			for _, b := range cur.man.Blocks {
				switch {
				case b.Scope == "" || b.Scope == protocol.ScopeGlobal:
					in, ev := p.getOrCreateInstanceLocked(b.ID, "", "", "")
					evicted = append(evicted, ev...)
					if !in.loaded {
						unloaded = append(unloaded, in)
					}
				case scopeKey != "" && b.Scope == resolvedScope.Scope:
					in, ev := p.getOrCreateInstanceLocked(b.ID, scopeKey, "", "")
					evicted = append(evicted, ev...)
					if !in.loaded {
						unloaded = append(unloaded, in)
					}
				}
			}
		}
		for _, in := range p.inst {
			if !in.loaded && (in.scopeKey == scopeKey || in.scopeKey == "") && !slices.Contains(unloaded, in) {
				unloaded = append(unloaded, in)
			}
		}
		p.mu.Unlock()
		p.evictInstances(evicted)
		for _, in := range unloaded {
			p.load(in)
		}
		res, err := p.invoke("callTool", false, toolID, string(argsJSON), ctxJSON)
		if err != nil {
			return err
		}
		out = res
		p.afterCall(scopeKey)
		return nil
	})
	return out, err
}
