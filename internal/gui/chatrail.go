package gui

import (
	"image/color"
	"math"
	"strings"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/mako/internal/gui/skin"
)

// The turn rail: a tick down the chat's left edge for each prompt the user
// sent, the one the reader is at drawn long and bright. The ticks swell
// under the pointer, as a dock's icons do; one hovered shows the prompt
// and how the agent's reply to it ends, and a click goes to the prompt.

// railTurn is a prompt as the rail shows it.
type railTurn struct {
	Key    string // the turn's, in the feed
	Index  int    // where the turn is in the feed
	Prompt string
	Reply  string // the last of the agent's words before the next prompt
}

// railTurns are the prompts among turns, each with the reply it had.
func railTurns(turns []chatTurnItem) []railTurn {
	var out []railTurn
	for i, t := range turns {
		if len(t.Messages) == 0 || t.Thinking {
			continue
		}
		msg := t.Messages[0]
		switch msg.GetRole() {
		case "user":
			if prompt := strings.TrimSpace(msg.GetText()); prompt != "" {
				out = append(out, railTurn{Key: t.Key, Index: i, Prompt: prompt})
			}
		case "tool":
		default:
			if n := len(out); n > 0 && msg.GetTool() == "" && strings.TrimSpace(msg.GetText()) != "" {
				out[n-1].Reply = msg.GetText()
			}
		}
	}
	return out
}

// The rail's measures: how wide it is, the most and least room between
// ticks, how long a tick is at rest, swollen and current, and how far from
// the pointer a tick swells.
const (
	railWidth   = 40
	railInset   = 12 // from the edge to where a tick starts
	railMaxGap  = 20
	railMinGap  = 3
	railRest    = 10
	railSwell   = 12
	railCurrent = 26
	railReach   = 60
	railCard    = 340
)

// turnRail lays its ticks down the feed's left edge, centred in its
// height, and works out at each paint which prompt the reader is at.
type turnRail struct {
	find    *chatFinder
	turns   ggui.Readable[[]chatTurnItem]
	prompts ggui.Readable[[]railTurn]
	child   ggui.Widget

	gap     float64 // the room each tick takes, as the height allows
	size    ggui.Size
	current string // the key of the prompt the reader is at
}

func newTurnRail(m *model, find *chatFinder, turns ggui.Readable[[]chatTurnItem]) *turnRail {
	rail := &turnRail{find: find, turns: turns}
	rail.prompts = ggui.Derived(func() []railTurn { return railTurns(turns.Get()) }).WithEqual(deepEqual)
	rail.child = ggui.Reactive(func() ggui.Widget {
		prompts := rail.prompts.Get()
		if len(prompts) < 2 {
			return ggui.Box()
		}
		ticks := make([]ggui.Widget, len(prompts))
		for i, p := range prompts {
			ticks[i] = railItem(rail, p)
		}
		return ggui.Column(ticks...)
	})
	return rail
}

func (r *turnRail) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	h := c.MaxH
	if math.IsInf(h, 1) {
		h = 0
	}
	if n := len(ggui.Peek(r.prompts)); n > 0 {
		r.gap = max(min(railMaxGap, (h-2*railMaxGap)/float64(n)), railMinGap)
	}
	r.size = r.child.Layout(ggui.Constraints{MaxW: railWidth, MaxH: ggui.Unbounded}, env)
	return c.Constrain(ggui.Sz(railWidth, h))
}

func (r *turnRail) Paint(dst *ggui.Canvas, at ggui.Rect) {
	r.current = r.reading(at)
	y := at.Origin.Y + max((at.Size.H-r.size.H)/2, 0)
	dst.Paint(r.child, ggui.Rct(ggui.Pt(at.Origin.X, y), r.size))
}

// reading is the key of the prompt the reader is at, as the feed's latest
// paint, which comes before the rail's, has it: the last one at or before
// the turn at the top of the view, where going to a prompt puts it, or at
// the latest, the last one in view.
func (r *turnRail) reading(view ggui.Rect) string {
	prompts := ggui.Peek(r.prompts)
	if len(prompts) == 0 {
		return ""
	}
	turns := ggui.Peek(r.turns)
	line := view.Origin.Y + chatGap
	atEnd := r.find.scroll.MaxOffset()-r.find.scroll.Offset() < 1
	first := -1
	for i, t := range turns {
		mk, ok := r.find.marks[t.Key]
		if !ok || mk.serial != r.find.serial {
			continue
		}
		if atEnd {
			first = i // the last in view
		} else if mk.at.Origin.Y+mk.at.Size.H > line {
			first = i
			break
		}
	}
	if first < 0 {
		return r.current // nothing of the feed painted: as it was
	}
	key := prompts[0].Key
	for _, p := range prompts {
		if p.Index > first {
			break
		}
		key = p.Key
	}
	return key
}

// railItem is a prompt's tick, with its card on hover and a click to go
// to it.
func railItem(rail *turnRail, p railTurn) ggui.Widget {
	tick := ggui.Tap(&railTick{rail: rail, key: p.Key}, func() { rail.find.jumpTo(p.Key) }).
		Cursor(ggui.CursorShapePointer)
	return ui.HoverCard(tick, railCardOf(p)).
		Side(ggui.PopupRight).Align(ggui.PopupCenter).AlignOffset(0).
		Delay(0).CloseDelay(0).Width(railCard).Name(p.Prompt)
}

// railCardOf is what a hovered tick shows: the prompt, and how the reply
// to it ends.
func railCardOf(p railTurn) ggui.Widget {
	rows := []ggui.Widget{
		ggui.Text(firstLine(p.Prompt)).Size(14).Weight(ggui.WeightMedium).NoWrap().Ellipsis(),
	}
	if reply := plainPreview(p.Reply); reply != "" {
		rows = append(rows, muted(ggui.Text(reply).Size(13)).MaxLines(3))
	}
	return ggui.Column(rows...).Gap(6).Stretch()
}

// plainPreview is the start of Markdown as plain words on one line: its
// marks taken out and its lines run together.
func plainPreview(md string) string {
	const most = 400
	var b strings.Builder
	fenced := false
	for line := range strings.Lines(md) {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "```") {
			fenced = !fenced
			continue
		}
		line = strings.TrimLeft(line, "#>-*+ ")
		if fenced || line == "" || strings.HasPrefix(line, "|") {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(line)
		if b.Len() > most {
			break
		}
	}
	s := strings.NewReplacer("**", "", "__", "", "`", "").Replace(b.String())
	if len(s) > most {
		s = strings.ToValidUTF8(s[:most], "") + "…"
	}
	return s
}

// railTick is a tick of the rail: a short line from the left, long for
// the prompt the reader is at, and swollen by the pointer's nearness.
type railTick struct {
	rail *turnRail
	key  string
}

func (t *railTick) Layout(c ggui.Constraints, _ ggui.Env) ggui.Size {
	return c.Constrain(ggui.Sz(railWidth, t.rail.gap))
}

func (t *railTick) Paint(dst *ggui.Canvas, r ggui.Rect) {
	mid := r.Origin.Y + r.Size.H/2
	length, ink := float64(railRest), color.Color(skin.ExtraMuted)
	near := 0.0
	if p, ok := dst.Pointer(); ok && p.X >= r.Origin.X-4 && p.X <= r.Origin.X+railWidth {
		near = max(0, 1-math.Abs(p.Y-mid)/railReach)
	}
	switch {
	case t.key == t.rail.current:
		length, ink = railCurrent, skin.Fg
	case near > 0:
		length += railSwell * near * near
		if near > 0.8 {
			ink = skin.Muted
		}
	}
	thick := min(2, max(t.rail.gap-1, 1))
	x := r.Origin.X + railInset
	dst.FillRect(ggui.Rct(ggui.Pt(x, mid-thick/2), ggui.Sz(length, thick)), ink)
}
