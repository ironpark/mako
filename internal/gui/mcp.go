package gui

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"maps"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/mcp"
	"github.com/ironpark/mako/internal/zed"
	"github.com/ironpark/mako/internal/zedhost"
)

// Settings ▸ Extensions also lists the MCP servers (Zed's context
// servers) of the extensions installed, which this node's daemon gives its
// claude and codex sessions, and edits their settings in config.json.

// mcpInfo is what the page knows of a server: the extension that has it
// and what its wasm says about setting it up.
type mcpInfo struct {
	Extension string // its name
	Config    *zedhost.ContextServerConfiguration
	Err       string
}

// loadMCPInfo asks each installed extension with context servers how to
// set them up, off the window's goroutine: it runs their wasm.
func (m *model) loadMCPInfo(exts []zed.Extension) {
	go func() {
		work, err := config.ExtensionsWorkDir()
		if err != nil {
			return
		}
		info := map[string]mcpInfo{}
		for _, e := range exts {
			for _, id := range e.ContextServerIDs() {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				cfg, err := mcp.Configuration(ctx, e, work, id)
				cancel()
				i := mcpInfo{Extension: e.Name, Config: cfg}
				if err != nil {
					log.Printf("mako: %s: %v", id, err)
					i.Err = err.Error()
				}
				info[id] = i
			}
		}
		m.post(func() { m.MCPInfo.Set(info) })
	}()
}

// saveMCPServer writes a server's configuration to config.json, where the
// daemon reads it for the next session.
func (m *model) saveMCPServer(id string, s mcp.Server) {
	next := *m.cfg
	next.ContextServers = maps.Clone(m.cfg.ContextServers)
	if next.ContextServers == nil {
		next.ContextServers = map[string]mcp.Server{}
	}
	if s.Enabled == nil && len(s.Settings) == 0 && s.Command == nil {
		delete(next.ContextServers, id) // at its defaults, it needs no entry
	} else {
		next.ContextServers[id] = s
	}
	if err := config.Save(&next); err != nil {
		m.MCPNote.Set(Tf("Could not save: %v", err))
		return
	}
	*m.cfg = next
	m.MCPServers.Set(maps.Clone(next.ContextServers))
	m.MCPNote.Set(T("Saved. Claude Code and Codex sessions started from now on use it."))
}

// mcpText is the settings field of a server, kept across the section's
// rebuilds.
func (m *model) mcpText(id string) *ggui.StateValue[string] {
	if t, ok := m.mcpTexts[id]; ok {
		return t
	}
	t := ggui.State(settingsText(m.cfg.ContextServers[id].Settings))
	if m.mcpTexts == nil {
		m.mcpTexts = map[string]*ggui.StateValue[string]{}
	}
	m.mcpTexts[id] = t
	return t
}

// settingsText is settings as the field shows them, indented.
func settingsText(settings json.RawMessage) string {
	if len(settings) == 0 || string(settings) == "null" {
		return ""
	}
	var b bytes.Buffer
	if json.Indent(&b, settings, "", "  ") != nil {
		return string(settings)
	}
	return b.String()
}

// mcpServerCard is an extension's MCP server: whether sessions get it,
// how it starts, and its settings.
func mcpServerCard(m *model, id, ext string) ggui.Widget {
	return ggui.Reactive(func() ggui.Widget {
		mono := ggui.TextStyle{Font: ggui.DefaultMonoFont()}
		conf := m.MCPServers.Get()[id]
		i := m.MCPInfo.Get()[id]
		on := ggui.Bind(func() bool { return ggui.Peek(m.MCPServers)[id].On() }, func(v bool) {
			next := ggui.Peek(m.MCPServers)[id]
			next.Enabled = nil
			if !v {
				next.Enabled = &v
			}
			m.saveMCPServer(id, next)
		})
		rows := []ggui.Widget{row(id, ext, ui.Switch(on, "").Name(Tf("%s enabled", id)))}
		if conf.Command != nil {
			rows = append(rows, row(T("Command"), strings.Join(append([]string{conf.Command.Path}, conf.Command.Args...), " "), ggui.Box()))
		}
		text := m.mcpText(id)
		defaults, placeholder, hint := "", "", ""
		if i.Config != nil {
			defaults = strings.TrimSpace(i.Config.DefaultSettings)
			hint = strings.TrimSpace(i.Config.InstallationInstructions)
			// A placeholder is one line: the defaults without their
			// comments, which the button fills in whole.
			if d, err := mcp.ParseSettings(defaults); err == nil {
				placeholder = string(d)
			}
		}
		if i.Err != "" {
			hint = i.Err
		}
		field := ui.TextField(text).Multiline().Lines(5).Style(mono).Placeholder(placeholder).Name(Tf("%s settings", id))
		buttons := []ggui.Widget{
			ui.Button(T("Save settings"), func() {
				settings, err := mcp.ParseSettings(ggui.Peek(text))
				if err != nil {
					m.MCPNote.Set(Tf("%s: the settings are not JSON: %v", id, err))
					return
				}
				next := ggui.Peek(m.MCPServers)[id]
				next.Settings = settings
				m.saveMCPServer(id, next)
			}).Outline(),
		}
		if defaults != "" {
			buttons = append(buttons, ui.Button(T("Fill in defaults"), func() { text.Set(defaults) }).Ghost())
		}
		rows = append(rows, stackedRow(T("Settings"), hint,
			ggui.Column(field, ggui.Row(buttons...).Gap(8)).Gap(8).Stretch()))
		note := ggui.View(m.MCPNote, func(s string) ggui.Widget {
			if s == "" {
				return ggui.Box()
			}
			return muted(ggui.Text(s).Size(12.5))
		})
		return ggui.Column(card(rows...), note).Gap(6).Stretch()
	})
}
