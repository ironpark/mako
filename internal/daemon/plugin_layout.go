package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ironpark/mako/internal/layoutdoc"
	"github.com/ironpark/mako/internal/plugin"
)

// pluginLayout carries out a plugin's layout:write (plugin.LayoutHost)
// through the flow an agent's patch_layout takes (layoutchange.go): the
// change staged on this node, shown, asked of the user unless they
// allowed this plugin such changes, and written into the workspace only
// then. The stage names the plugin, and what the user allows a plugin is
// kept apart from what they allow agents, and from other plugins.
type pluginLayout struct{ d *Daemon }

func (l pluginLayout) Prepare(document, ops, ifRev string) (plugin.LayoutChange, error) {
	d := l.d
	name, err := layoutName(document)
	if err != nil {
		return plugin.LayoutChange{}, err
	}
	var list []layoutdoc.Op
	dec := json.NewDecoder(bytes.NewReader([]byte(ops)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&list); err != nil {
		return plugin.LayoutChange{}, fmt.Errorf("ops is a JSON array of patch_layout's operations: %w", err)
	}
	if len(list) == 0 {
		return plugin.LayoutChange{}, errors.New("a patch has an operation at least")
	}
	cur, rev, _, err := d.layoutNow(name)
	if err != nil {
		return plugin.LayoutChange{}, err
	}
	if ifRev != "" && ifRev != rev {
		return plugin.LayoutChange{}, fmt.Errorf("the document's revision is %q, not %q", rev, ifRev)
	}
	// Apply changes the root alone: a plugin, as an agent, writes no
	// variants.
	next, err := layoutdoc.Apply(cur, list, d.blockCatalog())
	if err != nil {
		return plugin.LayoutChange{}, err
	}
	doc, err := json.Marshal(next)
	if err != nil {
		return plugin.LayoutChange{}, err
	}
	return plugin.LayoutChange{Document: name, Doc: doc, Base: rev, Summary: opsSummary(list)}, nil
}

func (l pluginLayout) Allowed(id, document string) bool {
	return l.d.layouts.allowsPlugin(id, document)
}

func (l pluginLayout) Change(ctx context.Context, id string, c plugin.LayoutChange) (string, error) {
	return l.d.changeLayoutFor(ctx, layoutOrigin{plugin: id}, layoutChange{
		tool: toolPatchLayout, name: c.Document, doc: c.Doc, base: c.Base, summary: c.Summary})
}

func (l pluginLayout) Forget(id string) { l.d.layouts.forgetPlugin(id) }
