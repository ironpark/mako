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
	KindColumn   = "column"
	KindRow      = "row"
	KindStack    = "stack"
	KindText     = "text"
	KindIcon     = "icon"
	KindPill     = "pill"
	KindChip     = "chip"
	KindButton   = "button"
	KindList     = "list"
	KindInput    = "input"
	KindToggle   = "toggle"
	KindSelect   = "select"
	KindMarkdown = "markdown"
	KindCode     = "code"
	KindProgress = "progress"
	KindDivider  = "divider"
	KindChart    = "chart"
)

// Kinds are the vocabulary, in the order docs/plugins.md lists it.
var Kinds = []string{KindColumn, KindRow, KindStack, KindText, KindIcon, KindPill, KindChip, KindButton,
	KindList, KindInput, KindToggle, KindSelect, KindMarkdown, KindCode, KindProgress, KindDivider, KindChart}

// Charts are the kinds of chart a chart node draws.
var Charts = []string{"line", "bar", "sparkline"}

// Tones are the colour tokens a node may take.
var Tones = []string{"", "muted", "faint", "accent", "success", "warning", "danger"}

// The bounds of a tree.
const (
	maxTreeJSON  = 512 << 10
	maxNodes     = 2000
	maxDepth     = 24
	maxNodeText  = 8 << 10
	maxLongText  = 32 << 10 // a markdown's or code's
	maxTreeText  = 256 << 10
	maxKey       = 128
	maxNodeOpts  = 64
	maxMessage   = 4 << 10
	maxGap, maxG = 64, 100
	maxOption    = 320
	// A chart's bounds: a few series of a few hundred points, labels as
	// short as an axis shows them.
	maxSeries      = 4
	maxPoints      = 200
	maxChartPoints = 600
	maxLabel       = 32
	maxSeriesName  = 64
	maxChartValue  = 1e15
)

var (
	iconName    = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	messageName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	languageTag = regexp.MustCompile(`^[a-z0-9+#-]{0,24}$`)
)

// jsNode is a node as the prelude writes it.
type jsNode struct {
	Kind        string          `json:"kind"`
	Key         string          `json:"key"`
	Text        string          `json:"text"`
	Tone        string          `json:"tone"`
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
	OnTap       json.RawMessage `json:"onTap"`
	OnChange    json.RawMessage `json:"onChange"`
	OnSubmit    json.RawMessage `json:"onSubmit"`
	Children    []*jsNode       `json:"children"`
	Chart       string          `json:"chart"`
	Series      []*jsSeries     `json:"series"`
	Labels      []string        `json:"labels"`
}

// jsSeries is a chart's series as the prelude writes it.
type jsSeries struct {
	Name   string     `json:"name"`
	Tone   string     `json:"tone"`
	Values []*float64 `json:"values"`
}

// actions are what a node's events send: each a message, the JSON array
// of its name and arguments.
type actions struct{ tap, change, submit json.RawMessage }

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
		r.text += len(o)
	}
	if r.text > maxTreeText {
		return nil, fmt.Errorf("the tree holds more than %d KiB of text", maxTreeText>>10)
	}
	if j.Kind != KindMarkdown && j.Kind != KindCode && strings.ContainsFunc(j.Text, spoofingInline) {
		return nil, fmt.Errorf("%s: text holds control or reordering characters", where(id))
	}
	n := &protocol.UiNode{
		Id: id, Kind: j.Kind, Text: j.Text, Tone: j.Tone, Icon: j.Icon, Weight: j.Weight, Size: j.Size,
		Value: j.Value, Checked: j.Checked, Options: j.Options, Progress: j.Progress, Language: j.Language,
		Disabled: j.Disabled, Grow: j.Grow, Gap: j.Gap,
	}
	if j.Kind == KindInput && n.Text == "" {
		n.Text = j.Placeholder
	}
	if err := r.chart(j, n, id); err != nil {
		return nil, err
	}
	var a actions
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
	switch j.Kind {
	case KindInput, KindToggle, KindSelect:
		if a.tap != nil {
			return nil, fmt.Errorf("%s: a %s takes onChange, not onTap", where(id), j.Kind)
		}
	case KindDivider, KindProgress, KindMarkdown, KindCode, KindChart:
		if a.tap != nil || a.change != nil || a.submit != nil {
			return nil, fmt.Errorf("%s: a %s sends no messages", where(id), j.Kind)
		}
	default:
		if a.change != nil || a.submit != nil {
			return nil, fmt.Errorf("%s: a %s takes onTap, not onChange", where(id), j.Kind)
		}
	}
	if a.submit != nil && j.Kind != KindInput {
		return nil, fmt.Errorf("%s: only an input takes onSubmit", where(id))
	}
	n.Tap, n.Change = a.tap != nil, a.change != nil || a.submit != nil
	if n.Tap || n.Change {
		r.t.actions[id] = a
	}
	container := j.Kind == KindColumn || j.Kind == KindRow || j.Kind == KindStack || j.Kind == KindList
	if !container && len(j.Children) > 0 {
		return nil, fmt.Errorf("%s: a %s holds no children", where(id), j.Kind)
	}
	keys := map[string]bool{}
	for i, c := range j.Children {
		if c == nil {
			return nil, fmt.Errorf("%s: child %d is null", where(id), i)
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
		if j.Chart != "" || len(j.Series) > 0 || len(j.Labels) > 0 {
			return fmt.Errorf("%s: only a chart has chart, series or labels", where(id))
		}
		return nil
	}
	if !slices.Contains(Charts, j.Chart) {
		return fmt.Errorf("%s: chart %q is not one of %s", where(id), j.Chart, strings.Join(Charts, ", "))
	}
	if len(j.Series) == 0 || len(j.Series) > maxSeries {
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
		if len(sj.Values) == 0 || len(sj.Values) > maxPoints {
			return fmt.Errorf("%s: series %d has 1 to %d values", where(id), i, maxPoints)
		}
		if len(j.Labels) > 0 && len(sj.Values) != len(j.Labels) {
			return fmt.Errorf("%s: series %d has %d values for %d labels", where(id), i, len(sj.Values), len(j.Labels))
		}
		points += len(sj.Values)
		s := &protocol.UiSeries{Name: sj.Name, Tone: sj.Tone, Values: make([]float64, len(sj.Values))}
		for k, v := range sj.Values {
			if v == nil || !finite(*v, -maxChartValue, maxChartValue) {
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
