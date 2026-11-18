package layoutdoc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"slices"
	"strings"
)

// A document of this version is read strictly: a field or kind of node it
// does not know, a value of the wrong type, or a tree a window could not
// draw is an error, and a window draws the default document instead, so a
// document never leaves it blank. A document of a later version, written
// by a newer node and synced to this one, is read leniently: fields this
// version does not know are left out, and a node of a kind it does not
// know is kept as a leaf of that kind, which a window draws as missing;
// what it does know is checked as strictly. This version writes only its
// own: a later one is not patched here. A block the catalog does not hold
// is not an error in either: it is drawn as missing, and the document
// keeps it.
//
// Version 2 adds variants: {"compact": <node>}, a root of the same nodes
// for a narrow screen. A window of version 1 reads a document of version
// 2 leniently, leaving the variants out, and draws its root as it would
// the same document of version 1; so a document is written at version 2
// only when it holds variants (Doc.Written).

// Bounds on a document, so that one written by a program cannot make the
// window build without end.
const (
	MaxNodes    = 256
	MaxDepth    = 16
	MaxNodeSize = 4096
)

var nodeIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)

// The fields of each kind of node, besides its kind's own.
var nodeFields = map[string][]string{
	KindRow:    {"id", "size", "grow", "gap", "collapsible"},
	KindColumn: {"id", "size", "grow", "gap", "collapsible"},
	KindStack:  {"id", "size", "grow", "collapsible", "active"},
	KindBlock:  {"id", "size", "grow", "collapsible", "props"},
	KindPanes:  {"id", "size", "grow"},
}

// Parse reads a document and checks it against cat.
func Parse(data []byte, cat Catalog) (*Doc, error) {
	var top map[string]json.RawMessage
	if err := oneJSON(data, &top); err != nil {
		return nil, err
	}
	var version int
	if raw, ok := top["version"]; !ok {
		return nil, errors.New("no version")
	} else if err := json.Unmarshal(raw, &version); err != nil || version < 1 {
		return nil, fmt.Errorf("version %s is not a version", clip(raw))
	}
	p := parser{lenient: version > Version}
	if !p.lenient {
		for k := range top {
			if k != "version" && k != "root" && (k != "variants" || version < VersionVariants) {
				return nil, fmt.Errorf("unknown field %q", k)
			}
		}
	}
	raw, ok := top["root"]
	if !ok || len(raw) == 0 {
		return nil, errors.New("no root")
	}
	root, err := p.node(raw, 1)
	if err != nil {
		return nil, err
	}
	d := &Doc{Version: version, Root: root}
	if raw, ok := top["variants"]; ok && version >= VersionVariants {
		if d.Compact, err = p.variants(raw); err != nil {
			if !p.lenient {
				return nil, err
			}
			d.Compact = nil // a later version's, which this one cannot read, left out
		}
	}
	if err := d.Check(cat); err != nil {
		if !p.lenient || d.Compact == nil || d.checkRoot(cat) != nil {
			return nil, err
		}
		d.Compact = nil
	}
	return d, nil
}

// variants reads a document's variants: its compact root alone, by now.
func (p *parser) variants(raw json.RawMessage) (*Node, error) {
	var vs map[string]json.RawMessage
	if err := json.Unmarshal(raw, &vs); err != nil {
		return nil, fmt.Errorf("variants are not an object: %s", clip(raw))
	}
	for k := range vs {
		if k != "compact" && !p.lenient {
			return nil, fmt.Errorf("no variant %q: there is compact", k)
		}
	}
	compact, ok := vs["compact"]
	if !ok || string(compact) == "null" {
		return nil, nil
	}
	n, err := p.node(compact, 1)
	if err != nil {
		return nil, fmt.Errorf("variants.compact: %w", err)
	}
	return n, nil
}

// oneJSON decodes the one JSON value data holds into v.
func oneJSON(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("more than one JSON value")
	}
	return nil
}

type parser struct {
	lenient bool
	count   int
}

// node reads a node: an object of one kind's field and the kind's others,
// or, for a block, its name alone.
func (p *parser) node(raw json.RawMessage, depth int) (*Node, error) {
	if p.count++; p.count > MaxNodes {
		return nil, fmt.Errorf("more than %d nodes", MaxNodes)
	}
	if depth > MaxDepth {
		return nil, fmt.Errorf("nested deeper than %d", MaxDepth)
	}
	var name string
	if json.Unmarshal(raw, &name) == nil {
		return Short(name), nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("a node is neither an object nor a block's name: %s", clip(raw))
	}
	n := &Node{}
	for _, k := range kinds {
		if _, ok := fields[k]; ok {
			if n.Kind != "" {
				return nil, fmt.Errorf("a node is both a %s and a %s", n.Kind, k)
			}
			n.Kind = k
		}
	}
	if n.Kind == "" {
		if !p.lenient {
			return nil, fmt.Errorf("a node of no kind this window knows (%s): %s", strings.Join(kinds, ", "), clip(raw))
		}
		return p.unknown(fields, raw)
	}
	rest := make(map[string]json.RawMessage, len(fields))
	for k, v := range fields {
		switch {
		case k == n.Kind && k != KindBlock && k != KindPanes:
		case k == n.Kind || slices.Contains(nodeFields[n.Kind], k):
			rest[k] = v
		case !p.lenient:
			return nil, fmt.Errorf("a %s has no field %q", n.Kind, k)
		}
	}
	if err := n.place(rest); err != nil {
		return nil, err
	}
	switch n.Kind {
	case KindBlock:
		if n.ID == "" {
			n.ID = n.Block
		}
	case KindPanes:
		if n.ID == "" {
			n.ID = n.Panes
		}
	default:
		var kids []json.RawMessage
		if err := json.Unmarshal(fields[n.Kind], &kids); err != nil {
			return nil, fmt.Errorf("node %q: a %s's children are not a list", n.ID, n.Kind)
		}
		for _, k := range kids {
			c, err := p.node(k, depth+1)
			if err != nil {
				return nil, err
			}
			n.Children = append(n.Children, c)
		}
	}
	return n, nil
}

// unknown reads a node of a later version's kind: a leaf of that kind,
// placed as its parent's child as any is, and holding nothing else.
func (p *parser) unknown(fields map[string]json.RawMessage, raw json.RawMessage) (*Node, error) {
	n := &Node{}
	rest := map[string]json.RawMessage{}
	for k, v := range fields {
		if slices.Contains([]string{"id", "size", "grow", "collapsible"}, k) {
			rest[k] = v
		} else if n.Kind == "" || k < n.Kind {
			n.Kind = k // the kind is a field none knows; of several, the first
		}
	}
	if n.Kind == "" {
		return nil, fmt.Errorf("a node of no kind: %s", clip(raw))
	}
	return n, n.place(rest)
}

// place decodes fields, checked for being the node's, into n.
func (n *Node) place(fields map[string]json.RawMessage) error {
	var in struct {
		ID          string         `json:"id"`
		Size        *float64       `json:"size"`
		Grow        *float64       `json:"grow"`
		Gap         float64        `json:"gap"`
		Collapsible bool           `json:"collapsible"`
		Active      int            `json:"active"`
		Props       map[string]any `json:"props"`
		Block       string         `json:"block"`
		Panes       string         `json:"panes"`
	}
	body, _ := json.Marshal(fields)
	if err := json.Unmarshal(body, &in); err != nil {
		return fmt.Errorf("a %s: %v", n.Kind, err)
	}
	n.ID, n.Gap, n.Collapsible, n.Active, n.Props, n.Block, n.Panes = in.ID, in.Gap, in.Collapsible, in.Active, in.Props, in.Block, in.Panes
	if in.Size != nil {
		if *in.Size <= 0 {
			return fmt.Errorf("node %q: size %v is not above 0", n.ID, *in.Size)
		}
		n.Size = *in.Size
	}
	if in.Grow != nil {
		if *in.Grow <= 0 {
			return fmt.Errorf("node %q: grow %v is not above 0", n.ID, *in.Grow)
		}
		n.Grow = *in.Grow
	}
	return nil
}

// clip is a short quote of raw JSON, for an error.
func clip(raw json.RawMessage) string {
	s := string(raw)
	if len(s) > 60 {
		s = s[:60] + "…"
	}
	return s
}

// Check says what of d a window could not draw: the IDs, each given and
// once; sizes only on a row's or column's children; a stack of blocks
// alone; a single main panes; block names of the three namespaces; the
// props of a block cat holds, against its schema; and the bounds on the
// document's size. Nodes of kinds this version does not know pass only in
// a document of a later version. The compact root is checked the same,
// its IDs apart from the root's, but for the panes, which it may leave
// out: a phone shows the sessions in its own way.
func (d *Doc) Check(cat Catalog) error {
	if err := d.checkRoot(cat); err != nil {
		return err
	}
	if d.Compact == nil {
		return nil
	}
	count, panes, err := d.checkTree(d.Compact, cat)
	switch {
	case err != nil:
		return fmt.Errorf("variants.compact: %w", err)
	case panes > 1:
		return fmt.Errorf("variants.compact: %d panes nodes, where a document has one at most, %q", panes, MainPanes)
	case d.count(d.Root)+count > MaxNodes:
		return fmt.Errorf("more than %d nodes", MaxNodes)
	}
	return nil
}

// checkRoot is Check of the document's root alone, as a window that does not
// read variants checks it.
func (d *Doc) checkRoot(cat Catalog) error {
	_, panes, err := d.checkTree(d.Root, cat)
	if err != nil {
		return err
	}
	if panes != 1 {
		return fmt.Errorf("%d panes nodes, where the window has one, %q", panes, MainPanes)
	}
	return nil
}

// count is the nodes of the tree under root.
func (d *Doc) count(root *Node) int {
	n := 0
	root.Walk(nil, func([]*Node) bool { n++; return true })
	return n
}

// checkTree checks the tree under root, a root of d, and counts its nodes
// and its panes.
func (d *Doc) checkTree(root *Node, cat Catalog) (count, panes int, err error) {
	if root == nil {
		return 0, 0, errors.New("no root")
	}
	if root.Size > 0 || root.Grow > 0 || root.Collapsible {
		return 0, 0, errors.New("the root is not a row's or column's child, to have a size, grow or collapse")
	}
	seen := map[string]bool{}
	root.Walk(nil, func(path []*Node) bool {
		n := path[len(path)-1]
		count++
		switch {
		case count > MaxNodes:
			err = fmt.Errorf("more than %d nodes", MaxNodes)
		case len(path) > MaxDepth:
			err = fmt.Errorf("nested deeper than %d", MaxDepth)
		case !Known(n.Kind) && d.Version <= Version:
			err = fmt.Errorf("node %q of no kind this window knows: %q", n.ID, n.Kind)
		default:
			err = n.check(cat)
		}
		if err == nil && seen[n.ID] {
			err = fmt.Errorf("two nodes have the id %q", n.ID)
		}
		seen[n.ID] = true
		if n.Kind == KindPanes {
			panes++
		}
		return err == nil
	})
	return count, panes, err
}

// check checks n by itself and its children's places in it.
func (n *Node) check(cat Catalog) error {
	if !nodeIDPattern.MatchString(n.ID) {
		return fmt.Errorf("a %s has no id, or one of other characters than letters, digits and ._:/-: %q", n.Kind, n.ID)
	}
	finite := func(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
	switch {
	case !finite(n.Size) || n.Size < 0 || n.Size > MaxNodeSize:
		return fmt.Errorf("node %q: size %v is not within 0 to %d", n.ID, n.Size, MaxNodeSize)
	case !finite(n.Grow) || n.Grow < 0 || !finite(n.Gap) || n.Gap < 0 || n.Gap > MaxNodeSize:
		return fmt.Errorf("node %q: grow or gap out of bounds", n.ID)
	case n.Size > 0 && n.Grow > 0:
		return fmt.Errorf("node %q: both a size and grow", n.ID)
	case n.Collapsible && n.Size <= 0:
		return fmt.Errorf("node %q: collapsible without a size to open to", n.ID)
	}
	switch n.Kind {
	case KindRow, KindColumn, KindStack:
		if len(n.Children) == 0 {
			return fmt.Errorf("node %q: a %s of no children", n.ID, n.Kind)
		}
	case KindBlock:
		if err := CheckBlockName(n.Block); err != nil {
			return fmt.Errorf("node %q: %v", n.ID, err)
		}
		if schema, ok := cat.BlockProps(n.Block); ok {
			if err := CheckProps(schema, n.Props); err != nil {
				return fmt.Errorf("node %q, block %s: %v", n.ID, n.Block, err)
			}
		}
	case KindPanes:
		if n.Panes != MainPanes {
			return fmt.Errorf("node %q: panes %q, where the window has only %q", n.ID, n.Panes, MainPanes)
		}
	}
	if n.Kind == KindStack && (n.Active < 0 || n.Active >= len(n.Children)) {
		return fmt.Errorf("node %q: active %d of %d children", n.ID, n.Active, len(n.Children))
	}
	for _, c := range n.Children {
		placed := c.Size > 0 || c.Grow > 0 || c.Collapsible
		switch {
		case n.Kind == KindStack && c.Kind != KindBlock:
			return fmt.Errorf("node %q: a stack holds blocks alone, not a %s (%q)", n.ID, c.Kind, c.ID)
		case n.Kind == KindStack && placed:
			return fmt.Errorf("node %q: a stack's child %q has a size, grow or collapses", n.ID, c.ID)
		}
	}
	return nil
}

// MarshalJSON writes d as Parse reads it, at the version it is written at
// (Written).
func (d *Doc) MarshalJSON() ([]byte, error) {
	type variants struct {
		Compact *Node `json:"compact,omitempty"`
	}
	out := struct {
		Version  int       `json:"version"`
		Root     *Node     `json:"root"`
		Variants *variants `json:"variants,omitempty"`
	}{Version: d.Written(), Root: d.Root}
	if d.Compact != nil {
		out.Variants = &variants{Compact: d.Compact}
	}
	return json.Marshal(out)
}

// MarshalJSON writes n with its ID first, then its kind's field, then the
// rest; a block written by its name alone is written so again.
func (n *Node) MarshalJSON() ([]byte, error) {
	if n.short && n.Kind == KindBlock && n.ID == n.Block && n.Size == 0 && n.Grow == 0 && !n.Collapsible && len(n.Props) == 0 {
		return json.Marshal(n.Block)
	}
	out := struct {
		ID          string         `json:"id"`
		Row         []*Node        `json:"row,omitempty"`
		Column      []*Node        `json:"column,omitempty"`
		Stack       []*Node        `json:"stack,omitempty"`
		Block       string         `json:"block,omitempty"`
		Panes       string         `json:"panes,omitempty"`
		Size        float64        `json:"size,omitempty"`
		Grow        float64        `json:"grow,omitempty"`
		Gap         float64        `json:"gap,omitempty"`
		Collapsible bool           `json:"collapsible,omitempty"`
		Active      int            `json:"active,omitempty"`
		Props       map[string]any `json:"props,omitempty"`
	}{ID: n.ID, Block: n.Block, Panes: n.Panes, Size: n.Size, Grow: n.Grow, Gap: n.Gap, Collapsible: n.Collapsible, Active: n.Active, Props: n.Props}
	switch n.Kind {
	case KindRow:
		out.Row = n.Children
	case KindColumn:
		out.Column = n.Children
	case KindStack:
		out.Stack = n.Children
	}
	return json.Marshal(out)
}

// Canonical is a document written as this version writes it: data parsed
// and written again, at the earliest version that holds it, so that two
// writings of one document are the same bytes. A document of a later
// version is kept as it came, compacted.
func Canonical(data []byte, cat Catalog) ([]byte, *Doc, error) {
	d, err := Parse(data, cat)
	if err != nil {
		return nil, nil, err
	}
	if d.Version > Version {
		var buf bytes.Buffer
		if err := json.Compact(&buf, data); err != nil {
			return nil, nil, err
		}
		return buf.Bytes(), d, nil
	}
	out, err := json.Marshal(d)
	return out, d, err
}
