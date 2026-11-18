package gui

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/layoutdoc"
)

// A block is a piece of the window a layout document places by name
// (internal/layoutdoc). The window's own are registered here as
// mako.<name>, their names, titles and props as layoutdoc declares them,
// with what each draws; plugins' will be plugin:<id>/<block> and agents'
// agent:<session>/<id>, in the same registry, so a document names them
// all alike.

// blockDef is a registered block.
type blockDef struct {
	Name  string
	Title string // in English, drawn through T
	// Props is the JSON Schema of the block's props: an object schema whose
	// properties each give a type. A document's props for the block are
	// checked against it (layoutdoc.CheckProps).
	Props map[string]any
	// Label, when set, is the block's name on a stack's tab, read as it
	// changes; otherwise the tab reads T(Title).
	Label func(m *model) string
	Build func(m *model, props map[string]any) blockView
}

// blockView is a block as drawn: its body, and, for one a document sizes,
// the face of the edge that resizes it, so the edge reads as part of the
// block. Edge gets the edge's line and makes the face around it with
// edgeBox; nil draws it on the workspace's surface.
type blockView struct {
	Body ggui.Widget
	Edge func(line ggui.Widget, vertical bool) ggui.Widget
}

// blockRegistry holds the blocks by name, in the order registered. The
// window's own are there from the start; plugins' come and go as the
// daemon runs them, and rev counts the changes, for what draws them to
// follow.
type blockRegistry struct {
	defs  map[string]*blockDef
	names []string
	rev   *ggui.StateValue[int]
}

func newBlockRegistry() *blockRegistry {
	return &blockRegistry{defs: map[string]*blockDef{}, rev: ggui.State(0)}
}

// replacePlugins makes the plugins' blocks those of defs, by name, each
// named plugin:<id>/<block>: the others go. It reports whether any came,
// went or changed its title.
func (r *blockRegistry) replacePlugins(defs []blockDef) bool {
	want := map[string]blockDef{}
	for _, d := range defs {
		if strings.HasPrefix(d.Name, "plugin:") && layoutdoc.CheckBlockName(d.Name) == nil {
			want[d.Name] = d
		}
	}
	changed := false
	for _, name := range slices.Clone(r.names) {
		if !strings.HasPrefix(name, "plugin:") {
			continue
		}
		d, keep := want[name]
		if keep && d.Title == r.defs[name].Title {
			delete(want, name)
			continue
		}
		delete(r.defs, name)
		r.names = slices.DeleteFunc(r.names, func(n string) bool { return n == name })
		changed = true
	}
	added := slices.Sorted(maps.Keys(want))
	for _, name := range added {
		if err := r.register(want[name]); err == nil {
			changed = true
		}
	}
	if changed {
		r.rev.Update(func(n int) int { return n + 1 })
	}
	return changed
}

// register adds d; a name of no namespace, or one taken, is refused.
func (r *blockRegistry) register(d blockDef) error {
	if err := layoutdoc.CheckBlockName(d.Name); err != nil {
		return err
	}
	if _, ok := r.defs[d.Name]; ok {
		return fmt.Errorf("block %q is registered already", d.Name)
	}
	if d.Build == nil {
		return fmt.Errorf("block %q draws nothing", d.Name)
	}
	if d.Props == nil {
		d.Props = layoutdoc.NoProps()
	}
	if t, _ := d.Props["type"].(string); t != "object" {
		return fmt.Errorf("block %q: props schema is not an object schema", d.Name)
	}
	r.defs[d.Name] = &d
	r.names = append(r.names, d.Name)
	return nil
}

// BlockProps makes the registry a layoutdoc.Catalog.
func (r *blockRegistry) BlockProps(name string) (map[string]any, bool) {
	d, ok := r.defs[name]
	if !ok {
		return nil, false
	}
	return d.Props, true
}

func (r *blockRegistry) lookup(name string) (*blockDef, bool) {
	d, ok := r.defs[name]
	return d, ok
}

// list is every block, in the order registered: the catalog an agent
// building a document will read.
func (r *blockRegistry) list() []*blockDef {
	out := make([]*blockDef, len(r.names))
	for i, n := range r.names {
		out[i] = r.defs[n]
	}
	return out
}

// edgeBox is the face of a resize edge around line: panelEdgeWidth across
// the way the panel resizes.
func edgeBox(line ggui.Widget, vertical bool) *widget.BoxWidget {
	if vertical {
		return widget.Box(line).Height(panelEdgeWidth)
	}
	return widget.Box(line).Width(panelEdgeWidth)
}

// missingBlock stands where a document names a block not registered here,
// as a plugin's not installed on this node: the document keeps it, and the
// block comes back in its place once registered.
func missingBlock(name string) ggui.Widget {
	return widget.Box(widget.Center(widget.Column(
		icon("layout-grid").Size(22).Color(skin.ExtraMuted),
		widget.Text(T("Missing block")).Size(13).Weight(ggui.WeightMedium),
		muted(widget.Text(name).Size(12).Font(ggui.DefaultMonoFont())),
		muted(widget.Text(T("No block of this name is registered on this node.")).Size(12)),
	).Gap(6).Align(widget.AlignCenter))).Pad(12).Fill(skin.Surface0)
}
