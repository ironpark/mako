package gui

import (
	"encoding/json"
	"slices"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// An agent that keeps a list of what it means to do, as Claude's TodoWrite
// and Codex's update_plan, rewrites it as it goes, each time a tool call
// that scrolls away with the rest. The latest stays over the composer while
// the work goes on: how far it is, the step in hand, and the whole list
// folded under them.

// planSteps are the steps of the latest list among messages, nil for none.
func planSteps(messages []*protocol.ChatMessage) []any {
	for _, msg := range slices.Backward(messages) {
		if k := msg.GetKind(); (k != protocol.ToolTodo && k != protocol.ToolPlan) || msg.GetOutput() {
			continue
		}
		var in map[string]any
		if json.Unmarshal([]byte(msg.GetText()), &in) != nil {
			return nil
		}
		return agent.List(in, "items")
	}
	return nil
}

// planStanding is how far steps are: those done, and the step in hand, or
// the next where none is.
func planStanding(steps []any) (done int, current string) {
	next := ""
	for _, it := range steps {
		step, _ := it.(map[string]any)
		switch text := agent.Str(step, "text"); agent.Str(step, "status") {
		case "completed", "done":
			done++
		case "in_progress":
			if current == "" {
				current = text
			}
		default:
			if next == "" {
				next = text
			}
		}
	}
	if current == "" {
		current = next
	}
	return done, current
}

// planProgress is session id's agent's list over the composer, while it
// has steps left; nothing once all are done, or the session has ended.
func planProgress(m *model, id string, state *chatState) ggui.Widget {
	steps := ggui.Derived(func() []any {
		if s, ok := sessionIn(m.Sessions.Get(), id); !ok || s.GetState() != protocol.StateRunning {
			return nil
		}
		steps := planSteps(state.result.Get().Chat.GetMessages())
		if done, _ := planStanding(steps); done == len(steps) {
			return nil
		}
		return steps
	}).WithEqual(func(a, b []any) bool {
		x, _ := json.Marshal(a)
		y, _ := json.Marshal(b)
		return string(x) == string(y)
	})
	open := ggui.State(false)
	return ggui.View(steps, func(steps []any) ggui.Widget {
		if len(steps) == 0 {
			return ggui.Box()
		}
		done, current := planStanding(steps)
		head := ggui.Tap(ggui.Row(
			icon("list").Size(13).Color(skin.AccentBright),
			ggui.Text(Tf("%d of %d", done, len(steps))).Size(12.5).Weight(ggui.WeightMedium).Color(skin.AccentBright).NoWrap(),
			ggui.Expanded(muted(ggui.Text(current).Size(12).NoWrap().Ellipsis())),
			chevron(open, 14, skin.Muted),
		).Gap(8).Align(ggui.AlignCenter), func() { open.Set(!ggui.Peek(open)) }).Cursor(ggui.CursorShapePointer)
		rows := reveal(open, func() ggui.Widget {
			return ggui.Column(hline(), ggui.Box(checklist(steps)).MaxHeight(240)).Gap(4).Stretch()
		})
		return &lane{child: ggui.Box(ggui.Column(head, rows).Gap(6).Stretch()).
			Fill(skin.Surface1).Border(1, skin.Border).Radius(10).Pad(8, 12)}
	})
}
