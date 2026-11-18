package gui

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/runtime"
	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/client"
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
		Later:   ggui.State(map[string]bool{}),
		Secrets: ggui.State(secretStore{}),
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
			name, title, id := b.GetName(), b.GetTitle(), p.GetId()
			defs = append(defs, blockDef{Name: name, Title: title,
				Build: func(m *model, _ map[string]any) blockView { return blockView{Body: pluginBlock(m, name, id, title)} }})
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

// actOnPlugin asks the daemon to act on a plugin for the user, and lists
// them again; a link the plugin asked to open in answer is opened.
func (m *model) actOnPlugin(req *protocol.PluginAction) {
	if m.post == nil {
		return
	}
	fetch(m, 30*time.Second, func(ctx context.Context, c *client.Client) (*protocol.PluginDone, error) {
		return c.PluginAction(ctx, req)
	}, func(done *protocol.PluginDone, err error) {
		if err != nil {
			m.connErr(errors.New(errorText(err)))
		} else {
			openPluginLink(done.GetOpenUrl())
		}
		m.link.pluginsStale.Store(true)
		m.link.pollNow()
	})
}

// blockEvent tells a block's plugin or agent of the user acting on a node
// of it, with the block's fields as they stand.
func (m *model) blockEvent(block, node, kind, value string, values map[string]string) {
	if m.post == nil {
		return
	}
	req := &protocol.BlockEvent{Block: block, Node: node, Kind: kind, Value: value, Values: values}
	fetch(m, 30*time.Second, func(ctx context.Context, c *client.Client) (*protocol.PluginDone, error) {
		return c.BlockEvent(ctx, req)
	}, func(done *protocol.PluginDone, err error) {
		if err != nil {
			m.connErr(err)
			return
		}
		openPluginLink(done.GetOpenUrl())
	})
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
	}
	return p
}

// pluginCommands are the plugins' commands, for the palette: named for
// their plugin, so none passes for one of the window's.
func (m *model) pluginCommands() []command {
	var out []command
	for _, p := range ggui.Peek(m.plugins.List) {
		usable := p.GetState() == protocol.PluginReady || p.GetState() == protocol.PluginRunning
		for _, c := range p.GetCommands() {
			id, cmd := p.GetId(), c.GetId()
			out = append(out, command{ID: c.GetTitle(), Category: id, Keys: []string{"plugin", id, cmd}, Disabled: !usable,
				Run: func() { m.actOnPlugin(&protocol.PluginAction{PluginId: id, Op: protocol.PluginRun, Command: cmd}) }})
		}
	}
	return out
}
