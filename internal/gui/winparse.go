package gui

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

// A layout document is read strictly: a field or kind of node this window
// does not know, a value of the wrong type, or a tree it could not draw is
// an error, and the window draws the default document instead, so a
// document never leaves it blank. A block the registry does not hold is
// not an error: it is drawn as missing, and the document keeps it.

// Bounds on a document, so that one written by a program cannot make the
// window build without end.
const (
	maxWindowNodes = 256
	maxWindowDepth = 16
	maxNodeSize    = 4096
)

var nodeIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)

// The fields of each kind of node, besides its kind's own.
var nodeFields = map[string][]string{
	nodeRow:    {"id", "size", "grow", "gap", "collapsible"},
	nodeColumn: {"id", "size", "grow", "gap", "collapsible"},
	nodeStack:  {"id", "size", "grow", "collapsible", "active"},
	nodeBlock:  {"id", "size", "grow", "collapsible", "props"},
	nodePanes:  {"id", "size", "grow"},
}

var nodeKinds = []string{nodeRow, nodeColumn, nodeStack, nodeBlock, nodePanes}

// parseWindowDoc reads a document and checks it against reg.
func parseWindowDoc(data []byte, reg *blockRegistry) (*windowDoc, error) {
	var top struct {
		Version *int            `json:"version"`
		Root    json.RawMessage `json:"root"`
	}
	if err := strictJSON(data, &top); err != nil {
		return nil, err
	}
	if top.Version == nil {
		return nil, errors.New("no version")
	}
	if *top.Version != windowDocVersion {
		return nil, fmt.Errorf("version %d, where this window reads %d", *top.Version, windowDocVersion)
	}
	if len(top.Root) == 0 {
		return nil, errors.New("no root")
	}
	count := 0
	root, err := parseNode(top.Root, 1, &count)
	if err != nil {
		return nil, err
	}
	d := &windowDoc{Version: *top.Version, Root: root}
	if err := d.check(reg); err != nil {
		return nil, err
	}
	return d, nil
}

// strictJSON decodes the one JSON value data holds into v, refusing
// fields v does not have.
func strictJSON(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("more than one JSON value")
	}
	return nil
}

// parseNode reads a node: an object of one kind's field and the kind's
// others, or, for a block, its name alone.
func parseNode(raw json.RawMessage, depth int, count *int) (*winNode, error) {
	if *count++; *count > maxWindowNodes {
		return nil, fmt.Errorf("more than %d nodes", maxWindowNodes)
	}
	if depth > maxWindowDepth {
		return nil, fmt.Errorf("nested deeper than %d", maxWindowDepth)
	}
	var name string
	if json.Unmarshal(raw, &name) == nil {
		return shortBlock(name), nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("a node is neither an object nor a block's name: %s", clip(raw))
	}
	n := &winNode{}
	for _, k := range nodeKinds {
		if _, ok := fields[k]; ok {
			if n.Kind != "" {
				return nil, fmt.Errorf("a node is both a %s and a %s", n.Kind, k)
			}
			n.Kind = k
		}
	}
	if n.Kind == "" {
		return nil, fmt.Errorf("a node of no kind this window knows (%s): %s", strings.Join(nodeKinds, ", "), clip(raw))
	}
	for k := range fields {
		if k != n.Kind && !slices.Contains(nodeFields[n.Kind], k) {
			return nil, fmt.Errorf("a %s has no field %q", n.Kind, k)
		}
	}
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
	// The kind's own field is decoded apart, as its type differs by kind.
	rest := make(map[string]json.RawMessage, len(fields))
	for k, v := range fields {
		if k != n.Kind || k == nodeBlock || k == nodePanes {
			rest[k] = v
		}
	}
	body, _ := json.Marshal(rest)
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, fmt.Errorf("a %s: %v", n.Kind, err)
	}
	n.ID, n.Gap, n.Collapsible, n.Active, n.Props, n.Block, n.Panes = in.ID, in.Gap, in.Collapsible, in.Active, in.Props, in.Block, in.Panes
	if in.Size != nil {
		if *in.Size <= 0 {
			return nil, fmt.Errorf("node %q: size %v is not above 0", n.ID, *in.Size)
		}
		n.Size = *in.Size
	}
	if in.Grow != nil {
		if *in.Grow <= 0 {
			return nil, fmt.Errorf("node %q: grow %v is not above 0", n.ID, *in.Grow)
		}
		n.Grow = *in.Grow
	}
	switch n.Kind {
	case nodeBlock:
		if n.ID == "" {
			n.ID = n.Block
		}
	case nodePanes:
		if n.ID == "" {
			n.ID = n.Panes
		}
	default:
		var kids []json.RawMessage
		if err := json.Unmarshal(fields[n.Kind], &kids); err != nil {
			return nil, fmt.Errorf("node %q: a %s's children are not a list", n.ID, n.Kind)
		}
		for _, k := range kids {
			c, err := parseNode(k, depth+1, count)
			if err != nil {
				return nil, err
			}
			n.Children = append(n.Children, c)
		}
	}
	return n, nil
}

// clip is a short quote of raw JSON, for an error.
func clip(raw json.RawMessage) string {
	s := string(raw)
	if len(s) > 60 {
		s = s[:60] + "…"
	}
	return s
}

// check says what of d this window could not draw: the IDs, each given
// and once; sizes only on a row's or column's children; a stack of blocks
// alone; a single main panes; block names of the three namespaces; and the
// props of a block reg holds, against its schema.
func (d *windowDoc) check(reg *blockRegistry) error {
	if d.Root == nil {
		return errors.New("no root")
	}
	if d.Root.Size > 0 || d.Root.Grow > 0 || d.Root.Collapsible {
		return errors.New("the root is not a row's or column's child, to have a size, grow or collapse")
	}
	seen := map[string]bool{}
	panes := 0
	var err error
	d.Root.walk(nil, func(path []*winNode) bool {
		n := path[len(path)-1]
		err = n.check(reg)
		if err == nil && seen[n.ID] {
			err = fmt.Errorf("two nodes have the id %q", n.ID)
		}
		seen[n.ID] = true
		if n.Kind == nodePanes {
			panes++
		}
		return err == nil
	})
	if err != nil {
		return err
	}
	if panes != 1 {
		return fmt.Errorf("%d panes nodes, where the window has one, %q", panes, mainPanes)
	}
	return nil
}

// check checks n by itself and its children's places in it.
func (n *winNode) check(reg *blockRegistry) error {
	if !nodeIDPattern.MatchString(n.ID) {
		return fmt.Errorf("a %s has no id, or one of other characters than letters, digits and ._:/-: %q", n.Kind, n.ID)
	}
	finite := func(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
	switch {
	case !finite(n.Size) || n.Size > maxNodeSize:
		return fmt.Errorf("node %q: size %v is not within 0 to %d", n.ID, n.Size, maxNodeSize)
	case !finite(n.Grow) || !finite(n.Gap) || n.Gap < 0 || n.Gap > maxNodeSize:
		return fmt.Errorf("node %q: grow or gap out of bounds", n.ID)
	case n.Size > 0 && n.Grow > 0:
		return fmt.Errorf("node %q: both a size and grow", n.ID)
	case n.Collapsible && n.Size <= 0:
		return fmt.Errorf("node %q: collapsible without a size to open to", n.ID)
	}
	switch n.Kind {
	case nodeRow, nodeColumn, nodeStack:
		if len(n.Children) == 0 {
			return fmt.Errorf("node %q: a %s of no children", n.ID, n.Kind)
		}
	case nodeBlock:
		if err := checkBlockName(n.Block); err != nil {
			return fmt.Errorf("node %q: %v", n.ID, err)
		}
		if def, ok := reg.lookup(n.Block); ok {
			if err := checkProps(def.Props, n.Props); err != nil {
				return fmt.Errorf("node %q, block %s: %v", n.ID, n.Block, err)
			}
		}
	case nodePanes:
		if n.Panes != mainPanes {
			return fmt.Errorf("node %q: panes %q, where the window has only %q", n.ID, n.Panes, mainPanes)
		}
	}
	if n.Kind == nodeStack && (n.Active < 0 || n.Active >= len(n.Children)) {
		return fmt.Errorf("node %q: active %d of %d children", n.ID, n.Active, len(n.Children))
	}
	for _, c := range n.Children {
		placed := c.Size > 0 || c.Grow > 0 || c.Collapsible
		switch {
		case n.Kind == nodeStack && c.Kind != nodeBlock:
			return fmt.Errorf("node %q: a stack holds blocks alone, not a %s (%q)", n.ID, c.Kind, c.ID)
		case n.Kind == nodeStack && placed:
			return fmt.Errorf("node %q: a stack's child %q has a size, grow or collapses", n.ID, c.ID)
		}
	}
	return nil
}

// MarshalJSON writes d as parseWindowDoc reads it.
func (d *windowDoc) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Version int      `json:"version"`
		Root    *winNode `json:"root"`
	}{d.Version, d.Root})
}

// MarshalJSON writes n with its ID first, then its kind's field, then the
// rest; a block written by its name alone is written so again.
func (n *winNode) MarshalJSON() ([]byte, error) {
	if n.short && n.Kind == nodeBlock && n.ID == n.Block && n.Size == 0 && n.Grow == 0 && !n.Collapsible && len(n.Props) == 0 {
		return json.Marshal(n.Block)
	}
	out := struct {
		ID          string         `json:"id"`
		Row         []*winNode     `json:"row,omitempty"`
		Column      []*winNode     `json:"column,omitempty"`
		Stack       []*winNode     `json:"stack,omitempty"`
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
	case nodeRow:
		out.Row = n.Children
	case nodeColumn:
		out.Column = n.Children
	case nodeStack:
		out.Stack = n.Children
	}
	return json.Marshal(out)
}
