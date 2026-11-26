package gui

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/runtime"
	"github.com/ironpark/ggui/ui"
	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/layoutdoc"
	"github.com/ironpark/mako/internal/protocol"
)

// The daemon's plugins (internal/plugin), as the window shows them: their
// blocks registered as plugin:<id>/<block> while the daemon lists them,
// drawn from the trees it sends (pluginblock.go), framed with where they
// come from; their commands in the palette; their settings and what they
// ask for in Settings ▸ Plugins (pluginsettings.go). What a plugin asks
// for is answered in a bar the window draws itself, never in a plugin's
// block.

// pluginsState is the plugins as the daemon last listed them.
type pluginsState struct {
	List *ggui.StateValue[[]*protocol.PluginInfo]
	// Later are the plugins whose asking the user put off in this window.
	Later *ggui.StateValue[map[string]bool]
	// Secrets is where the daemon keeps its plugins' secrets: the
	// keychain's name, or "file" with why; "" from an older daemon.
	Secrets *ggui.StateValue[secretStore]
	// SettingScope is the scope currently picked in Settings ▸ Plugins for
	// each plugin ID ("" for global default).
	SettingScope *ggui.StateValue[map[string]string]
	// blockCache preserves the last BlockUpdate feed and blockForm per
	// (block, scope) across solo <-> tabbed WinDoc remounts. UI goroutine only.
	blockCache map[blockCacheKey]*cachedBlockState
}

// secretStore is where a node keeps its plugins' secrets.
type secretStore struct{ Store, Note string }

func newPluginsState() *pluginsState {
	return &pluginsState{
		List: ggui.State([]*protocol.PluginInfo(nil)).WithEqual(func(a, b []*protocol.PluginInfo) bool {
			if len(a) != len(b) {
				return false
			}
			for i := range a {
				if !proto.Equal(a[i], b[i]) {
					return false
				}
			}
			return true
		}),
		Later:        ggui.State(map[string]bool{}),
		Secrets:      ggui.State(secretStore{}),
		SettingScope: ggui.State(map[string]string{}).WithEqual(maps.Equal[map[string]string]),
		blockCache:   map[blockCacheKey]*cachedBlockState{},
	}
}

// scopedBlockProps is the layoutdoc props schema for a scoped plugin block,
// permitting an optional pinned "scope" key (e.g. "project:<id>").
func scopedBlockProps() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"scope": map[string]any{"type": "string"},
		},
		"additionalProperties": false,
	}
}

// setPlugins takes the daemon's listing in, registering their blocks.
func (m *model) setPlugins(list []*protocol.PluginInfo) {
	m.setPluginList(&protocol.Plugins{Plugins: list})
}

// setPluginList takes the daemon's listing in, with where it keeps the
// plugins' secrets.
func (m *model) setPluginList(pl *protocol.Plugins) {
	m.plugins.Secrets.Set(secretStore{pl.GetSecrets(), pl.GetSecretsNote()})
	list := pl.GetPlugins()
	m.plugins.List.Set(list)
	var defs []blockDef
	for _, p := range list {
		for _, b := range p.GetBlocks() {
			name, title, id, scope := b.GetName(), b.GetTitle(), p.GetId(), b.GetScope()
			def := blockDef{
				Name:   name,
				Title:  title,
				Scope:  scope,
				Chrome: chromePanel,
				Icon:   cmp.Or(b.GetIcon(), "package"),
				Build: func(m *model, props map[string]any, at blockPlace) blockView {
					return pluginBlockView(m, name, id, title, scope, props, at)
				},
			}
			if isScoped(scope) {
				def.Props = scopedBlockProps()
			}
			defs = append(defs, def)
		}
	}
	m.blocks.replaceOf("plugin:", defs)
}

// pluginOf is the plugin of the ID as listed, nil for none.
func (m *model) pluginOf(id string) *protocol.PluginInfo {
	for _, p := range ggui.Peek(m.plugins.List) {
		if p.GetId() == id {
			return p
		}
	}
	return nil
}

// isBundledPlugin reports whether plugin id is shipped bundled inside Mako.
func (m *model) isBundledPlugin(id string) bool {
	if id == "" {
		return false
	}
	if m != nil {
		if p := m.pluginOf(id); p != nil {
			return p.GetBundled()
		}
	}
	return strings.HasPrefix(id, "mako-")
}

// pluginBundledBadge is the compact "Built-in" pill shown for bundled plugins.
func pluginBundledBadge() ggui.Widget {
	return pill(T("Built-in"), skin.AccentBright)
}

// actOnPlugin asks the daemon to act on a plugin for the user, and lists
// them again; a link the plugin asked to open in answer is opened.
func (m *model) actOnPlugin(req *protocol.PluginAction) {
	if m.post == nil {
		return
	}
	if req != nil && req.Locale == "" {
		req = proto.CloneOf(req)
		req.Locale = m.currentLocale()
	}
	fetch(m, 30*time.Second, func(ctx context.Context, c *client.Client) (*protocol.PluginDone, error) {
		return c.PluginAction(ctx, req)
	}, func(done *protocol.PluginDone, err error) {
		if err != nil {
			m.connErr(errors.New(errorText(err)))
		} else {
			m.handlePluginDone(done)
		}
		m.link.pluginsStale.Store(true)
		m.link.pollNow()
	})
}

// blockEvent tells a block's plugin or agent of the user acting on a node
// of it, with the block's fields as they stand.
func (m *model) blockEvent(block, scope, node, kind, value string, values map[string]string) {
	m.blockEventInstance(block, scope, "", node, kind, value, values)
}

// blockEventInstance tells a block instance (such as a custom plugin tab) of
// the user acting on a node of it.
func (m *model) blockEventInstance(block, scope, instance, node, kind, value string, values map[string]string) {
	if m.post == nil {
		return
	}
	req := &protocol.BlockEvent{Block: block, Scope: scope, Instance: instance, Node: node, Kind: kind, Value: value, Values: values, Locale: m.currentLocale()}
	fetch(m, 30*time.Second, func(ctx context.Context, c *client.Client) (*protocol.PluginDone, error) {
		return c.BlockEvent(ctx, req)
	}, func(done *protocol.PluginDone, err error) {
		if err != nil {
			m.connErr(err)
			return
		}
		m.handlePluginDone(done)
	})
}

func (m *model) showPluginToast(msg string) {
	if msg == "" || m.toaster == nil {
		return
	}
	m.toaster.Push(ui.Toast(msg, ""))
}

// openPluginLink opens a link a plugin asked to open in answer to the
// user, if it is a web link: the daemon checked it, and it is checked
// again here, where it is opened.
func openPluginLink(url string) {
	if strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://") {
		_ = runtime.OpenURL(url)
	}
}

// permissionText says what a permission lets a plugin do, for the user
// deciding.
func permissionText(p string) string {
	if host, ok := strings.CutPrefix(p, "net:"); ok {
		return Tf("Connect to %s", host)
	}
	switch p {
	case "storage":
		return T("Keep data on this computer")
	case "db":
		return T("Keep structured SQLite databases on this computer")
	case "db:sync":
		return T("Sync plugin database tables across your paired computers")
	case "fs:data":
		return T("Keep files and folders in this plugin's private data directory")
	case "sessions:read":
		return T("See this node's sessions and what their agents are doing")
	case "sessions:write":
		return T("Send messages to sessions, and start and stop agents")
	case "fs:project":
		return T("Read the files of this node's projects")
	case "fs:project:write":
		return T("Change the files of this node's projects, which can change what their builds run")
	case "layout:write":
		return T("Change the window layout")
	case "secrets":
		return T("Keep secrets in the system keychain")
	case "decision":
		return T("Ask the decision model short typed questions, with the API keys set in Settings")
	case "agents:ask":
		return T("List this node's agents and ask them questions, which runs them and may cost money")
	case "git:read":
		return T("Read git status, branches, stashes, and commit history in this node's repositories")
	case "git:write":
		return T("Run git commands such as commit, checkout, pull, push, and stash in this node's repositories")
	case "forge:read":
		return T("Read pull requests and CI workflow status for this node's repositories")
	}
	return p
}

// pluginCommands are the plugins' commands and block open/close actions, for
// the palette: named for their plugin, so none passes for one of the window's.
func (m *model) pluginCommands() []command {
	var out []command
	doc := ggui.Peek(m.WinDoc)
	for _, p := range ggui.Peek(m.plugins.List) {
		usable := p.GetState() == protocol.PluginReady || p.GetState() == protocol.PluginRunning
		for _, c := range p.GetCommands() {
			id, cmd := p.GetId(), c.GetId()
			out = append(out, command{ID: c.GetTitle(), Category: id, Keys: []string{"plugin", id, cmd}, Disabled: !usable,
				Run: func() { m.actOnPlugin(&protocol.PluginAction{PluginId: id, Op: protocol.PluginRun, Command: cmd}) }})
		}
		for _, b := range p.GetBlocks() {
			blockName, title, pid, bIcon, bScope := b.GetName(), b.GetTitle(), p.GetId(), b.GetIcon(), b.GetScope()
			bSlots := b.GetSlots()
			hasSidebar := slices.Contains(bSlots, "sidebar")
			hasTab := slices.Contains(bSlots, "tab")
			hasPanelSlot := len(bSlots) == 0 || slices.ContainsFunc(bSlots, func(s string) bool {
				return s == "left" || s == "right" || s == "top" || s == "bottom"
			})
			if hasSidebar {
				out = append(out, command{
					ID: Tf("Open %s view", title), Category: pid,
					Keys: []string{"plugin", "sidebar", "page", "view", pid, title, blockName},
					Run:  func() { m.openSidebarPage(blockName) },
				})
			}
			if hasTab {
				out = append(out, command{
					ID: Tf("Open %s tab", title), Category: pid,
					Keys: []string{"plugin", "tab", "open", pid, title, blockName},
					Run: func() {
						scopeKey := ""
						if isScoped(bScope) {
							scopeKey = ggui.Untrack(func() string { return m.resolveActiveScope(bScope) })
						}
						m.openPluginTab(pluginTab{
							Block:    blockName,
							PluginID: pid,
							Key:      "default",
							Title:    title,
							Icon:     bIcon,
							Scope:    scopeKey,
						})
					},
				})
			}
			if !hasPanelSlot {
				continue
			}
			placed := doc != nil && doc.BlockPath(blockName) != nil
			if placed {
				out = append(out,
					command{ID: Tf("Show %s", title), Category: pid, Keys: []string{"plugin", "block", "open", pid, title, blockName},
						Run: func() { m.revealBlock(blockName) }},
					command{ID: Tf("Close %s", title), Category: pid, Keys: []string{"plugin", "block", "close", "remove", pid, title, blockName},
						Run: func() { m.removePluginBlock(blockName) }},
				)
			} else {
				defaultPlace := "right-stack"
				if slices.Contains(bSlots, "bottom") {
					defaultPlace = "bottom"
				}
				out = append(out,
					command{ID: Tf("Open %s", title), Category: pid, Keys: []string{"plugin", "block", "open", "show", pid, title, blockName},
						Run: func() { m.placePluginBlock(blockName, defaultPlace) }},
				)
			}
		}
	}
	return out
}

// pluginBlockNodeID makes a valid, unique layout node ID for a placed plugin block.
func pluginBlockNodeID(doc *layoutdoc.Doc, blockName string) string {
	base := strings.TrimPrefix(blockName, "plugin:")
	base = strings.NewReplacer("/", "-", ":", "-", "_", "-", ".", "-").Replace(base)
	if base == "" {
		base = "plugin-block"
	}
	id := base
	for i := 2; doc != nil && doc.Node(id) != nil; i++ {
		id = base + "-" + string(rune('0'+i%10))
		if i >= 10 {
			id = base + "-x"
		}
	}
	return id
}

// activeLayoutDoc returns the active space's layout document name and parsed doc.
func (m *model) activeLayoutDoc() (string, *layoutdoc.Doc) {
	p := m.spacesNow()
	docName := layoutdoc.DefaultName
	if sp, ok := p.spaceOf(p.Space); ok && sp.Layout != "" {
		docName = sp.Layout
	}
	if raw := p.WindowLayouts[docName]; len(raw) > 0 {
		if d, err := layoutdoc.Parse(raw, m.blocks); err == nil {
			return docName, d
		}
	}
	return docName, layoutdoc.Default()
}

// placePluginBlock puts a plugin block into the active window layout document
// ("right-stack", "right-column", or "bottom") directly as the user's own edit.
func (m *model) placePluginBlock(blockName, placement string) {
	docName, doc := m.activeLayoutDoc()
	// Remove any existing occurrence first so switching placement moves cleanly.
	var ops []layoutdoc.Op
	if path := doc.BlockPath(blockName); len(path) > 0 {
		leaf := path[len(path)-1]
		if leaf.ID != "" {
			ops = append(ops, layoutdoc.Op{Op: "remove", ID: leaf.ID})
		}
	}
	nodeID := pluginBlockNodeID(doc, blockName)
	switch placement {
	case "right-column":
		ops = append(ops, layoutdoc.Op{
			Op: "insert", Parent: doc.Root.ID,
			Node: map[string]any{"id": nodeID, "block": blockName, "size": 460, "collapsible": true},
		})
	case "bottom":
		parent := "workspace"
		if doc.Node(parent) == nil || doc.Node(parent).Kind != layoutdoc.KindColumn {
			parent = doc.Root.ID
		}
		ops = append(ops, layoutdoc.Op{
			Op: "insert", Parent: parent,
			Node: map[string]any{"id": nodeID, "block": blockName, "size": 320, "collapsible": true},
		})
	default: // "right-stack"
		if st := firstStackIn(doc.Node(layoutdoc.DetailsNode)); st != nil {
			ops = append(ops,
				layoutdoc.Op{Op: "insert", Parent: st.ID, Node: blockName},
				layoutdoc.Op{Op: "select", ID: st.ID, Block: blockName},
			)
		} else {
			ops = append(ops, layoutdoc.Op{
				Op: "insert", Parent: doc.Root.ID,
				Node: map[string]any{"id": nodeID, "block": blockName, "size": 380, "collapsible": true},
			})
		}
	}
	next, err := layoutdoc.Apply(doc, ops, m.blocks)
	if err != nil {
		m.tellWindowDoc(err)
		return
	}
	m.commitLayoutDoc(docName, next, blockName)
}

// removePluginBlock removes a plugin block from the active window layout document.
func (m *model) removePluginBlock(blockName string) {
	docName, doc := m.activeLayoutDoc()
	path := doc.BlockPath(blockName)
	if len(path) == 0 {
		return
	}
	leaf := path[len(path)-1]
	next := doc.Clone()
	m.syncStackActives(next)
	if !removeNodeAndCollapse(next, leaf.ID) {
		return
	}
	m.evictCachedBlock(blockName)
	m.commitLayoutDoc(docName, next, "")
}

// setPluginBlockScope updates props["scope"] on the active layout document's
// node for blockName (matching oldScope first when multiple instances exist),
// persisting via layoutdoc.Apply and setWindowLayout. Passing newScope == ""
// removes the pinned scope so the block follows the active workspace context.
func (m *model) setPluginBlockScope(blockName, oldScope, newScope string) {
	docName, doc := m.activeLayoutDoc()
	path := doc.PathTo(func(n *layoutdoc.Node) bool {
		return n.Kind == layoutdoc.KindBlock && n.Block == blockName && blockPropScope(n.Props) == oldScope
	})
	if len(path) < 2 {
		path = doc.BlockPath(blockName)
	}
	if len(path) < 2 {
		return
	}
	parent, leaf := path[len(path)-2], path[len(path)-1]
	idx := slices.Index(parent.Children, leaf)
	if idx < 0 {
		return
	}
	nextProps := maps.Clone(leaf.Props)
	if newScope == "" {
		delete(nextProps, "scope")
	} else {
		if nextProps == nil {
			nextProps = map[string]any{}
		}
		nextProps["scope"] = newScope
	}
	nodeObj := map[string]any{
		"id":    leaf.ID,
		"block": leaf.Block,
	}
	if leaf.Size > 0 {
		nodeObj["size"] = leaf.Size
	}
	if leaf.Grow > 0 {
		nodeObj["grow"] = leaf.Grow
	}
	if leaf.Collapsible {
		nodeObj["collapsible"] = true
	}
	if len(nextProps) > 0 {
		nodeObj["props"] = nextProps
	}
	ops := []layoutdoc.Op{
		{Op: "remove", ID: leaf.ID},
		{Op: "insert", Parent: parent.ID, Index: &idx, Node: nodeObj},
	}
	if parent.Kind == layoutdoc.KindStack {
		active := parent.Active
		ops = append(ops, layoutdoc.Op{Op: "select", ID: parent.ID, Active: &active})
	}
	next, err := layoutdoc.Apply(doc, ops, m.blocks)
	if err != nil {
		m.tellWindowDoc(err)
		return
	}
	body, err := json.Marshal(next)
	if err != nil {
		return
	}
	m.setWindowLayout(docName, body)
}
