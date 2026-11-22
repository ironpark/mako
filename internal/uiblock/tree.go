// Package uiblock is the blocks drawn from trees of a fixed vocabulary:
// a plugin's (internal/plugin) and an agent's (the show_ui tool). A tree
// comes as JSON, a plugin's render result or an agent's tool call, and is
// read here strictly, as untrusted: an unknown kind or field (a raw
// colour, say) is refused, as is a tone that is not a token, a tree too
// large or too deep, and text that could pass for something else. What
// survives is the protocol's tree, which the front-ends draw with their
// own components, and the messages its nodes send, which stay in the
// daemon: a front-end reports only which node the user acted on. A block,
// of either kind, goes to the front-ends watching it whole, then as
// patches (Follow).
package uiblock

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"

	"github.com/ironpark/mako/internal/protocol"
)

// The UI vocabulary's kinds.
const (
	KindColumn      = "column"
	KindRow         = "row"
	KindStack       = "stack"
	KindCard        = "card"
	KindScroll      = "scroll"
	KindWrap        = "wrap"
	KindGrid        = "grid"
	KindSplit       = "split"
	KindCollapsible = "collapsible"
	KindBreadcrumb  = "breadcrumb"
	KindTree        = "tree"
	KindTreeItem    = "tree_item"
	KindText        = "text"
	KindIcon        = "icon"
	KindPill        = "pill"
	KindChip        = "chip"
	KindButton      = "button"
	KindList        = "list"
	KindInput       = "input"
	KindTextarea    = "textarea"
	KindToggle      = "toggle"
	KindSelect      = "select"
	KindTabs        = "tabs"
	KindTable       = "table"
	KindMarkdown    = "markdown"
	KindCode        = "code"
	KindProgress    = "progress"
	KindDivider     = "divider"
	KindChart       = "chart"
	KindGraph       = "graph"
)

// Kinds are the vocabulary, in the order docs/plugins.md lists it.
var Kinds = []string{KindColumn, KindRow, KindStack, KindCard, KindScroll, KindWrap, KindGrid, KindSplit,
	KindCollapsible, KindBreadcrumb, KindTree, KindTreeItem, KindText, KindIcon, KindPill, KindChip, KindButton,
	KindList, KindInput, KindTextarea, KindToggle, KindSelect, KindTabs, KindTable, KindMarkdown, KindCode,
	KindProgress, KindDivider, KindChart, KindGraph}

// Charts are the kinds of chart a chart node draws.
var Charts = []string{"line", "bar", "sparkline", "donut", "pie"}

// Tones are the colour tokens a node may take.
var Tones = []string{"", "muted", "faint", "accent", "success", "warning", "danger"}

// Variants are the style tokens a button or card may take.
var Variants = []string{"", "primary", "secondary", "outline", "ghost", "danger"}

// The bounds of a tree.
const (
	maxTreeJSON   = 512 << 10
	maxNodes      = 2000
	maxDepth      = 24
	maxNodeText   = 8 << 10
	maxLongText   = 32 << 10 // a markdown's or code's
	maxTreeText   = 256 << 10
	maxKey        = 128
	maxDrag       = 256
	maxDragGroup  = 64
	maxDropAccept = 16
	maxScrollH    = 4000
	maxTableCols  = 16
	maxGridCols   = 6
	minSplitRatio = 0.1
	maxSplitRatio = 0.9
	defSplitRatio = 0.5
	maxNodeOpts   = 64
	maxMessage    = 4 << 10
	maxGap, maxG  = 64, 100
	maxOption     = 320
	// A chart's bounds: a few series of a few hundred points, labels as
	// short as an axis shows them.
	maxSeries      = 4
	maxPoints      = 200
	maxChartPoints = 600
	maxPieSlices   = 16
	maxLabel       = 32
	maxSeriesName  = 64
	maxChartValue  = 1e15
	// A graph canvas's bounds.
	maxGraphNodes    = 120
	maxGraphEdges    = 400
	maxGraphID       = 128
	maxGraphLabel    = 128
	maxGraphGroup    = 64
	maxGraphSubtitle = 160
	maxGraphWeight   = 1e6
	minGraphHeight   = 120
	maxGraphHeight   = 1200
)

var (
	iconName      = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	messageName   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	languageTag   = regexp.MustCompile(`^[a-z0-9+#-]{0,24}$`)
	dragGroupName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
)

// jsNode is a node as the prelude writes it.
type jsNode struct {
	Kind        string          `json:"kind"`
	Key         string          `json:"key"`
	Text        string          `json:"text"`
	Tone        string          `json:"tone"`
	Variant     string          `json:"variant"`
	Icon        string          `json:"icon"`
	Weight      string          `json:"weight"`
	Size        string          `json:"size"`
	Value       string          `json:"value"`
	Checked     bool            `json:"checked"`
	Options     []string        `json:"options"`
	Progress    float64         `json:"progress"`
	Language    string          `json:"language"`
	Placeholder string          `json:"placeholder"`
	Disabled    bool            `json:"disabled"`
	Grow        float64         `json:"grow"`
	Gap         float64         `json:"gap"`
	Columns     int32           `json:"columns"`
	Ratio       *float64        `json:"ratio"`
	Fill        bool            `json:"fill"`
	OnTap       json.RawMessage `json:"onTap"`
	OnChange    json.RawMessage `json:"onChange"`
	OnSubmit    json.RawMessage `json:"onSubmit"`
	OnLink      json.RawMessage `json:"onLink"`
	Drag        string          `json:"drag"`
	DragGroup   string          `json:"dragGroup"`
	OnDrop      json.RawMessage `json:"onDrop"`
	DropAccept  []string        `json:"dropAccept"`
	Direction   string          `json:"direction"`
	MaxHeight   float64         `json:"maxHeight"`
	Children    []*jsNode       `json:"children"`
	Chart       string          `json:"chart"`
	Series      []*jsSeries     `json:"series"`
	Labels      []string        `json:"labels"`
	GraphNodes  []*jsGraphNode  `json:"graphNodes"`
	GraphEdges  []*jsGraphEdge  `json:"graphEdges"`
}

// jsSeries is a chart's series as the prelude writes it.
type jsSeries struct {
	Name   string     `json:"name"`
	Tone   string     `json:"tone"`
	Values []*float64 `json:"values"`
}

// jsGraphNode is a node of a graph canvas as the prelude writes it.
type jsGraphNode struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Group    string   `json:"group"`
	Tone     string   `json:"tone"`
	Subtitle string   `json:"subtitle"`
	Weight   *float64 `json:"weight"`
}

// jsGraphEdge is an edge of a graph canvas as the prelude writes it.
type jsGraphEdge struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Label  string `json:"label"`
	Tone   string `json:"tone"`
	Dashed bool   `json:"dashed"`
}

// actions are what a node's events send: each a message, the JSON array
// of its name and arguments.
type actions struct{ tap, change, submit, drop json.RawMessage }

// Tree is a tree, read: the protocol's tree and its nodes' messages by
// node id.
type Tree struct {
	Root    *protocol.UiNode
	actions map[string]actions
}

// Read reads a tree's JSON.
func Read(data []byte) (*Tree, error) {
	if len(data) > maxTreeJSON {
		return nil, fmt.Errorf("the tree is over %d KiB of JSON", maxTreeJSON>>10)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var root jsNode
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("the tree does not read: %w", err)
	}
	if dec.More() {
		return nil, fmt.Errorf("the tree does not read: more after it")
	}
	r := &treeReader{t: &Tree{actions: map[string]actions{}}}
	n, err := r.node(&root, "", 1)
	if err != nil {
		return nil, err
	}
	r.t.Root = n
	return r.t, nil
}

type treeReader struct {
	t     *Tree
	nodes int
	text  int
}

func (r *treeReader) node(j *jsNode, id string, depth int) (*protocol.UiNode, error) {
	if j == nil {
		return nil, fmt.Errorf("%s: a node is null", where(id))
	}
	r.nodes++
	if r.nodes > maxNodes {
		return nil, fmt.Errorf("the tree has more than %d nodes", maxNodes)
	}
	if depth > maxDepth {
		return nil, fmt.Errorf("the tree is deeper than %d", maxDepth)
	}
	if !slices.Contains(Kinds, j.Kind) {
		return nil, fmt.Errorf("%s: %q is not a kind of node (%s)", where(id), j.Kind, strings.Join(Kinds, ", "))
	}
	if !slices.Contains(Tones, j.Tone) {
		return nil, fmt.Errorf("%s: tone %q is not a colour token (%s)", where(id), j.Tone, strings.Join(Tones[1:], ", "))
	}
	if !slices.Contains(Variants, j.Variant) {
		return nil, fmt.Errorf("%s: variant %q is not a variant token (%s)", where(id), j.Variant, strings.Join(Variants[1:], ", "))
	}
	if j.Variant != "" && j.Kind != KindButton && j.Kind != KindCard {
		return nil, fmt.Errorf("%s: only a button or card takes variant", where(id))
	}
	if j.Weight != "" && j.Weight != "medium" && j.Weight != "semibold" {
		return nil, fmt.Errorf("%s: weight %q is not medium or semibold", where(id), j.Weight)
	}
	if j.Size != "" && j.Size != "small" && j.Size != "large" {
		return nil, fmt.Errorf("%s: size %q is not small or large", where(id), j.Size)
	}
	if j.Icon != "" && (len(j.Icon) > 48 || !iconName.MatchString(j.Icon)) {
		return nil, fmt.Errorf("%s: icon %q is not an icon's name", where(id), j.Icon)
	}
	if j.Kind == KindIcon && j.Icon == "" {
		return nil, fmt.Errorf("%s: an icon names its icon", where(id))
	}
	if j.Kind == KindButton && j.Text == "" && j.Icon == "" {
		return nil, fmt.Errorf("%s: a button requires text or icon", where(id))
	}
	if depth == 1 && j.Kind == KindTreeItem {
		return nil, fmt.Errorf("%s: a tree_item must be inside a tree", where(id))
	}
	if j.Kind != KindGrid && j.Columns != 0 {
		return nil, fmt.Errorf("%s: only a grid node takes columns", where(id))
	}
	if j.Kind == KindGrid && (j.Columns < 1 || j.Columns > maxGridCols) {
		return nil, fmt.Errorf("%s: a grid takes 1 to %d columns", where(id), maxGridCols)
	}
	var ratio float64
	if j.Kind != KindSplit && j.Ratio != nil {
		return nil, fmt.Errorf("%s: only a split node takes ratio", where(id))
	}
	if j.Kind == KindSplit {
		if j.Ratio == nil {
			ratio = defSplitRatio
		} else {
			if !finite(*j.Ratio, minSplitRatio, maxSplitRatio) {
				return nil, fmt.Errorf("%s: ratio must be from %.1f to %.1f", where(id), minSplitRatio, maxSplitRatio)
			}
			ratio = *j.Ratio
		}
	}
	if j.Fill && (depth != 1 || (j.Kind != KindColumn && j.Kind != KindRow && j.Kind != KindSplit)) {
		return nil, fmt.Errorf("%s: fill is only allowed on a root column, row, or split", where(id))
	}
	if !languageTag.MatchString(j.Language) {
		return nil, fmt.Errorf("%s: language %q is not a language's name", where(id), j.Language)
	}
	if !finite(j.Progress, 0, 1) || !finite(j.Grow, 0, maxG) || !finite(j.Gap, 0, maxGap) {
		return nil, fmt.Errorf("%s: progress is from 0 to 1, grow from 0 to %d, gap from 0 to %d", where(id), maxG, maxGap)
	}
	limit := maxNodeText
	if j.Kind == KindMarkdown || j.Kind == KindCode {
		limit = maxLongText
	}
	for _, s := range []string{j.Text, j.Value, j.Placeholder} {
		if len(s) > limit {
			return nil, fmt.Errorf("%s: text over %d KiB", where(id), limit>>10)
		}
		r.text += len(s)
	}
	if len(j.Options) > maxNodeOpts {
		return nil, fmt.Errorf("%s: more than %d options", where(id), maxNodeOpts)
	}
	for _, o := range j.Options {
		if len(o) > maxOption {
			return nil, fmt.Errorf("%s: an option is too long", where(id))
		}
		if j.Kind == KindBreadcrumb && strings.ContainsFunc(o, Spoofing) {
			return nil, fmt.Errorf("%s: breadcrumb segment holds control or reordering characters", where(id))
		}
		r.text += len(o)
	}
	if r.text > maxTreeText {
		return nil, fmt.Errorf("the tree holds more than %d KiB of text", maxTreeText>>10)
	}
	if j.Kind != KindMarkdown && j.Kind != KindCode && strings.ContainsFunc(j.Text, spoofingInline) {
		return nil, fmt.Errorf("%s: text holds control or reordering characters", where(id))
	}
	if j.Kind == KindTreeItem && strings.ContainsFunc(j.Value, spoofingInline) {
		return nil, fmt.Errorf("%s: value holds control or reordering characters", where(id))
	}
	if j.Drag != "" {
		if len(j.Drag) > maxDrag || strings.ContainsFunc(j.Drag, Spoofing) {
			return nil, fmt.Errorf("%s: drag payload is over %d bytes or holds control or reordering characters", where(id), maxDrag)
		}
		r.text += len(j.Drag)
	}
	if j.DragGroup != "" {
		if j.Drag == "" {
			return nil, fmt.Errorf("%s: dragGroup requires drag", where(id))
		}
		if len(j.DragGroup) > maxDragGroup || !dragGroupName.MatchString(j.DragGroup) {
			return nil, fmt.Errorf("%s: invalid dragGroup %q", where(id), j.DragGroup)
		}
		r.text += len(j.DragGroup)
	}
	if len(j.DropAccept) > maxDropAccept {
		return nil, fmt.Errorf("%s: more than %d dropAccept groups", where(id), maxDropAccept)
	}
	for _, g := range j.DropAccept {
		if len(g) > maxDragGroup || !dragGroupName.MatchString(g) {
			return nil, fmt.Errorf("%s: invalid dropAccept group %q", where(id), g)
		}
		r.text += len(g)
	}
	if j.Direction != "" && j.Direction != "vertical" && j.Direction != "horizontal" {
		return nil, fmt.Errorf("%s: direction %q is not vertical or horizontal", where(id), j.Direction)
	}
	if j.Direction != "" && j.Kind != KindScroll && j.Kind != KindSplit {
		return nil, fmt.Errorf("%s: only a scroll or split node takes direction", where(id))
	}
	if j.MaxHeight != 0 {
		switch j.Kind {
		case KindScroll:
			if !finite(j.MaxHeight, 0, maxScrollH) {
				return nil, fmt.Errorf("%s: maxHeight is from 0 to %d", where(id), maxScrollH)
			}
		case KindGraph:
			if !finite(j.MaxHeight, minGraphHeight, maxGraphHeight) {
				return nil, fmt.Errorf("%s: graph maxHeight is from %d to %d", where(id), minGraphHeight, maxGraphHeight)
			}
		default:
			return nil, fmt.Errorf("%s: only a scroll or graph node takes maxHeight", where(id))
		}
	}
	if r.text > maxTreeText {
		return nil, fmt.Errorf("the tree holds more than %d KiB of text", maxTreeText>>10)
	}
	n := &protocol.UiNode{
		Id: id, Kind: j.Kind, Text: j.Text, Tone: j.Tone, Variant: j.Variant, Icon: j.Icon, Weight: j.Weight, Size: j.Size,
		Value: j.Value, Checked: j.Checked, Options: j.Options, Progress: j.Progress, Language: j.Language,
		Disabled: j.Disabled, Grow: j.Grow, Gap: j.Gap, Columns: j.Columns, Ratio: ratio, Fill: j.Fill,
		Drag: j.Drag, DragGroup: j.DragGroup, DropAccept: j.DropAccept, Direction: j.Direction, MaxHeight: j.MaxHeight,
	}
	if (j.Kind == KindInput || j.Kind == KindTextarea) && n.Text == "" {
		n.Text = j.Placeholder
	}
	if j.Kind == KindTable {
		if len(j.Labels) > maxTableCols {
			return nil, fmt.Errorf("%s: a table has at most %d columns", where(id), maxTableCols)
		}
		for _, h := range j.Labels {
			if len(h) > maxOption || strings.ContainsFunc(h, Spoofing) {
				return nil, fmt.Errorf("%s: invalid table header %q", where(id), h)
			}
			r.text += len(h)
		}
		if r.text > maxTreeText {
			return nil, fmt.Errorf("the tree holds more than %d KiB of text", maxTreeText>>10)
		}
		n.Labels = j.Labels
	}
	if err := r.chart(j, n, id); err != nil {
		return nil, err
	}
	if err := r.graph(j, n, id); err != nil {
		return nil, err
	}
	var a actions
	var linkMsg json.RawMessage
	var err error
	if a.tap, err = message(j.OnTap); err != nil {
		return nil, fmt.Errorf("%s: onTap: %w", where(id), err)
	}
	if a.change, err = message(j.OnChange); err != nil {
		return nil, fmt.Errorf("%s: onChange: %w", where(id), err)
	}
	if a.submit, err = message(j.OnSubmit); err != nil {
		return nil, fmt.Errorf("%s: onSubmit: %w", where(id), err)
	}
	if a.drop, err = message(j.OnDrop); err != nil {
		return nil, fmt.Errorf("%s: onDrop: %w", where(id), err)
	}
	if linkMsg, err = message(j.OnLink); err != nil {
		return nil, fmt.Errorf("%s: onLink: %w", where(id), err)
	}
	if linkMsg != nil && j.Kind != KindMarkdown {
		return nil, fmt.Errorf("%s: only a markdown node takes onLink", where(id))
	}
	switch j.Kind {
	case KindInput, KindTextarea, KindToggle, KindSelect, KindTabs, KindBreadcrumb, KindGraph:
		if a.tap != nil {
			return nil, fmt.Errorf("%s: a %s takes onChange, not onTap", where(id), j.Kind)
		}
		if j.Drag != "" || a.drop != nil {
			return nil, fmt.Errorf("%s: a %s does not take drag or onDrop", where(id), j.Kind)
		}
	case KindCollapsible:
		if a.tap != nil {
			return nil, fmt.Errorf("%s: a collapsible takes onChange, not onTap", where(id))
		}
	case KindTreeItem:
		// tree_item may take onTap (select), onChange (expand/collapse), drag, and onDrop.
	case KindMarkdown:
		if a.tap != nil || a.change != nil || a.submit != nil || a.drop != nil {
			return nil, fmt.Errorf("%s: a markdown takes onLink, not onTap/onChange/onSubmit/onDrop", where(id))
		}
		if j.Drag != "" {
			return nil, fmt.Errorf("%s: a markdown does not take drag", where(id))
		}
		a.change = linkMsg
	case KindDivider, KindProgress, KindCode, KindChart:
		if a.tap != nil || a.change != nil || a.submit != nil || a.drop != nil {
			return nil, fmt.Errorf("%s: a %s sends no messages", where(id), j.Kind)
		}
		if j.Drag != "" {
			return nil, fmt.Errorf("%s: a %s does not take drag", where(id), j.Kind)
		}
	default:
		if a.change != nil || a.submit != nil {
			return nil, fmt.Errorf("%s: a %s takes onTap, not onChange", where(id), j.Kind)
		}
	}
	if j.Kind == KindTabs {
		if len(j.Options) == 0 {
			return nil, fmt.Errorf("%s: tabs requires at least one option", where(id))
		}
		if j.Value != "" && !slices.Contains(j.Options, j.Value) {
			return nil, fmt.Errorf("%s: tabs value %q is not one of its options", where(id), j.Value)
		}
	}
	if j.Kind == KindBreadcrumb && len(j.Options) == 0 {
		return nil, fmt.Errorf("%s: breadcrumb requires at least one option", where(id))
	}
	if a.submit != nil && j.Kind != KindInput && j.Kind != KindTextarea {
		return nil, fmt.Errorf("%s: only an input or textarea takes onSubmit", where(id))
	}
	if len(j.DropAccept) > 0 && a.drop == nil {
		return nil, fmt.Errorf("%s: dropAccept requires onDrop", where(id))
	}
	n.Tap, n.Change, n.Drop = a.tap != nil, a.change != nil || a.submit != nil, a.drop != nil
	if n.Tap || n.Change || n.Drop {
		r.t.actions[id] = a
	}
	container := j.Kind == KindColumn || j.Kind == KindRow || j.Kind == KindStack || j.Kind == KindCard ||
		j.Kind == KindScroll || j.Kind == KindWrap || j.Kind == KindGrid || j.Kind == KindSplit ||
		j.Kind == KindCollapsible || j.Kind == KindTree || j.Kind == KindTreeItem || j.Kind == KindList || j.Kind == KindTable
	if !container && len(j.Children) > 0 {
		return nil, fmt.Errorf("%s: a %s holds no children", where(id), j.Kind)
	}
	if j.Kind == KindSplit && len(j.Children) != 2 {
		return nil, fmt.Errorf("%s: a split holds exactly 2 children, got %d", where(id), len(j.Children))
	}
	if j.Kind == KindTable {
		for i, c := range j.Children {
			if c != nil && c.Kind != KindRow {
				return nil, fmt.Errorf("%s: table child %d must be a row, got %q", where(id), i, c.Kind)
			}
		}
	}
	if j.Kind == KindTree || j.Kind == KindTreeItem {
		for i, c := range j.Children {
			if c != nil && c.Kind != KindTreeItem {
				return nil, fmt.Errorf("%s: %s child %d must be a tree_item, got %q", where(id), j.Kind, i, c.Kind)
			}
		}
	}
	keys := map[string]bool{}
	for i, c := range j.Children {
		if c == nil {
			return nil, fmt.Errorf("%s: child %d is null", where(id), i)
		}
		if c.Kind == KindTreeItem && j.Kind != KindTree && j.Kind != KindTreeItem {
			return nil, fmt.Errorf("%s: tree_item child %d must be inside a tree or tree_item", where(id), i)
		}
		k := c.Key
		if k == "" {
			k = fmt.Sprintf("#%d", i)
		} else if len(k) > maxKey || strings.ContainsAny(k, "/#") || strings.ContainsFunc(k, Spoofing) {
			return nil, fmt.Errorf("%s: key %q is over %d bytes or holds / or #", where(id), k, maxKey)
		}
		if keys[k] {
			return nil, fmt.Errorf("%s: two children have the key %q", where(id), k)
		}
		keys[k] = true
		child, err := r.node(c, id+"/"+k, depth+1)
		if err != nil {
			return nil, err
		}
		n.Children = append(n.Children, child)
	}
	return n, nil
}

// chart reads a chart node's chart, series and labels into n, and
// refuses them on any other node: a kind of chart, at most maxSeries
// series of maxPoints finite numbers each, maxChartPoints in all, each
// series's colour a token, and its labels short text, as many as the
// points.
func (r *treeReader) chart(j *jsNode, n *protocol.UiNode, id string) error {
	if j.Kind != KindChart {
		if j.Chart != "" || len(j.Series) > 0 || (len(j.Labels) > 0 && j.Kind != KindTable) {
			return fmt.Errorf("%s: only a chart has chart, series or labels", where(id))
		}
		return nil
	}
	if !slices.Contains(Charts, j.Chart) {
		return fmt.Errorf("%s: chart %q is not one of %s", where(id), j.Chart, strings.Join(Charts, ", "))
	}
	radial := j.Chart == "donut" || j.Chart == "pie"
	if radial {
		if len(j.Series) != 1 {
			return fmt.Errorf("%s: a %s chart has 1 series", where(id), j.Chart)
		}
	} else if len(j.Series) == 0 || len(j.Series) > maxSeries {
		return fmt.Errorf("%s: a chart has 1 to %d series", where(id), maxSeries)
	}
	points := 0
	for i, sj := range j.Series {
		if sj == nil {
			return fmt.Errorf("%s: series %d is null", where(id), i)
		}
		if !slices.Contains(Tones, sj.Tone) {
			return fmt.Errorf("%s: series %d: tone %q is not a colour token", where(id), i, sj.Tone)
		}
		if len(sj.Name) > maxSeriesName || strings.ContainsFunc(sj.Name, Spoofing) {
			return fmt.Errorf("%s: series %d: a name is at most %d bytes, without control or reordering characters", where(id), i, maxSeriesName)
		}
		if radial {
			if len(sj.Values) == 0 || len(sj.Values) > maxPieSlices {
				return fmt.Errorf("%s: a %s chart has 1 to %d values", where(id), j.Chart, maxPieSlices)
			}
			if len(j.Labels) != len(sj.Values) {
				return fmt.Errorf("%s: a %s chart requires %d labels matching its values", where(id), j.Chart, len(sj.Values))
			}
		} else {
			if len(sj.Values) == 0 || len(sj.Values) > maxPoints {
				return fmt.Errorf("%s: series %d has 1 to %d values", where(id), i, maxPoints)
			}
			if len(j.Labels) > 0 && len(sj.Values) != len(j.Labels) {
				return fmt.Errorf("%s: series %d has %d values for %d labels", where(id), i, len(sj.Values), len(j.Labels))
			}
		}
		points += len(sj.Values)
		s := &protocol.UiSeries{Name: sj.Name, Tone: sj.Tone, Values: make([]float64, len(sj.Values))}
		for k, v := range sj.Values {
			lo := -maxChartValue
			if radial {
				lo = 0
			}
			if v == nil || !finite(*v, lo, maxChartValue) {
				if radial {
					return fmt.Errorf("%s: series %d: value %d is not a finite non-negative number", where(id), i, k)
				}
				return fmt.Errorf("%s: series %d: value %d is not a finite number", where(id), i, k)
			}
			s.Values[k] = *v
		}
		r.text += len(sj.Name)
		n.Series = append(n.Series, s)
	}
	if points > maxChartPoints {
		return fmt.Errorf("%s: a chart has at most %d points in all", where(id), maxChartPoints)
	}
	if len(j.Labels) > maxPoints {
		return fmt.Errorf("%s: a chart has at most %d labels", where(id), maxPoints)
	}
	for _, l := range j.Labels {
		if len(l) > maxLabel || strings.ContainsFunc(l, Spoofing) {
			return fmt.Errorf("%s: a label is at most %d bytes, without control or reordering characters", where(id), maxLabel)
		}
		r.text += len(l)
	}
	if r.text > maxTreeText {
		return fmt.Errorf("the tree holds more than %d KiB of text", maxTreeText>>10)
	}
	n.Chart, n.Labels = j.Chart, j.Labels
	return nil
}

// graph reads a graph node's graphNodes and graphEdges into n, and refuses
// them on any other node.
func (r *treeReader) graph(j *jsNode, n *protocol.UiNode, id string) error {
	if j.Kind != KindGraph {
		if len(j.GraphNodes) > 0 || len(j.GraphEdges) > 0 {
			return fmt.Errorf("%s: only a graph has graphNodes or graphEdges", where(id))
		}
		return nil
	}
	if len(j.GraphNodes) > maxGraphNodes {
		return fmt.Errorf("%s: a graph has at most %d nodes", where(id), maxGraphNodes)
	}
	if len(j.GraphEdges) > maxGraphEdges {
		return fmt.Errorf("%s: a graph has at most %d edges", where(id), maxGraphEdges)
	}
	nodeIDs := make(map[string]bool, len(j.GraphNodes))
	for i, gn := range j.GraphNodes {
		if gn == nil {
			return fmt.Errorf("%s: graph node %d is null", where(id), i)
		}
		if gn.ID == "" || len(gn.ID) > maxGraphID || strings.ContainsFunc(gn.ID, Spoofing) {
			return fmt.Errorf("%s: graph node %d has invalid id %q", where(id), i, gn.ID)
		}
		if nodeIDs[gn.ID] {
			return fmt.Errorf("%s: graph node id %q is duplicated", where(id), gn.ID)
		}
		nodeIDs[gn.ID] = true
		if gn.Label == "" || len(gn.Label) > maxGraphLabel || strings.ContainsFunc(gn.Label, Spoofing) {
			return fmt.Errorf("%s: graph node %q has invalid label", where(id), gn.ID)
		}
		if len(gn.Group) > maxGraphGroup || strings.ContainsFunc(gn.Group, Spoofing) {
			return fmt.Errorf("%s: graph node %q has invalid group", where(id), gn.ID)
		}
		if !slices.Contains(Tones, gn.Tone) {
			return fmt.Errorf("%s: graph node %q: tone %q is not a colour token", where(id), gn.ID, gn.Tone)
		}
		if len(gn.Subtitle) > maxGraphSubtitle || strings.ContainsFunc(gn.Subtitle, Spoofing) {
			return fmt.Errorf("%s: graph node %q has invalid subtitle", where(id), gn.ID)
		}
		w := 1.0
		if gn.Weight != nil {
			if !finite(*gn.Weight, 0, maxGraphWeight) || *gn.Weight <= 0 {
				return fmt.Errorf("%s: graph node %q: weight must be a finite positive number", where(id), gn.ID)
			}
			w = *gn.Weight
		}
		r.text += len(gn.ID) + len(gn.Label) + len(gn.Group) + len(gn.Subtitle)
		n.GraphNodes = append(n.GraphNodes, &protocol.UiGraphNode{
			Id: gn.ID, Label: gn.Label, Group: gn.Group, Tone: gn.Tone, Subtitle: gn.Subtitle, Weight: w,
		})
	}
	for i, ge := range j.GraphEdges {
		if ge == nil {
			return fmt.Errorf("%s: graph edge %d is null", where(id), i)
		}
		if !nodeIDs[ge.From] {
			return fmt.Errorf("%s: graph edge %d: unknown source node %q", where(id), i, ge.From)
		}
		if !nodeIDs[ge.To] {
			return fmt.Errorf("%s: graph edge %d: unknown target node %q", where(id), i, ge.To)
		}
		if len(ge.Label) > maxGraphLabel || strings.ContainsFunc(ge.Label, Spoofing) {
			return fmt.Errorf("%s: graph edge %d has invalid label", where(id), i)
		}
		if !slices.Contains(Tones, ge.Tone) {
			return fmt.Errorf("%s: graph edge %d: tone %q is not a colour token", where(id), i, ge.Tone)
		}
		r.text += len(ge.From) + len(ge.To) + len(ge.Label)
		n.GraphEdges = append(n.GraphEdges, &protocol.UiGraphEdge{
			From: ge.From, To: ge.To, Label: ge.Label, Tone: ge.Tone, Dashed: ge.Dashed,
		})
	}
	if j.Value != "" && !nodeIDs[j.Value] {
		return fmt.Errorf("%s: graph value %q does not match any node id", where(id), j.Value)
	}
	if r.text > maxTreeText {
		return fmt.Errorf("the tree holds more than %d KiB of text", maxTreeText>>10)
	}
	return nil
}

// message reads a node's message: null, or an array of a name and up to
// a few KiB of arguments.
func message(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	if len(raw) > maxMessage {
		return nil, fmt.Errorf("over %d KiB", maxMessage>>10)
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil || len(parts) == 0 {
		return nil, fmt.Errorf("a message is [name, ...arguments]")
	}
	var name string
	if err := json.Unmarshal(parts[0], &name); err != nil || !messageName.MatchString(name) {
		return nil, fmt.Errorf("a message's name is a word")
	}
	return raw, nil
}

func finite(v, lo, hi float64) bool { return !math.IsNaN(v) && v >= lo && v <= hi }

// spoofingInline is Spoofing but for a line break, which text may hold.
func spoofingInline(r rune) bool { return r != '\n' && r != '\t' && Spoofing(r) }

// Spoofing says r is a control character or one of Unicode's marks that
// reorder or hide text, which could make a title or a label read as
// something else (protocol.Spoofing).
func Spoofing(r rune) bool { return protocol.Spoofing(r) }

func where(id string) string {
	if id == "" {
		return "the root"
	}
	return "node " + id
}
