package daemon

import (
	"context"
	"slices"
	"strings"

	"github.com/ironpark/mako/internal/layoutdoc"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transport"
	"github.com/ironpark/mako/internal/workspace"
)

// A node let in, a phone, lists a computer's panels before it opens one,
// but may not list its plugins (plugins.go): what a plugin is, asks for
// and is allowed is the computer's own. list_panels is the narrowest
// listing that names them: the blocks the node's window layout documents
// place, and its agents' blocks, each by name, its title and a hint of
// its kind; nothing of permissions, settings or registries, and nothing
// of a plugin no document places. A node let in already watches and taps
// these blocks (revision 30), so their titles give it nothing more.

func (d *Daemon) handleListPanels(ctx context.Context, stream *transport.Stream, env *protocol.Envelope) error {
	return serve(d, ctx, stream, env, protocol.TypePanels, func(*protocol.ListPanels) (*protocol.Panels, error) {
		return &protocol.Panels{Panels: d.panels()}, nil
	})
}

// panels are the blocks the documents place, the default first and then
// by name, each depth first, its compact root after its root; then the
// agents' blocks; each block once.
func (d *Daemon) panels() []*protocol.Panel {
	st := d.workspace.get(false).GetState()
	names := []string{layoutdoc.DefaultName}
	for n := range st.GetWindowLayouts() {
		if n != layoutdoc.DefaultName && workspace.WindowLayout(st, n) != nil {
			names = append(names, n)
		}
	}
	slices.Sort(names[1:])
	cat := d.blockCatalog()
	seen := map[string]bool{}
	var out []*protocol.Panel
	add := func(p *protocol.Panel) {
		if !seen[p.GetBlock()] {
			seen[p.GetBlock()] = true
			out = append(out, p)
		}
	}
	for _, name := range names {
		doc, _, _, err := d.layoutNow(name)
		if err != nil || doc == nil {
			continue
		}
		for _, root := range []*layoutdoc.Node{doc.Root, doc.Compact} {
			if root == nil {
				continue
			}
			root.Walk(nil, func(path []*layoutdoc.Node) bool {
				if n := path[len(path)-1]; n.Kind == layoutdoc.KindBlock && n.Block != "" {
					add(d.panelOf(n.Block, cat))
				}
				return true
			})
		}
	}
	for _, b := range d.agentUI.List() {
		add(&protocol.Panel{Block: b.GetName(), Title: protocol.Printable(b.GetTitle()), Kind: protocol.PanelAgent})
	}
	return out
}

// panelOf is the block of the name as a panel: its title as this node
// knows it.
func (d *Daemon) panelOf(name string, cat layoutdoc.Catalog) *protocol.Panel {
	switch {
	case strings.HasPrefix(name, "plugin:"):
		if d.plugins != nil {
			for _, b := range d.plugins.Blocks() {
				if b.Name == name {
					return &protocol.Panel{Block: name, Title: b.Title, Kind: protocol.PanelPlugin}
				}
			}
		}
		return &protocol.Panel{Block: name, Kind: protocol.PanelMissing}
	case strings.HasPrefix(name, "agent:"):
		for _, b := range d.agentUI.List() {
			if b.GetName() == name {
				return &protocol.Panel{Block: name, Title: protocol.Printable(b.GetTitle()), Kind: protocol.PanelAgent}
			}
		}
		return &protocol.Panel{Block: name, Kind: protocol.PanelMissing}
	}
	for _, b := range layoutdoc.Builtin() {
		if b.Name == name {
			return &protocol.Panel{Block: name, Title: b.Title, Kind: protocol.PanelBuiltin}
		}
	}
	return &protocol.Panel{Block: name, Kind: protocol.PanelMissing}
}
