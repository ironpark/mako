package uiblock

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/ironpark/mako/internal/protocol"
)

// The bounds of an event from a front-end. Its values are as many as a
// tree has nodes at most, a front-end sending every field of the tree it
// has, so they are bounded by their bytes.
const (
	MaxEventValue = 8 << 10  // the value a change carries
	maxValuesSize = 32 << 10 // the block's values an event carries, ids and values
)

// Message is what a node sends for an event: the message its tree gave
// it, [name, ...arguments], and for a change the value last.
type Message struct {
	Name string
	// Given is the node's message as the tree gave it.
	Given json.RawMessage
	// Args are its arguments, then a change's value: a toggle's as a
	// boolean, any other's as text.
	Args []json.RawMessage
	// Kind is the node's kind.
	Kind string
}

// Message is the message the node of an event sends, refusing an event of
// a node the tree does not hold, or one that sends nothing for it.
func (t *Tree) Message(ev *protocol.BlockEvent) (Message, error) {
	if len(ev.GetValue()) > MaxEventValue {
		return Message{}, fmt.Errorf("the event's value is over %d KiB", MaxEventValue>>10)
	}
	var a actions
	var kind string
	var node *protocol.UiNode
	found := false
	if t != nil {
		a, found = t.actions[ev.GetNode()]
		if node = nodeOf(t.Root, ev.GetNode()); node != nil {
			kind = node.GetKind()
		}
	}
	var raw json.RawMessage
	switch ev.GetKind() {
	case protocol.UiTap:
		raw = a.tap
	case protocol.UiDoubleTap:
		raw = a.doubleTap
	case protocol.UiChange:
		raw = a.change
	case protocol.UiSubmit:
		raw = a.submit
		if raw == nil {
			raw = a.change
		}
	case protocol.UiDrop:
		limit := maxDrag
		if kind == KindGraph {
			limit = maxGraphID*6 + 128
		}
		if ev.GetValue() == "" || len(ev.GetValue()) > limit || strings.ContainsFunc(ev.GetValue(), Spoofing) {
			return Message{}, fmt.Errorf("invalid drop payload for %q of %s", ev.GetNode(), ev.GetBlock())
		}
		raw = a.drop
	case protocol.UiCheck:
		if ev.GetValue() != "on" && ev.GetValue() != "off" {
			return Message{}, fmt.Errorf("invalid check value %q for %q of %s", ev.GetValue(), ev.GetNode(), ev.GetBlock())
		}
		raw = a.check
	default:
		return Message{}, fmt.Errorf("%q is not a kind of event", ev.GetKind())
	}
	if !found || raw == nil {
		return Message{}, fmt.Errorf("node %q of %s sends no message on %s", ev.GetNode(), ev.GetBlock(), ev.GetKind())
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil || len(parts) == 0 {
		return Message{}, errors.New("the node's message does not read")
	}
	m := Message{Given: raw, Args: parts[1:], Kind: kind}
	_ = json.Unmarshal(parts[0], &m.Name)
	switch ev.GetKind() {
	case protocol.UiTap:
		if menuKey, ok := t.itemContextMenu[ev.GetNode()]; ok {
			allowed := t.contextArgs[menuKey]
			if !allowed[ev.GetValue()] {
				return Message{}, fmt.Errorf("context menu %q of %s has no opener for arg %q", menuKey, ev.GetBlock(), ev.GetValue())
			}
			b, _ := json.Marshal(ev.GetValue())
			m.Args = append(m.Args, b)
		}
	case protocol.UiDoubleTap:
		if kind != KindGraph || !graphHasNode(node, ev.GetValue()) {
			return Message{}, fmt.Errorf("graph %q of %s has no node %q", ev.GetNode(), ev.GetBlock(), ev.GetValue())
		}
		b, _ := json.Marshal(ev.GetValue())
		m.Args = append(m.Args, b)
	case protocol.UiDrop:
		if kind == KindGraph {
			var pos struct {
				ID string  `json:"id"`
				X  float64 `json:"x"`
				Y  float64 `json:"y"`
			}
			dec := json.NewDecoder(strings.NewReader(ev.GetValue()))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&pos); err != nil || dec.More() || !graphHasNode(node, pos.ID) || !finite(pos.X, -maxGraphCoord, maxGraphCoord) || !finite(pos.Y, -maxGraphCoord, maxGraphCoord) {
				return Message{}, fmt.Errorf("invalid graph drop payload for %q of %s", ev.GetNode(), ev.GetBlock())
			}
			b, _ := json.Marshal(pos)
			m.Args = append(m.Args, b)
		} else {
			b, _ := json.Marshal(ev.GetValue())
			m.Args = append(m.Args, b)
		}
	case protocol.UiChange:
		var value any = ev.GetValue()
		switch kind {
		case KindToggle, KindCollapsible, KindTreeItem:
			value = ev.GetValue() == "true"
		case KindCheckbox:
			if ev.GetValue() != "on" && ev.GetValue() != "off" {
				return Message{}, fmt.Errorf("invalid checkbox value %q for %q of %s", ev.GetValue(), ev.GetNode(), ev.GetBlock())
			}
		case KindDialog, KindAlert:
			if ev.GetValue() != "dismiss" {
				return Message{}, fmt.Errorf("invalid %s change value %q for %q of %s", kind, ev.GetValue(), ev.GetNode(), ev.GetBlock())
			}
		case KindDag:
			if !dagHasRow(node, ev.GetValue()) {
				return Message{}, fmt.Errorf("dag %q of %s has no row %q", ev.GetNode(), ev.GetBlock(), ev.GetValue())
			}
		case KindGraph:
			if !graphHasNode(node, ev.GetValue()) {
				return Message{}, fmt.Errorf("graph %q of %s has no node %q", ev.GetNode(), ev.GetBlock(), ev.GetValue())
			}
		case KindRadio:
			if !slices.Contains(node.GetOptions(), ev.GetValue()) {
				return Message{}, fmt.Errorf("radio %q of %s has no option %q", ev.GetNode(), ev.GetBlock(), ev.GetValue())
			}
		case KindSlider:
			v, err := strconv.ParseFloat(ev.GetValue(), 64)
			if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < node.GetMin()-1e-9 || v > node.GetMax()+1e-9 {
				return Message{}, fmt.Errorf("invalid slider value %q for %q of %s", ev.GetValue(), ev.GetNode(), ev.GetBlock())
			}
			value = v
		case KindTable:
			col := ev.GetValue()
			if !slices.Contains(node.GetLabels(), col) {
				if c, dir, ok := strings.Cut(col, ":"); !ok || (dir != "asc" && dir != "desc") || !slices.Contains(node.GetLabels(), c) {
					return Message{}, fmt.Errorf("table %q of %s has no column %q", ev.GetNode(), ev.GetBlock(), ev.GetValue())
				}
			}
		case KindDiff:
			idx, err := strconv.Atoi(ev.GetValue())
			if err != nil || idx < 0 || idx >= len(node.GetDiffLines()) {
				return Message{}, fmt.Errorf("diff %q of %s has no line %q", ev.GetNode(), ev.GetBlock(), ev.GetValue())
			}
			value = idx
		case KindCanvas:
			if !canvasHasHitID(node, ev.GetValue()) {
				return Message{}, fmt.Errorf("canvas %q of %s has no hitId %q", ev.GetNode(), ev.GetBlock(), ev.GetValue())
			}
		case KindTimeline:
			if !timelineHasSpan(node, ev.GetValue()) {
				return Message{}, fmt.Errorf("timeline %q of %s has no span %q", ev.GetNode(), ev.GetBlock(), ev.GetValue())
			}
		case KindChart:
			if !validChartChange(node, ev.GetValue()) {
				return Message{}, fmt.Errorf("invalid chart selection %q for %q of %s", ev.GetValue(), ev.GetNode(), ev.GetBlock())
			}
		}
		b, _ := json.Marshal(value)
		m.Args = append(m.Args, b)
	default:
		b, _ := json.Marshal(ev.GetValue())
		m.Args = append(m.Args, b)
	}
	return m, nil
}

func dagHasRow(n *protocol.UiNode, id string) bool {
	if n == nil || id == "" {
		return false
	}
	for _, r := range n.GetRows() {
		if r.GetId() == id {
			return true
		}
	}
	return false
}

func graphHasNode(n *protocol.UiNode, id string) bool {
	if n == nil || id == "" {
		return false
	}
	for _, gn := range n.GetGraphNodes() {
		if gn.GetId() == id {
			return true
		}
	}
	return false
}

func canvasHasHitID(n *protocol.UiNode, id string) bool {
	if n == nil || id == "" {
		return false
	}
	for _, cmd := range n.GetCanvasCommands() {
		if cmd.GetHitId() == id {
			return true
		}
	}
	return false
}

func timelineHasSpan(n *protocol.UiNode, id string) bool {
	if n == nil || id == "" {
		return false
	}
	for _, lane := range n.GetTimelineLanes() {
		for _, sp := range lane.GetSpans() {
			if sp.GetId() == id {
				return true
			}
		}
	}
	return false
}

func validChartChange(n *protocol.UiNode, val string) bool {
	if n == nil || val == "" || len(val) > 512 || strings.ContainsFunc(val, Spoofing) {
		return false
	}
	numSeries := len(n.GetSeries())
	maxCols := len(n.GetLabels())
	for _, s := range n.GetSeries() {
		maxCols = max(maxCols, len(s.GetValues()))
	}
	if idx, err := strconv.Atoi(val); err == nil {
		return idx >= 0 && idx < maxChartPoints && (maxCols == 0 || idx < maxCols)
	}
	if strings.HasPrefix(val, "{") {
		var obj struct {
			Index  *int     `json:"index"`
			Row    *int     `json:"row"`
			Col    *int     `json:"col"`
			Series string   `json:"series"`
			Label  string   `json:"label"`
			Value  *float64 `json:"value"`
		}
		dec := json.NewDecoder(strings.NewReader(val))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&obj); err != nil || dec.More() {
			return false
		}
		if strings.ContainsFunc(obj.Series, Spoofing) || strings.ContainsFunc(obj.Label, Spoofing) {
			return false
		}
		if obj.Value != nil && (math.IsNaN(*obj.Value) || math.IsInf(*obj.Value, 0)) {
			return false
		}
		hasIdx := obj.Index != nil
		hasRC := obj.Row != nil || obj.Col != nil
		if hasIdx && hasRC {
			return false
		}
		if hasIdx {
			return *obj.Index >= 0 && *obj.Index < maxChartPoints && (maxCols == 0 || *obj.Index < maxCols)
		}
		if obj.Row != nil && obj.Col != nil {
			r, c := *obj.Row, *obj.Col
			return r >= 0 && r < numSeries && c >= 0 && c < len(n.Series[r].GetValues())
		}
		return false
	}
	if rowStr, colStr, ok := strings.Cut(val, ":"); ok {
		row, err1 := strconv.Atoi(rowStr)
		col, err2 := strconv.Atoi(colStr)
		return err1 == nil && err2 == nil && row >= 0 && col >= 0 && row < numSeries && col < len(n.Series[row].GetValues())
	}
	return false
}

// Values are the values of an event's block, as the front-end has them,
// kept to the tree's inputs, toggles and selects, by node id: what a
// form's submit carries. Too many, more than a tree holds nodes, or too
// much is refused, as is a value of a node of another kind. A value of a
// node the tree no longer holds is left out: the front-end's tree may be
// a render behind the daemon's, and the rest of the form is still the
// user's.
func (t *Tree) Values(in map[string]string) (map[string]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) > maxNodes {
		return nil, fmt.Errorf("an event carries at most %d values", maxNodes)
	}
	size := 0
	for id, v := range in {
		size += len(id) + len(v)
		if len(v) > MaxEventValue || size > maxValuesSize {
			return nil, fmt.Errorf("an event's values are at most %d KiB", maxValuesSize>>10)
		}
	}
	kinds := map[string]string{}
	var walk func(n *protocol.UiNode)
	walk = func(n *protocol.UiNode) {
		if n == nil {
			return
		}
		kinds[n.GetId()] = n.GetKind()
		for _, c := range n.GetChildren() {
			walk(c)
		}
	}
	if t != nil {
		walk(t.Root)
	}
	out := make(map[string]string, len(in))
	for id, v := range in {
		switch kind, ok := kinds[id]; {
		case !ok:
			continue
		case kind != KindInput && kind != KindTextarea && kind != KindToggle && kind != KindCheckbox && kind != KindSelect && kind != KindTabs && kind != KindRadio && kind != KindSlider:
			return nil, fmt.Errorf("node %q is no input, textarea, toggle, checkbox, select, tabs, radio or slider of the block", id)
		}
		out[id] = v
	}
	return out, nil
}

// nodeOf is the node of id in a tree, nil for none.
func nodeOf(n *protocol.UiNode, id string) *protocol.UiNode {
	if n.GetId() == id {
		return n
	}
	for _, c := range n.GetChildren() {
		if m := nodeOf(c, id); m != nil {
			return m
		}
	}
	return nil
}
