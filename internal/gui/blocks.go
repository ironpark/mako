package gui

import (
	"fmt"
	"regexp"
	"slices"
	"sort"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/gui/skin"
)

// A block is a piece of the window a layout document places by name
// (winlayout.go). The window's own are registered here as mako.<name>;
// plugins' will be plugin:<id>/<block> and agents' agent:<session>/<id>,
// in the same registry, so a document names them all alike.

// blockDef is a registered block.
type blockDef struct {
	Name  string
	Title string // in English, drawn through T
	// Props is the JSON Schema of the block's props: an object schema whose
	// properties each give a type. A document's props for the block are
	// checked against it (checkProps).
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

// blockRegistry holds the blocks by name, in the order registered.
type blockRegistry struct {
	defs  map[string]*blockDef
	names []string
}

func newBlockRegistry() *blockRegistry { return &blockRegistry{defs: map[string]*blockDef{}} }

// register adds d; a name of no namespace, or one taken, is refused.
func (r *blockRegistry) register(d blockDef) error {
	if err := checkBlockName(d.Name); err != nil {
		return err
	}
	if _, ok := r.defs[d.Name]; ok {
		return fmt.Errorf("block %q is registered already", d.Name)
	}
	if d.Build == nil {
		return fmt.Errorf("block %q draws nothing", d.Name)
	}
	if d.Props == nil {
		d.Props = noProps()
	}
	if t, _ := d.Props["type"].(string); t != "object" {
		return fmt.Errorf("block %q: props schema is not an object schema", d.Name)
	}
	r.defs[d.Name] = &d
	r.names = append(r.names, d.Name)
	return nil
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

// Block names: the window's, a plugin's and an agent's.
var (
	makoBlockName   = regexp.MustCompile(`^mako\.[a-z][a-z0-9-]*$`)
	pluginBlockName = regexp.MustCompile(`^plugin:[a-z0-9][a-z0-9._-]*/[a-z0-9][a-z0-9._-]*$`)
	agentBlockName  = regexp.MustCompile(`^agent:[A-Za-z0-9]+/[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// checkBlockName says whether name is in one of the three namespaces.
func checkBlockName(name string) error {
	if makoBlockName.MatchString(name) || pluginBlockName.MatchString(name) || agentBlockName.MatchString(name) {
		return nil
	}
	return fmt.Errorf("block name %q is not mako.<name>, plugin:<id>/<block> or agent:<session>/<id>", name)
}

// noProps is the schema of a block that takes no props.
func noProps() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
}

// checkProps checks props against schema, an object schema: each prop's
// JSON type against its property's, the required ones present, and no
// others unless additionalProperties allows them. It reads no more of JSON
// Schema than that.
func checkProps(schema, props map[string]any) error {
	declared, _ := schema["properties"].(map[string]any)
	extra, _ := schema["additionalProperties"].(bool)
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		p, ok := declared[k].(map[string]any)
		if !ok {
			if extra {
				continue
			}
			return fmt.Errorf("prop %q is not one the block takes", k)
		}
		if t, _ := p["type"].(string); t != "" && !jsonTypeIs(props[k], t) {
			return fmt.Errorf("prop %q is not of type %s", k, t)
		}
	}
	required, _ := schema["required"].([]any)
	for _, r := range required {
		if k, _ := r.(string); k != "" && !slices.Contains(keys, k) {
			return fmt.Errorf("prop %q is required", k)
		}
	}
	return nil
}

// jsonTypeIs says whether v, as encoding/json decodes into any, is of the
// JSON Schema type t.
func jsonTypeIs(v any, t string) bool {
	switch v := v.(type) {
	case string:
		return t == "string"
	case float64:
		return t == "number" || t == "integer" && v == float64(int64(v))
	case bool:
		return t == "boolean"
	case map[string]any:
		return t == "object"
	case []any:
		return t == "array"
	case nil:
		return t == "null"
	}
	return false
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
