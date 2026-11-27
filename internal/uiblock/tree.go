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
	"cmp"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ironpark/mako/internal/protocol"
	"google.golang.org/protobuf/proto"
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
	KindCanvas      = "canvas"
	KindTimeline    = "timeline"
	KindMenu        = "menu"
	KindMenuItem    = "menu_item"
	KindCheckbox    = "checkbox"
	KindDialog      = "dialog"
	KindDag         = "dag"
	KindDiff        = "diff"
	KindAlert       = "alert"
	KindEmpty       = "empty"
	KindStat        = "stat"
	KindRadio       = "radio"
	KindSlider      = "slider"
	KindKbd         = "kbd"
	KindSkeleton    = "skeleton"
	KindSpacer      = "spacer"
)

// Kinds are the vocabulary, in the order docs/plugins.md lists it.
var Kinds = []string{KindColumn, KindRow, KindStack, KindCard, KindScroll, KindWrap, KindGrid, KindSplit,
	KindCollapsible, KindBreadcrumb, KindTree, KindTreeItem, KindText, KindIcon, KindPill, KindChip, KindButton,
	KindList, KindInput, KindTextarea, KindToggle, KindSelect, KindTabs, KindTable, KindMarkdown, KindCode,
	KindProgress, KindDivider, KindChart, KindGraph, KindCanvas, KindTimeline, KindMenu, KindMenuItem, KindCheckbox, KindDialog, KindDag,
	KindDiff, KindAlert, KindEmpty, KindStat, KindRadio, KindSlider, KindKbd, KindSkeleton, KindSpacer}

// Charts are the kinds of chart a chart node draws.
var Charts = []string{"line", "bar", "sparkline", "donut", "pie", "area", "stacked-bar", "heatmap"}

// CanvasOps are the sandboxed 2D vector ops a canvas node accepts.
var CanvasOps = []string{"roundRect", "circle", "line", "bezier", "text", "icon"}

// CanvasTones are the strict theme colour tokens a canvas command accepts.
var CanvasTones = []string{"", "muted", "faint", "accent", "success", "warning", "danger", "surface", "surfaceAlt", "border", "text", "ink"}

// Tones are the colour tokens a node may take.
var Tones = []string{"", "muted", "faint", "accent", "success", "warning", "danger"}

// Checks are the tri-state check tokens a checkbox or tree_item may take.
var Checks = []string{"", "on", "off", "mixed"}

// GraphLayouts are the layout modes a graph node accepts.
var GraphLayouts = []string{"", "force", "hierarchy", "radial"}

// GraphNodeShapes are the visual shapes a graph node accepts.
var GraphNodeShapes = []string{"", "card", "circle"}

// GraphStatuses are the live status tokens a graph node accepts.
var GraphStatuses = []string{"", "working", "waiting", "idle", "error", "done", "compacting", "stopped"}

// BadgeVariants are the style variant tokens a UiBadge may take.
var BadgeVariants = []string{"", "soft", "solid", "outline", "mono"}

// variantsOf says which variant tokens each node kind accepts (excluding "").
var variantsOf = map[string][]string{
	KindRow:      {"toolbar"},
	KindText:     {"mono", "caption", "heading"},
	KindButton:   {"primary", "secondary", "outline", "ghost", "danger"},
	KindCard:     {"primary", "secondary", "outline", "ghost", "danger", "soft"},
	KindPill:     {"soft", "solid", "outline"},
	KindChip:     {"soft", "solid", "outline"},
	KindInput:    {"search", "password"},
	KindTextarea: {"mono"},
	KindTabs:     {"pills", "underline"},
	KindTable:    {"compact", "striped"},
	KindProgress: {"spinner", "ring"},
	KindScroll:   {"follow"},
	KindTreeItem: {"badge"},
	KindGraph:    {"cards"},
	KindMenu:     {"ghost", "outline", "context"},
	KindDialog:   {"danger"},
	KindDiff:     {"unified", "split"},
	KindAlert:    {"soft", "outline", "solid"},
	KindSkeleton: {"text", "rect", "circle"},
}

// Variants are every style variant token, for docs; VariantsOf says which a kind takes.
var Variants = []string{"", "primary", "secondary", "outline", "ghost", "danger", "soft", "solid", "spinner", "ring",
	"follow", "badge", "cards", "toolbar", "context", "mono", "caption", "heading", "search", "password", "pills", "underline",
	"compact", "striped", "unified", "split", "text", "rect", "circle"}

// VariantsOf returns the non-empty variant tokens accepted by kind.
func VariantsOf(kind string) []string {
	return slices.Clone(variantsOf[kind])
}

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
	maxGraphCoord    = 1e6
	maxGraphBadges   = 6
	maxGraphEdgeKind = 32
	minGraphHeight   = 120
	maxGraphHeight   = 1200
	// Badges, menus, context menus, and dag bounds (revision 46).
	maxBadges       = 8
	maxBadgeText    = 32
	maxContextArg   = 256
	maxPlaceholder  = 256
	maxMenuDepth    = 2
	maxDagRows      = 1000
	maxDagID        = 64
	maxDagParents   = 8
	maxDagText      = 320
	maxDagDetail    = 320
	maxDagMeta      = 128
	maxDagRowBadges = 6
	// Diff and slider bounds (revision 49).
	maxDiffLines    = 2000
	maxDiffLineText = 4 << 10
	maxDiffLabel    = 256
	maxSliderBound  = 1e12
	// Sandboxed 2D canvas bounds.
	maxCanvasCommands = 1000
	maxCanvasCoord    = 1e6
	maxCanvasSize     = 4000
	maxCanvasText     = 1024
	maxCanvasHitID    = 128
	maxCanvasStroke   = 64
	maxCanvasFontSize = 256
	// Timeline bounds.
	maxTimelineLanes    = 64
	maxTimelineSpans    = 500
	maxTimelineID       = 128
	maxTimelineLabel    = 128
	maxTimelineSubtitle = 160
	maxTimelineKind     = 32
	maxTimelineMs       = 1e15
)

var (
	iconName          = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	messageName       = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	languageTag       = regexp.MustCompile(`^[a-z0-9+#-]{0,24}$`)
	dragGroupName     = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	graphEdgeKindName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,31}$`)
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
	OnDoubleTap json.RawMessage `json:"onDoubleTap"`
	OnChange    json.RawMessage `json:"onChange"`
	OnSubmit    json.RawMessage `json:"onSubmit"`
	OnLink      json.RawMessage `json:"onLink"`
	Drag        string          `json:"drag"`
	DragGroup   string          `json:"dragGroup"`
	OnDrop      json.RawMessage `json:"onDrop"`
	DropAccept  []string        `json:"dropAccept"`
	Direction   string          `json:"direction"`
	MaxHeight   float64         `json:"maxHeight"`
	Layout      string          `json:"layout"`
	Minimap     bool            `json:"minimap"`
	Children    []*jsNode       `json:"children"`
	Chart       string          `json:"chart"`
	Series      []*jsSeries     `json:"series"`
	Labels      []string        `json:"labels"`
	GraphNodes  []*jsGraphNode  `json:"graphNodes"`
	GraphEdges  []*jsGraphEdge  `json:"graphEdges"`
	Badges      []*jsBadge      `json:"badges"`
	Check       string          `json:"check"`
	OnCheck     json.RawMessage `json:"onCheck"`
	ContextMenu string          `json:"contextMenu"`
	ContextArg  string          `json:"contextArg"`
	Rows        []*jsRow        `json:"rows"`
	Min         *float64        `json:"min"`
	Max         *float64        `json:"max"`
	Step        *float64        `json:"step"`
	DiffLines      []*jsDiffLine      `json:"diffLines"`
	DiffFrom       string             `json:"diffFrom"`
	DiffTo         string             `json:"diffTo"`
	Width          *float64           `json:"width"`
	Height         *float64           `json:"height"`
	Pan            *bool              `json:"pan"`
	Zoom           *bool              `json:"zoom"`
	CanvasCommands []*jsCanvasCommand `json:"canvasCommands"`
	TimelineLanes  []*jsTimelineLane  `json:"timelineLanes"`
}

// jsCanvasCommand is one sandboxed 2D vector drawing command as the prelude writes it.
type jsCanvasCommand struct {
	Op          string          `json:"op"`
	X           float64         `json:"x"`
	Y           float64         `json:"y"`
	W           float64         `json:"w"`
	H           float64         `json:"h"`
	R           float64         `json:"r"`
	X1          float64         `json:"x1"`
	Y1          float64         `json:"y1"`
	X2          float64         `json:"x2"`
	Y2          float64         `json:"y2"`
	Cx1         float64         `json:"cx1"`
	Cy1         float64         `json:"cy1"`
	Cx2         float64         `json:"cx2"`
	Cy2         float64         `json:"cy2"`
	StrokeWidth float64         `json:"strokeWidth"`
	Alpha       *float64        `json:"alpha"`
	Text        string          `json:"text"`
	Icon        string          `json:"icon"`
	Size        json.RawMessage `json:"size"`
	Tone        string          `json:"tone"`
	FillTone    string          `json:"fillTone"`
	StrokeTone  string          `json:"strokeTone"`
	HitID       string          `json:"hitId"`
}

// jsTimelineLane is one track lane of a timeline node as the prelude writes it.
type jsTimelineLane struct {
	ID    string            `json:"id"`
	Label string            `json:"label"`
	Spans []*jsTimelineSpan `json:"spans"`
}

// jsTimelineSpan is one timed interval bar inside a timeline lane.
type jsTimelineSpan struct {
	ID       string   `json:"id"`
	StartMs  *float64 `json:"startMs"`
	EndMs    *float64 `json:"endMs"`
	Label    string   `json:"label"`
	Subtitle string   `json:"subtitle"`
	Tone     string   `json:"tone"`
	Kind     string   `json:"kind"`
}

// jsDiffLine is one line of a diff node as the prelude writes it.
type jsDiffLine struct {
	Op      string `json:"op"`
	Text    string `json:"text"`
	OldLine int32  `json:"oldLine"`
	NewLine int32  `json:"newLine"`
}

// jsSeries is a chart's series as the prelude writes it.
type jsSeries struct {
	Name   string     `json:"name"`
	Tone   string     `json:"tone"`
	Values []*float64 `json:"values"`
}

// jsGraphNode is a node of a graph canvas as the prelude writes it.
type jsGraphNode struct {
	ID          string     `json:"id"`
	Label       string     `json:"label"`
	Group       string     `json:"group"`
	Tone        string     `json:"tone"`
	Subtitle    string     `json:"subtitle"`
	Weight      *float64   `json:"weight"`
	Shape       string     `json:"shape"`
	Icon        string     `json:"icon"`
	Status      string     `json:"status"`
	Badges      []*jsBadge `json:"badges"`
	X           *float64   `json:"x"`
	Y           *float64   `json:"y"`
	HasPos      *bool      `json:"hasPos"`
	Pinned      bool       `json:"pinned"`
	ContextMenu string     `json:"contextMenu"`
	ContextArg  string     `json:"contextArg"`
}

// jsGraphEdge is an edge of a graph canvas as the prelude writes it.
type jsGraphEdge struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Label  string `json:"label"`
	Tone   string `json:"tone"`
	Dashed bool   `json:"dashed"`
	Active bool   `json:"active"`
	Kind   string `json:"kind"`
}

// jsBadge is a small trailing tag on a node or dag row.
type jsBadge struct {
	Text    string `json:"text"`
	Tone    string `json:"tone"`
	Variant string `json:"variant"`
	Icon    string `json:"icon"`
}

// jsRow is one data-driven row of a dag node.
type jsRow struct {
	ID      string     `json:"id"`
	Parents []string   `json:"parents"`
	Text    string     `json:"text"`
	Detail  string     `json:"detail"`
	Meta    string     `json:"meta"`
	Badges  []*jsBadge `json:"badges"`
	Tone    string     `json:"tone"`
	Strong  bool       `json:"strong"`
	Icon    string     `json:"icon"`
}

// actions are what a node's events send: each a message, the JSON array
// of its name and arguments.
type actions struct{ tap, doubleTap, change, submit, drop, check json.RawMessage }

// Tree is a tree, read: the protocol's tree and its nodes' messages by
// node id.
type Tree struct {
	Root            *protocol.UiNode
	actions         map[string]actions
	itemContextMenu map[string]string
	contextArgs     map[string]map[string]bool
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
	r := &treeReader{
		t: &Tree{
			actions:         map[string]actions{},
			itemContextMenu: map[string]string{},
			contextArgs:     map[string]map[string]bool{},
		},
		contextMenus: map[string]string{},
	}
	n, err := r.node(&root, "", 1, 0, "")
	if err != nil {
		return nil, err
	}
	for _, ref := range r.contextRefs {
		if _, ok := r.contextMenus[ref.menu]; !ok {
			return nil, fmt.Errorf("%s: unknown context menu %q", ref.where, ref.menu)
		}
	}
	r.t.Root = n
	return r.t, nil
}

type contextRef struct {
	where string
	menu  string
}

type treeReader struct {
	t            *Tree
	nodes        int
	text         int
	dialogs      int
	contextMenus map[string]string
	contextRefs  []contextRef
}

func (r *treeReader) node(j *jsNode, id string, depth, menuDepth int, ctxMenuKey string) (*protocol.UiNode, error) {
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
	if j.Variant != "" {
		allowed := variantsOf[j.Kind]
		if len(allowed) == 0 {
			return nil, fmt.Errorf("%s: a %s takes no variant", where(id), j.Kind)
		}
		if !slices.Contains(allowed, j.Variant) {
			return nil, fmt.Errorf("%s: a %s takes no variant %q (%s)", where(id), j.Kind, j.Variant, strings.Join(allowed, ", "))
		}
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
	if j.Kind == KindMenu {
		if j.Variant == "context" {
			if menuDepth > 0 {
				return nil, fmt.Errorf("%s: a context menu cannot be nested inside another menu", where(id))
			}
			if j.Key == "" || len(j.Key) > maxKey || strings.ContainsAny(j.Key, "/#") || strings.ContainsFunc(j.Key, Spoofing) {
				return nil, fmt.Errorf("%s: a context menu requires a valid key", where(id))
			}
			if _, dup := r.contextMenus[j.Key]; dup {
				return nil, fmt.Errorf("%s: duplicate context menu key %q", where(id), j.Key)
			}
			r.contextMenus[j.Key] = id
			ctxMenuKey = j.Key
		} else if j.Text == "" && j.Icon == "" {
			return nil, fmt.Errorf("%s: a menu requires text or icon", where(id))
		}
		menuDepth++
		if menuDepth > maxMenuDepth {
			return nil, fmt.Errorf("%s: menu nesting exceeds depth %d", where(id), maxMenuDepth)
		}
	}
	if j.Kind == KindMenuItem {
		if menuDepth == 0 {
			return nil, fmt.Errorf("%s: a menu_item must be inside a menu", where(id))
		}
		if j.Text == "" {
			return nil, fmt.Errorf("%s: a menu_item requires text", where(id))
		}
		if ctxMenuKey != "" {
			r.t.itemContextMenu[id] = ctxMenuKey
		}
	}
	if j.Kind == KindDialog {
		r.dialogs++
		if r.dialogs > 1 {
			return nil, fmt.Errorf("%s: a tree may contain at most one dialog", where(id))
		}
	}
	if !slices.Contains(Checks, j.Check) {
		return nil, fmt.Errorf("%s: check %q is not on, off or mixed", where(id), j.Check)
	}
	if j.Check != "" && j.Kind != KindCheckbox && j.Kind != KindTreeItem {
		return nil, fmt.Errorf("%s: only a checkbox or tree_item takes check", where(id))
	}
	checkVal := j.Check
	if j.Kind == KindCheckbox && checkVal == "" {
		checkVal = "off"
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
	if j.Fill && j.Kind != KindGraph && j.Kind != KindCanvas && j.Kind != KindTimeline && j.Kind != KindChart && (depth != 1 || (j.Kind != KindColumn && j.Kind != KindRow && j.Kind != KindSplit)) {
		return nil, fmt.Errorf("%s: fill is only allowed on a root column, row, or split, or a graph, canvas, timeline or chart", where(id))
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
		if strings.ContainsFunc(o, Spoofing) {
			return nil, fmt.Errorf("%s: option holds control or reordering characters", where(id))
		}
		r.text += len(o)
	}
	if r.text > maxTreeText {
		return nil, fmt.Errorf("the tree holds more than %d KiB of text", maxTreeText>>10)
	}
	if j.Kind != KindMarkdown && j.Kind != KindCode && strings.ContainsFunc(j.Text, spoofingInline) {
		return nil, fmt.Errorf("%s: text holds control or reordering characters", where(id))
	}
	if strings.ContainsFunc(j.Placeholder, spoofingInline) {
		return nil, fmt.Errorf("%s: placeholder holds control or reordering characters", where(id))
	}
	if utf8.RuneCountInString(j.Placeholder) > maxPlaceholder {
		return nil, fmt.Errorf("%s: placeholder is over %d characters", where(id), maxPlaceholder)
	}
	if strings.ContainsFunc(j.Value, spoofingInline) {
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
	if j.Direction != "" {
		switch j.Kind {
		case KindScroll, KindRadio:
			if j.Direction != "vertical" && j.Direction != "horizontal" {
				return nil, fmt.Errorf("%s: direction %q is not vertical or horizontal", where(id), j.Direction)
			}
		case KindSplit:
			if j.Direction != "vertical" && j.Direction != "horizontal" && j.Direction != "auto" {
				return nil, fmt.Errorf("%s: direction %q is not vertical, horizontal or auto", where(id), j.Direction)
			}
		default:
			return nil, fmt.Errorf("%s: only a scroll, split or radio node takes direction", where(id))
		}
	}
	if j.MaxHeight != 0 {
		switch j.Kind {
		case KindScroll, KindDag, KindCode, KindDiff, KindSkeleton, KindChart, KindCanvas, KindTimeline:
			if !finite(j.MaxHeight, 0, maxScrollH) {
				return nil, fmt.Errorf("%s: maxHeight is from 0 to %d", where(id), maxScrollH)
			}
		case KindGraph:
			if !finite(j.MaxHeight, minGraphHeight, maxGraphHeight) {
				return nil, fmt.Errorf("%s: graph maxHeight is from %d to %d", where(id), minGraphHeight, maxGraphHeight)
			}
		default:
			return nil, fmt.Errorf("%s: only a scroll, graph, dag, code, diff, skeleton, chart, canvas or timeline node takes maxHeight", where(id))
		}
	}
	if j.Layout != "" && j.Kind != KindGraph {
		return nil, fmt.Errorf("%s: only a graph node takes layout", where(id))
	}
	if j.Kind == KindGraph && !slices.Contains(GraphLayouts, j.Layout) {
		return nil, fmt.Errorf("%s: graph layout %q is not force, hierarchy or radial", where(id), j.Layout)
	}
	if j.Minimap && j.Kind != KindGraph {
		return nil, fmt.Errorf("%s: only a graph node takes minimap", where(id))
	}
	if j.ContextMenu != "" {
		if len(j.ContextMenu) > maxKey || strings.ContainsAny(j.ContextMenu, "/#") || strings.ContainsFunc(j.ContextMenu, Spoofing) {
			return nil, fmt.Errorf("%s: invalid contextMenu %q", where(id), j.ContextMenu)
		}
		r.text += len(j.ContextMenu)
		r.contextRefs = append(r.contextRefs, contextRef{where: where(id), menu: j.ContextMenu})
	}
	if j.ContextArg != "" {
		if j.ContextMenu == "" {
			return nil, fmt.Errorf("%s: contextArg requires contextMenu", where(id))
		}
		if utf8.RuneCountInString(j.ContextArg) > maxContextArg || strings.ContainsFunc(j.ContextArg, Spoofing) {
			return nil, fmt.Errorf("%s: contextArg is over %d characters or holds control or reordering characters", where(id), maxContextArg)
		}
		r.text += len(j.ContextArg)
	}
	if r.text > maxTreeText {
		return nil, fmt.Errorf("the tree holds more than %d KiB of text", maxTreeText>>10)
	}
	n := &protocol.UiNode{
		Id: id, Kind: j.Kind, Text: j.Text, Tone: j.Tone, Variant: j.Variant, Icon: j.Icon, Weight: j.Weight, Size: j.Size,
		Value: j.Value, Checked: j.Checked, Options: j.Options, Progress: j.Progress, Language: j.Language,
		Disabled: j.Disabled, Grow: j.Grow, Gap: j.Gap, Columns: j.Columns, Ratio: ratio, Fill: j.Fill,
		Drag: j.Drag, DragGroup: j.DragGroup, DropAccept: j.DropAccept, Direction: j.Direction, MaxHeight: j.MaxHeight,
		Check: checkVal, ContextMenu: j.ContextMenu, ContextArg: j.ContextArg,
		GraphLayout: j.Layout, Minimap: j.Minimap,
	}
	if (j.Kind == KindInput || j.Kind == KindTextarea) && n.Text == "" {
		n.Text = j.Placeholder
	}
	if j.Placeholder != "" {
		switch j.Kind {
		case KindInput, KindTextarea, KindButton, KindMenu, KindTreeItem, KindCheckbox, KindIcon, KindPill, KindChip, KindStat, KindDiff, KindSlider, KindRadio, KindAlert, KindEmpty:
			n.Placeholder = j.Placeholder
		}
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
	if len(j.Badges) > 0 {
		badges, err := r.badges(j.Badges, maxBadges, where(id))
		if err != nil {
			return nil, err
		}
		n.Badges = badges
	}
	if err := r.chart(j, n, id); err != nil {
		return nil, err
	}
	if err := r.graph(j, n, id); err != nil {
		return nil, err
	}
	if err := r.dag(j, n, id); err != nil {
		return nil, err
	}
	if err := r.slider(j, n, id); err != nil {
		return nil, err
	}
	if err := r.diff(j, n, id); err != nil {
		return nil, err
	}
	if err := r.canvas(j, n, id); err != nil {
		return nil, err
	}
	if err := r.timeline(j, n, id); err != nil {
		return nil, err
	}
	if j.ContextMenu != "" {
		args := r.t.contextArgs[j.ContextMenu]
		if args == nil {
			args = map[string]bool{}
			r.t.contextArgs[j.ContextMenu] = args
		}
		switch j.Kind {
		case KindDag:
			for _, row := range n.Rows {
				args[row.GetId()] = true
			}
			if j.ContextArg != "" {
				args[j.ContextArg] = true
			}
		case KindGraph:
			for _, gn := range n.GraphNodes {
				if gn.GetContextMenu() == "" {
					args[cmp.Or(gn.GetContextArg(), gn.GetId())] = true
				}
			}
			if j.ContextArg != "" {
				args[j.ContextArg] = true
			}
		default:
			args[j.ContextArg] = true
		}
	}
	var a actions
	var linkMsg json.RawMessage
	var err error
	if a.tap, err = message(j.OnTap); err != nil {
		return nil, fmt.Errorf("%s: onTap: %w", where(id), err)
	}
	if a.doubleTap, err = message(j.OnDoubleTap); err != nil {
		return nil, fmt.Errorf("%s: onDoubleTap: %w", where(id), err)
	}
	if a.doubleTap != nil && j.Kind != KindGraph {
		return nil, fmt.Errorf("%s: only a graph node takes onDoubleTap", where(id))
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
	if a.check, err = message(j.OnCheck); err != nil {
		return nil, fmt.Errorf("%s: onCheck: %w", where(id), err)
	}
	if linkMsg, err = message(j.OnLink); err != nil {
		return nil, fmt.Errorf("%s: onLink: %w", where(id), err)
	}
	if linkMsg != nil && j.Kind != KindMarkdown {
		return nil, fmt.Errorf("%s: only a markdown node takes onLink", where(id))
	}
	if a.check != nil {
		if j.Kind != KindTreeItem {
			return nil, fmt.Errorf("%s: only a tree_item takes onCheck", where(id))
		}
		if j.Check == "" {
			return nil, fmt.Errorf("%s: onCheck requires check", where(id))
		}
	}
	switch j.Kind {
	case KindInput, KindTextarea, KindToggle, KindSelect, KindTabs, KindBreadcrumb, KindCheckbox, KindDialog, KindDag, KindRadio, KindSlider, KindDiff, KindAlert, KindChart, KindCanvas, KindTimeline:
		if a.tap != nil {
			return nil, fmt.Errorf("%s: a %s takes onChange, not onTap", where(id), j.Kind)
		}
		if j.Drag != "" || a.drop != nil {
			return nil, fmt.Errorf("%s: a %s does not take drag or onDrop", where(id), j.Kind)
		}
	case KindGraph:
		if a.tap != nil {
			return nil, fmt.Errorf("%s: a %s takes onChange, not onTap", where(id), j.Kind)
		}
		if j.Drag != "" {
			return nil, fmt.Errorf("%s: a %s does not take drag", where(id), j.Kind)
		}
	case KindTable:
		if a.submit != nil {
			return nil, fmt.Errorf("%s: a table takes onChange, not onSubmit", where(id))
		}
	case KindCollapsible:
		if a.tap != nil {
			return nil, fmt.Errorf("%s: a collapsible takes onChange, not onTap", where(id))
		}
	case KindTreeItem:
		// tree_item may take onTap (select), onChange (expand/collapse), onCheck (tri-state checkbox), drag, and onDrop.
	case KindMenuItem:
		if a.change != nil || a.submit != nil {
			return nil, fmt.Errorf("%s: a menu_item takes onTap, not onChange", where(id))
		}
		if j.Drag != "" || a.drop != nil {
			return nil, fmt.Errorf("%s: a menu_item does not take drag or onDrop", where(id))
		}
	case KindMarkdown:
		if a.tap != nil || a.change != nil || a.submit != nil || a.drop != nil {
			return nil, fmt.Errorf("%s: a markdown takes onLink, not onTap/onChange/onSubmit/onDrop", where(id))
		}
		if j.Drag != "" {
			return nil, fmt.Errorf("%s: a markdown does not take drag", where(id))
		}
		a.change = linkMsg
	case KindDivider, KindProgress, KindCode, KindMenu, KindKbd, KindSkeleton, KindSpacer:
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
	if j.Kind == KindRadio {
		if len(j.Options) == 0 {
			return nil, fmt.Errorf("%s: radio requires at least one option", where(id))
		}
		if j.Value != "" && !slices.Contains(j.Options, j.Value) {
			return nil, fmt.Errorf("%s: radio value %q is not one of its options", where(id), j.Value)
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
	n.Tap, n.Change, n.Drop, n.Checks, n.DoubleTap = a.tap != nil, a.change != nil || a.submit != nil, a.drop != nil, a.check != nil, a.doubleTap != nil
	if n.Tap || n.Change || n.Drop || n.Checks || n.DoubleTap {
		r.t.actions[id] = a
	}
	container := j.Kind == KindColumn || j.Kind == KindRow || j.Kind == KindStack || j.Kind == KindCard ||
		j.Kind == KindScroll || j.Kind == KindWrap || j.Kind == KindGrid || j.Kind == KindSplit ||
		j.Kind == KindCollapsible || j.Kind == KindTree || j.Kind == KindTreeItem || j.Kind == KindList ||
		j.Kind == KindTable || j.Kind == KindMenu || j.Kind == KindDialog || j.Kind == KindAlert || j.Kind == KindEmpty
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
	if j.Kind == KindMenu {
		for i, c := range j.Children {
			if c != nil && c.Kind != KindMenuItem && c.Kind != KindDivider && c.Kind != KindMenu {
				return nil, fmt.Errorf("%s: menu child %d must be a menu_item, divider or menu, got %q", where(id), i, c.Kind)
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
		if c.Kind == KindMenuItem && j.Kind != KindMenu {
			return nil, fmt.Errorf("%s: menu_item child %d must be inside a menu", where(id), i)
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
		child, err := r.node(c, id+"/"+k, depth+1, menuDepth, ctxMenuKey)
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
	heatmap := j.Chart == "heatmap"
	if radial {
		if len(j.Series) != 1 {
			return fmt.Errorf("%s: a %s chart has 1 series", where(id), j.Chart)
		}
	} else if heatmap {
		if len(j.Series) == 0 || len(j.Series) > maxPieSlices {
			return fmt.Errorf("%s: a heatmap chart has 1 to %d series", where(id), maxPieSlices)
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
		if !slices.Contains(GraphNodeShapes, gn.Shape) {
			return fmt.Errorf("%s: graph node %q: shape %q is not card or circle", where(id), gn.ID, gn.Shape)
		}
		if gn.Icon != "" && (len(gn.Icon) > 48 || !iconName.MatchString(gn.Icon)) {
			return fmt.Errorf("%s: graph node %q: icon %q is not an icon's name", where(id), gn.ID, gn.Icon)
		}
		if !slices.Contains(GraphStatuses, gn.Status) {
			return fmt.Errorf("%s: graph node %q: invalid status %q", where(id), gn.ID, gn.Status)
		}
		var nodeBadges []*protocol.UiBadge
		if len(gn.Badges) > 0 {
			var err error
			nodeBadges, err = r.badges(gn.Badges, maxGraphBadges, fmt.Sprintf("%s graph node %q", where(id), gn.ID))
			if err != nil {
				return err
			}
		}
		var x, y float64
		hasPos := false
		if gn.X != nil {
			if !finite(*gn.X, -maxGraphCoord, maxGraphCoord) {
				return fmt.Errorf("%s: graph node %q: invalid x", where(id), gn.ID)
			}
			x = *gn.X
			hasPos = true
		}
		if gn.Y != nil {
			if !finite(*gn.Y, -maxGraphCoord, maxGraphCoord) {
				return fmt.Errorf("%s: graph node %q: invalid y", where(id), gn.ID)
			}
			y = *gn.Y
			hasPos = true
		}
		if gn.HasPos != nil {
			if *gn.HasPos && gn.X == nil && gn.Y == nil {
				return fmt.Errorf("%s: graph node %q: hasPos requires x or y", where(id), gn.ID)
			}
			hasPos = *gn.HasPos
		}
		if gn.ContextMenu != "" {
			if len(gn.ContextMenu) > maxKey || strings.ContainsAny(gn.ContextMenu, "/#") || strings.ContainsFunc(gn.ContextMenu, Spoofing) {
				return fmt.Errorf("%s: graph node %q: invalid contextMenu %q", where(id), gn.ID, gn.ContextMenu)
			}
			r.text += len(gn.ContextMenu)
			r.contextRefs = append(r.contextRefs, contextRef{where: fmt.Sprintf("%s graph node %q", where(id), gn.ID), menu: gn.ContextMenu})
		}
		if gn.ContextArg != "" {
			if gn.ContextMenu == "" && j.ContextMenu == "" {
				return fmt.Errorf("%s: graph node %q: contextArg requires contextMenu", where(id), gn.ID)
			}
			if utf8.RuneCountInString(gn.ContextArg) > maxContextArg || strings.ContainsFunc(gn.ContextArg, Spoofing) {
				return fmt.Errorf("%s: graph node %q: contextArg is over %d characters or holds control or reordering characters", where(id), gn.ID, maxContextArg)
			}
			r.text += len(gn.ContextArg)
		}
		if gn.ContextMenu != "" {
			args := r.t.contextArgs[gn.ContextMenu]
			if args == nil {
				args = map[string]bool{}
				r.t.contextArgs[gn.ContextMenu] = args
			}
			args[cmp.Or(gn.ContextArg, gn.ID)] = true
		}
		r.text += len(gn.ID) + len(gn.Label) + len(gn.Group) + len(gn.Subtitle) + len(gn.Shape) + len(gn.Icon) + len(gn.Status)
		if r.text > maxTreeText {
			return fmt.Errorf("the tree holds more than %d KiB of text", maxTreeText>>10)
		}
		n.GraphNodes = append(n.GraphNodes, &protocol.UiGraphNode{
			Id: gn.ID, Label: gn.Label, Group: gn.Group, Tone: gn.Tone, Subtitle: gn.Subtitle, Weight: w,
			Shape: gn.Shape, Icon: gn.Icon, Status: gn.Status, Badges: nodeBadges,
			X: x, Y: y, HasPos: hasPos, Pinned: gn.Pinned,
			ContextMenu: gn.ContextMenu, ContextArg: gn.ContextArg,
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
		if ge.Kind != "" && (len(ge.Kind) > maxGraphEdgeKind || !graphEdgeKindName.MatchString(ge.Kind)) {
			return fmt.Errorf("%s: graph edge %d has invalid kind %q", where(id), i, ge.Kind)
		}
		r.text += len(ge.From) + len(ge.To) + len(ge.Label) + len(ge.Kind)
		n.GraphEdges = append(n.GraphEdges, &protocol.UiGraphEdge{
			From: ge.From, To: ge.To, Label: ge.Label, Tone: ge.Tone, Dashed: ge.Dashed,
			Active: ge.Active, Kind: ge.Kind,
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

// badges validates up to limit UiBadges, counting their text toward r.text.
func (r *treeReader) badges(in []*jsBadge, limit int, loc string) ([]*protocol.UiBadge, error) {
	if len(in) > limit {
		return nil, fmt.Errorf("%s: at most %d badges", loc, limit)
	}
	out := make([]*protocol.UiBadge, 0, len(in))
	for i, b := range in {
		if b == nil {
			return nil, fmt.Errorf("%s: badge %d is null", loc, i)
		}
		if b.Text == "" && b.Icon == "" {
			return nil, fmt.Errorf("%s: badge %d requires text or icon", loc, i)
		}
		if utf8.RuneCountInString(b.Text) > maxBadgeText || strings.ContainsFunc(b.Text, Spoofing) {
			return nil, fmt.Errorf("%s: badge %d text is over %d runes or holds control or reordering characters", loc, i, maxBadgeText)
		}
		if !slices.Contains(Tones, b.Tone) {
			return nil, fmt.Errorf("%s: badge %d: tone %q is not a colour token", loc, i, b.Tone)
		}
		if !slices.Contains(BadgeVariants, b.Variant) {
			return nil, fmt.Errorf("%s: badge %d: variant %q is not soft, solid, outline or mono", loc, i, b.Variant)
		}
		if b.Icon != "" && (len(b.Icon) > 48 || !iconName.MatchString(b.Icon)) {
			return nil, fmt.Errorf("%s: badge %d: icon %q is not an icon's name", loc, i, b.Icon)
		}
		r.text += len(b.Text) + len(b.Icon)
		if r.text > maxTreeText {
			return nil, fmt.Errorf("the tree holds more than %d KiB of text", maxTreeText>>10)
		}
		out = append(out, &protocol.UiBadge{
			Text: b.Text, Tone: b.Tone, Variant: b.Variant, Icon: b.Icon,
		})
	}
	return out, nil
}

// dag validates a dag node's rows into n, and refuses rows on any other node.
func (r *treeReader) dag(j *jsNode, n *protocol.UiNode, id string) error {
	if j.Kind != KindDag {
		if len(j.Rows) > 0 {
			return fmt.Errorf("%s: only a dag has rows", where(id))
		}
		return nil
	}
	if len(j.Rows) > maxDagRows {
		return fmt.Errorf("%s: a dag has at most %d rows", where(id), maxDagRows)
	}
	rowIDs := make(map[string]bool, len(j.Rows))
	for i, row := range j.Rows {
		if row == nil {
			return fmt.Errorf("%s: dag row %d is null", where(id), i)
		}
		if row.ID == "" || utf8.RuneCountInString(row.ID) > maxDagID || len(row.ID) > maxDagID*4 || strings.ContainsFunc(row.ID, Spoofing) {
			return fmt.Errorf("%s: dag row %d has invalid id %q", where(id), i, row.ID)
		}
		if rowIDs[row.ID] {
			return fmt.Errorf("%s: dag row id %q is duplicated", where(id), row.ID)
		}
		rowIDs[row.ID] = true
		if len(row.Parents) > maxDagParents {
			return fmt.Errorf("%s: dag row %q has more than %d parents", where(id), row.ID, maxDagParents)
		}
		for pi, p := range row.Parents {
			if p == "" || utf8.RuneCountInString(p) > maxDagID || len(p) > maxDagID*4 || strings.ContainsFunc(p, Spoofing) {
				return fmt.Errorf("%s: dag row %q parent %d has invalid id %q", where(id), row.ID, pi, p)
			}
			r.text += len(p)
		}
		if utf8.RuneCountInString(row.Text) > maxDagText || strings.ContainsFunc(row.Text, Spoofing) {
			return fmt.Errorf("%s: dag row %q text is over %d characters or holds control or reordering characters", where(id), row.ID, maxDagText)
		}
		if utf8.RuneCountInString(row.Detail) > maxDagDetail || strings.ContainsFunc(row.Detail, Spoofing) {
			return fmt.Errorf("%s: dag row %q detail is over %d characters or holds control or reordering characters", where(id), row.ID, maxDagDetail)
		}
		if utf8.RuneCountInString(row.Meta) > maxDagMeta || strings.ContainsFunc(row.Meta, Spoofing) {
			return fmt.Errorf("%s: dag row %q meta is over %d characters or holds control or reordering characters", where(id), row.ID, maxDagMeta)
		}
		if !slices.Contains(Tones, row.Tone) {
			return fmt.Errorf("%s: dag row %q: tone %q is not a colour token", where(id), row.ID, row.Tone)
		}
		if row.Icon != "" && (len(row.Icon) > 48 || !iconName.MatchString(row.Icon)) {
			return fmt.Errorf("%s: dag row %q: icon %q is not an icon's name", where(id), row.ID, row.Icon)
		}
		var rowBadges []*protocol.UiBadge
		if len(row.Badges) > 0 {
			var err error
			rowBadges, err = r.badges(row.Badges, maxDagRowBadges, fmt.Sprintf("%s dag row %q", where(id), row.ID))
			if err != nil {
				return err
			}
		}
		r.text += len(row.ID) + len(row.Text) + len(row.Detail) + len(row.Meta) + len(row.Icon)
		if r.text > maxTreeText {
			return fmt.Errorf("the tree holds more than %d KiB of text", maxTreeText>>10)
		}
		n.Rows = append(n.Rows, &protocol.UiRow{
			Id: row.ID, Parents: row.Parents, Text: row.Text, Detail: row.Detail, Meta: row.Meta,
			Badges: rowBadges, Tone: row.Tone, Strong: row.Strong, Icon: row.Icon,
		})
	}
	return nil
}

// slider validates a slider node's min, max, step, and numeric value.
func (r *treeReader) slider(j *jsNode, n *protocol.UiNode, id string) error {
	if j.Kind != KindSlider {
		if j.Min != nil || j.Max != nil || j.Step != nil {
			return fmt.Errorf("%s: only a slider has min, max or step", where(id))
		}
		return nil
	}
	lo, hi := 0.0, 100.0
	if j.Min != nil {
		lo = *j.Min
	}
	if j.Max != nil {
		hi = *j.Max
	}
	if !finite(lo, -maxSliderBound, maxSliderBound) || !finite(hi, -maxSliderBound, maxSliderBound) || lo >= hi {
		return fmt.Errorf("%s: slider requires finite min < max", where(id))
	}
	step := 0.0
	if j.Step != nil {
		step = *j.Step
		if !finite(step, 0, hi-lo) {
			return fmt.Errorf("%s: slider step must be from 0 to %g", where(id), hi-lo)
		}
	}
	if j.Value != "" {
		v, err := strconv.ParseFloat(j.Value, 64)
		if err != nil || !finite(v, lo-1e-9, hi+1e-9) {
			return fmt.Errorf("%s: slider value %q must be a number from %g to %g", where(id), j.Value, lo, hi)
		}
	}
	n.Min, n.Max, n.Step = lo, hi, step
	return nil
}

// diff validates a diff node's lines and header labels.
func (r *treeReader) diff(j *jsNode, n *protocol.UiNode, id string) error {
	if j.Kind != KindDiff {
		if len(j.DiffLines) > 0 || j.DiffFrom != "" || j.DiffTo != "" {
			return fmt.Errorf("%s: only a diff has diffLines, diffFrom or diffTo", where(id))
		}
		return nil
	}
	if len(j.DiffLines) > maxDiffLines {
		return fmt.Errorf("%s: a diff has at most %d lines", where(id), maxDiffLines)
	}
	if utf8.RuneCountInString(j.DiffFrom) > maxDiffLabel || strings.ContainsFunc(j.DiffFrom, Spoofing) {
		return fmt.Errorf("%s: diffFrom is over %d characters or holds control or reordering characters", where(id), maxDiffLabel)
	}
	if utf8.RuneCountInString(j.DiffTo) > maxDiffLabel || strings.ContainsFunc(j.DiffTo, Spoofing) {
		return fmt.Errorf("%s: diffTo is over %d characters or holds control or reordering characters", where(id), maxDiffLabel)
	}
	r.text += len(j.DiffFrom) + len(j.DiffTo)
	for i, l := range j.DiffLines {
		if l == nil {
			return fmt.Errorf("%s: diff line %d is null", where(id), i)
		}
		if l.Op != "+" && l.Op != "-" && l.Op != " " && l.Op != "@" {
			return fmt.Errorf("%s: diff line %d op %q is not +, -, space or @", where(id), i, l.Op)
		}
		if len(l.Text) > maxDiffLineText || strings.ContainsFunc(l.Text, spoofingInline) {
			return fmt.Errorf("%s: diff line %d text is over %d bytes or holds control or reordering characters", where(id), i, maxDiffLineText)
		}
		if l.OldLine < 0 || l.NewLine < 0 {
			return fmt.Errorf("%s: diff line %d line numbers must be >= 0", where(id), i)
		}
		r.text += len(l.Text)
		if r.text > maxTreeText {
			return fmt.Errorf("the tree holds more than %d KiB of text", maxTreeText>>10)
		}
		n.DiffLines = append(n.DiffLines, &protocol.UiDiffLine{
			Op: l.Op, Text: l.Text, OldLine: l.OldLine, NewLine: l.NewLine,
		})
	}
	if r.text > maxTreeText {
		return fmt.Errorf("the tree holds more than %d KiB of text", maxTreeText>>10)
	}
	n.DiffFrom, n.DiffTo = j.DiffFrom, j.DiffTo
	return nil
}

// canvas validates a sandboxed 2D vector canvas node and its drawing commands.
func (r *treeReader) canvas(j *jsNode, n *protocol.UiNode, id string) error {
	if j.Kind != KindCanvas {
		if j.Width != nil || j.Height != nil || j.Pan != nil || j.Zoom != nil || j.CanvasCommands != nil {
			return fmt.Errorf("%s: only a canvas has width, height, pan, zoom or canvasCommands", where(id))
		}
		return nil
	}
	if j.Width != nil {
		if !finite(*j.Width, 0, maxCanvasSize) {
			return fmt.Errorf("%s: canvas width must be from 0 to %d", where(id), maxCanvasSize)
		}
		n.CanvasWidth = *j.Width
	}
	if j.Height != nil {
		if !finite(*j.Height, 0, maxCanvasSize) {
			return fmt.Errorf("%s: canvas height must be from 0 to %d", where(id), maxCanvasSize)
		}
		n.CanvasHeight = *j.Height
	}
	pan := true
	if j.Pan != nil {
		pan = *j.Pan
	}
	zoom := true
	if j.Zoom != nil {
		zoom = *j.Zoom
	}
	n.CanvasPan = pan
	n.CanvasZoom = zoom
	if len(j.CanvasCommands) > maxCanvasCommands {
		return fmt.Errorf("%s: a canvas has at most %d commands", where(id), maxCanvasCommands)
	}
	for i, cmd := range j.CanvasCommands {
		if cmd == nil {
			return fmt.Errorf("%s: canvas command %d is null", where(id), i)
		}
		if !slices.Contains(CanvasOps, cmd.Op) {
			return fmt.Errorf("%s: canvas command %d op %q is not one of %s", where(id), i, cmd.Op, strings.Join(CanvasOps, ", "))
		}
		if !slices.Contains(CanvasTones, cmd.Tone) {
			return fmt.Errorf("%s: canvas command %d tone %q is not a colour token", where(id), i, cmd.Tone)
		}
		if !slices.Contains(CanvasTones, cmd.FillTone) {
			return fmt.Errorf("%s: canvas command %d fillTone %q is not a colour token", where(id), i, cmd.FillTone)
		}
		if !slices.Contains(CanvasTones, cmd.StrokeTone) {
			return fmt.Errorf("%s: canvas command %d strokeTone %q is not a colour token", where(id), i, cmd.StrokeTone)
		}
		for _, coord := range []float64{cmd.X, cmd.Y, cmd.X1, cmd.Y1, cmd.X2, cmd.Y2, cmd.Cx1, cmd.Cy1, cmd.Cx2, cmd.Cy2} {
			if !finite(coord, -maxCanvasCoord, maxCanvasCoord) {
				return fmt.Errorf("%s: canvas command %d coordinate out of range", where(id), i)
			}
		}
		for _, dim := range []float64{cmd.W, cmd.H, cmd.R} {
			if !finite(dim, 0, maxCanvasSize) {
				return fmt.Errorf("%s: canvas command %d dimension must be from 0 to %d", where(id), i, maxCanvasSize)
			}
		}
		if !finite(cmd.StrokeWidth, 0, maxCanvasStroke) {
			return fmt.Errorf("%s: canvas command %d strokeWidth must be from 0 to %d", where(id), i, maxCanvasStroke)
		}
		alpha := 1.0
		if cmd.Alpha != nil {
			if !finite(*cmd.Alpha, 0, 1) {
				return fmt.Errorf("%s: canvas command %d alpha must be from 0 to 1", where(id), i)
			}
			alpha = *cmd.Alpha
		}
		size, err := parseCanvasSize(cmd.Size)
		if err != nil {
			return fmt.Errorf("%s: canvas command %d: %w", where(id), i, err)
		}
		if cmd.Op == "icon" && cmd.Icon == "" {
			return fmt.Errorf("%s: canvas command %d icon op requires icon", where(id), i)
		}
		if cmd.Icon != "" && (len(cmd.Icon) > 48 || !iconName.MatchString(cmd.Icon)) {
			return fmt.Errorf("%s: canvas command %d icon %q is not an icon's name", where(id), i, cmd.Icon)
		}
		if len(cmd.Text) > maxCanvasText || strings.ContainsFunc(cmd.Text, spoofingInline) {
			return fmt.Errorf("%s: canvas command %d text is over %d bytes or holds control or reordering characters", where(id), i, maxCanvasText)
		}
		if cmd.HitID != "" && (len(cmd.HitID) > maxCanvasHitID || strings.ContainsFunc(cmd.HitID, Spoofing)) {
			return fmt.Errorf("%s: canvas command %d invalid hitId %q", where(id), i, cmd.HitID)
		}
		r.text += len(cmd.Text) + len(cmd.Icon) + len(cmd.HitID)
		if r.text > maxTreeText {
			return fmt.Errorf("the tree holds more than %d KiB of text", maxTreeText>>10)
		}
		n.CanvasCommands = append(n.CanvasCommands, &protocol.UiCanvasCommand{
			Op:          cmd.Op,
			X:           cmd.X,
			Y:           cmd.Y,
			W:           cmd.W,
			H:           cmd.H,
			R:           cmd.R,
			X1:          cmd.X1,
			Y1:          cmd.Y1,
			X2:          cmd.X2,
			Y2:          cmd.Y2,
			Cx1:         cmd.Cx1,
			Cy1:         cmd.Cy1,
			Cx2:         cmd.Cx2,
			Cy2:         cmd.Cy2,
			StrokeWidth: cmd.StrokeWidth,
			Alpha:       proto.Float64(alpha),
			Text:        cmd.Text,
			Icon:        cmd.Icon,
			Size:        size,
			Tone:        cmd.Tone,
			FillTone:    cmd.FillTone,
			StrokeTone:  cmd.StrokeTone,
			HitId:       cmd.HitID,
		})
	}
	return nil
}

func parseCanvasSize(raw json.RawMessage) (float64, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		switch s {
		case "":
			return 0, nil
		case "small":
			return 11.5, nil
		case "medium":
			return 13, nil
		case "large":
			return 15, nil
		default:
			return 0, fmt.Errorf("size %q is not small, medium or large", s)
		}
	}
	var v float64
	if err := json.Unmarshal(raw, &v); err != nil || !finite(v, 0, maxCanvasFontSize) {
		return 0, fmt.Errorf("size must be small, medium, large, or a number from 0 to %d", maxCanvasFontSize)
	}
	return v, nil
}

// timeline validates a timeline node's lanes and spans.
func (r *treeReader) timeline(j *jsNode, n *protocol.UiNode, id string) error {
	if j.Kind != KindTimeline {
		if j.TimelineLanes != nil {
			return fmt.Errorf("%s: only a timeline has timelineLanes", where(id))
		}
		return nil
	}
	if len(j.TimelineLanes) > maxTimelineLanes {
		return fmt.Errorf("%s: a timeline has at most %d lanes", where(id), maxTimelineLanes)
	}
	laneIDs := make(map[string]bool, len(j.TimelineLanes))
	spanIDs := map[string]bool{}
	totalSpans := 0
	for i, lane := range j.TimelineLanes {
		if lane == nil {
			return fmt.Errorf("%s: timeline lane %d is null", where(id), i)
		}
		if lane.ID == "" || utf8.RuneCountInString(lane.ID) > maxTimelineID || len(lane.ID) > maxTimelineID*4 || strings.ContainsFunc(lane.ID, Spoofing) {
			return fmt.Errorf("%s: timeline lane %d has invalid id %q", where(id), i, lane.ID)
		}
		if laneIDs[lane.ID] {
			return fmt.Errorf("%s: timeline lane id %q is duplicated", where(id), lane.ID)
		}
		laneIDs[lane.ID] = true
		if len(lane.Label) > maxTimelineLabel || strings.ContainsFunc(lane.Label, Spoofing) {
			return fmt.Errorf("%s: timeline lane %q has invalid label", where(id), lane.ID)
		}
		r.text += len(lane.ID) + len(lane.Label)
		outLane := &protocol.UiTimelineLane{
			Id:    lane.ID,
			Label: lane.Label,
		}
		for si, sp := range lane.Spans {
			totalSpans++
			if totalSpans > maxTimelineSpans {
				return fmt.Errorf("%s: a timeline has at most %d spans", where(id), maxTimelineSpans)
			}
			if sp == nil {
				return fmt.Errorf("%s: timeline lane %q span %d is null", where(id), lane.ID, si)
			}
			if sp.ID == "" || utf8.RuneCountInString(sp.ID) > maxTimelineID || len(sp.ID) > maxTimelineID*4 || strings.ContainsFunc(sp.ID, Spoofing) {
				return fmt.Errorf("%s: timeline span %d has invalid id %q", where(id), si, sp.ID)
			}
			if spanIDs[sp.ID] {
				return fmt.Errorf("%s: timeline span id %q is duplicated", where(id), sp.ID)
			}
			spanIDs[sp.ID] = true
			if sp.StartMs == nil || sp.EndMs == nil || !finite(*sp.StartMs, -maxTimelineMs, maxTimelineMs) || !finite(*sp.EndMs, -maxTimelineMs, maxTimelineMs) {
				return fmt.Errorf("%s: timeline span %q requires finite startMs and endMs", where(id), sp.ID)
			}
			if *sp.EndMs < *sp.StartMs {
				return fmt.Errorf("%s: timeline span %q has endMs < startMs", where(id), sp.ID)
			}
			if len(sp.Label) > maxTimelineLabel || strings.ContainsFunc(sp.Label, Spoofing) {
				return fmt.Errorf("%s: timeline span %q has invalid label", where(id), sp.ID)
			}
			if len(sp.Subtitle) > maxTimelineSubtitle || strings.ContainsFunc(sp.Subtitle, Spoofing) {
				return fmt.Errorf("%s: timeline span %q has invalid subtitle", where(id), sp.ID)
			}
			if !slices.Contains(Tones, sp.Tone) {
				return fmt.Errorf("%s: timeline span %q: tone %q is not a colour token", where(id), sp.ID, sp.Tone)
			}
			if sp.Kind != "" && (len(sp.Kind) > maxTimelineKind || !graphEdgeKindName.MatchString(sp.Kind)) {
				return fmt.Errorf("%s: timeline span %q has invalid kind %q", where(id), sp.ID, sp.Kind)
			}
			r.text += len(sp.ID) + len(sp.Label) + len(sp.Subtitle) + len(sp.Kind)
			if r.text > maxTreeText {
				return fmt.Errorf("the tree holds more than %d KiB of text", maxTreeText>>10)
			}
			outLane.Spans = append(outLane.Spans, &protocol.UiTimelineSpan{
				Id:       sp.ID,
				StartMs:  *sp.StartMs,
				EndMs:    *sp.EndMs,
				Label:    sp.Label,
				Subtitle: sp.Subtitle,
				Tone:     sp.Tone,
				Kind:     sp.Kind,
			})
		}
		if r.text > maxTreeText {
			return fmt.Errorf("the tree holds more than %d KiB of text", maxTreeText>>10)
		}
		n.TimelineLanes = append(n.TimelineLanes, outLane)
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
