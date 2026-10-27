package gui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	uitheme "github.com/ironpark/ggui/ui/theme"
	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/transcript"
)

// tokens writes a token count as the chat footer does: 950, 12.3k, 1.2M.
func tokens(n int64) string {
	switch {
	case n < 1000:
		return fmt.Sprint(n)
	case n < 1_000_000:
		return trimZero(fmt.Sprintf("%.1f", float64(n)/1e3)) + "k"
	}
	return trimZero(fmt.Sprintf("%.1f", float64(n)/1e6)) + "M"
}

func trimZero(s string) string {
	if len(s) > 2 && s[len(s)-2:] == ".0" {
		return s[:len(s)-2]
	}
	return s
}

// usageLine is a conversation's tokens in brief: how full its context is,
// and what it has spent.
func usageLine(u *protocol.Usage) string {
	ctx := ""
	if u.GetContext() > 0 {
		ctx = Tf("context %s", tokens(u.GetContext()))
		if u.GetWindow() > 0 {
			ctx = Tf("context %d%%", u.GetContext()*100/u.GetWindow())
		}
		ctx += " · "
	}
	return ctx + Tf("%s in · %s out", tokens(u.GetInput()+u.GetCachedInput()), tokens(u.GetOutput()))
}

// usageDetail is a conversation's tokens in full, and the account's limits
// as they stand at now, with when each starts over.
func usageDetail(u *protocol.Usage, now time.Time) string {
	s := Tf("Input %s (cached %s) · output %s", tokens(u.GetInput()+u.GetCachedInput()), tokens(u.GetCachedInput()), tokens(u.GetOutput()))
	if u.GetContext() > 0 {
		s += "\n" + Tf("Latest prompt %s tokens", tokens(u.GetContext()))
		if u.GetWindow() > 0 {
			s += Tf(" of %s", tokens(u.GetWindow()))
		}
	}
	for _, l := range u.GetLimits() {
		used, resets := limitNow(l, now)
		s += "\n" + Tf("%s limit %d%% used", limitName(l.GetMinutes()), int(used+0.5))
		if resets > 0 {
			s += Tf(", resets in %s (%s)", untilText(resets), time.Unix(l.GetResets(), 0).Format("01-02 15:04"))
		}
	}
	return s
}

// usageTip shows the usage's detail under the pointer over w, written as
// it opens, so its waits are never stale.
func usageTip(m *model, u *protocol.Usage, w ggui.Widget) ggui.Widget {
	return ui.TooltipText(w, func() string { return usageDetail(u, ggui.Peek(m.Minute)) })
}

// usageCornerLabels are the corners the usage overlay can sit in, by value,
// in English for T.
var usageCornerLabels = map[string]string{
	"top-left":     "Top left",
	"top-right":    "Top right",
	"bottom-left":  "Bottom left",
	"bottom-right": "Bottom right",
}

var usageCorners = []string{"top-left", "top-right", "bottom-left", "bottom-right"}

type usageState struct {
	usage   *ggui.StateValue[*protocol.Usage]
	loading bool
}

func (m *model) usageState(id string) *usageState {
	if state := m.usages[id]; state != nil {
		return state
	}
	state := &usageState{usage: ggui.State(&protocol.Usage{}).WithEqual(protocol.Same)}
	m.usages[id] = state
	return state
}

// loadUsage asks for a session's usage alone, without its messages.
func (m *model) loadUsage(id string, state *usageState) {
	if state.loading || m.usages[id] != state {
		return
	}
	info, ok := m.session(id)
	if !ok {
		return
	}
	state.loading = true
	req := &protocol.ReadChat{NodeId: m.remoteID(info.GetNodeId()), SessionId: id, UsageOnly: true}
	fetch(m, 10*time.Second, func(ctx context.Context, c *client.Client) (*protocol.Chat, error) {
		return c.ReadChat(ctx, req)
	}, func(chat *protocol.Chat, err error) {
		if m.usages[id] != state {
			return
		}
		state.loading = false
		if err == nil && chat.GetUsage() != nil {
			state.usage.Set(chat.GetUsage())
		}
	})
}

// usageOverlay floats a session's tokens over its view, in the corner the
// settings choose: how full its context is and how much of the account's
// limits is spent. src is the usage the view already reads, such as Chat's;
// nil has the overlay ask for it alone. A plain shell has no conversation,
// so it has none.
func usageOverlay(m *model, id string, src ggui.Readable[*protocol.Usage]) ggui.Widget {
	// The session may not be listed yet when its view is built.
	shown := ggui.Derived(func() bool {
		if !m.Prefs.Get().UsageOverlay {
			return false
		}
		s, ok := sessionIn(m.Sessions.Get(), id)
		return ok && transcript.Supported(s.GetAgent())
	})
	return ggui.If(shown, func() ggui.Widget {
		if src == nil {
			state := m.usageState(id)
			ggui.Effect(func() ggui.Cleanup {
				m.loadUsage(id, state)
				return m.every(3*time.Second, func() { m.loadUsage(id, state) })
			})
			src = state.usage
		}
		corner := ggui.Map(m.Prefs, func(p prefs) string { return p.UsageCorner })
		return ggui.View(corner, func(c string) ggui.Widget {
			x := pick(strings.HasSuffix(c, "left"), 0.0, 1.0)
			y := pick(strings.HasPrefix(c, "top"), 0.0, 1.0)
			return ggui.Padding(ggui.Align(ggui.View(src, func(u *protocol.Usage) ggui.Widget { return usageCard(m, u) })).At(x, y), 10, 18)
		})
	})
}

// limitName names a limit by its window: 5h, Week, 30d.
func limitName(minutes int64) string {
	switch {
	case minutes == protocol.Week:
		return T("Week")
	case minutes%(24*60) == 0:
		return Tf("%dd", minutes/(24*60))
	case minutes%60 == 0:
		return Tf("%dh", minutes/60)
	}
	return Tf("%dm", minutes)
}

// usageCard is the overlay itself, one short line: how full the context
// is, on a ring when the agent names the window, then each of the
// account's limits, such as Claude's five hours and week, as spent so far.
// An account the agent reports no limits for, such as one on an API key,
// shows the tokens spent instead. When the limits start over, and the
// rest, open under the pointer.
func usageCard(m *model, u *protocol.Usage) ggui.Widget {
	if u.IsZero() {
		return ggui.Box()
	}
	label := func(s string) ggui.Widget { return ggui.Text(s).Size(11).Color(tokExtraMuted).NoWrap() }
	pair := func(lead ggui.Widget, v string, share float64) ggui.Widget {
		return ggui.Row(lead, ggui.Text(v).Size(11.5).Color(shareColor(share, tokFg)).NoWrap()).Gap(4).Align(ggui.AlignCenter)
	}
	var head []ggui.Widget
	switch {
	case u.GetContext() > 0 && u.GetWindow() > 0:
		share := float64(u.GetContext()) / float64(u.GetWindow())
		ring := ui.ProgressRing(ggui.Const(share)).Size(12).Color(shareColor(share, tokFg)).Track(tokSurface3)
		head = append(head, pair(ring, Tf("%d%%", u.GetContext()*100/u.GetWindow()), share))
	case u.GetContext() > 0:
		head = append(head, pair(label(T("Context")), tokens(u.GetContext()), 0))
	}
	if len(head) > 0 {
		head = append(head, ggui.Box(vline()).Height(10))
	}
	card := func(items []ggui.Widget) ggui.Widget {
		return usageTip(m, u, ggui.Reactive(func() ggui.Widget {
			return ggui.Box(ggui.Row(items...).Gap(8).Align(ggui.AlignCenter)).
				Fill(tokSurface1).Border(1, tokBorder).Radius(7).Pad(4, 8).Shadow(uitheme.Use().PanelShadow)
		}))
	}
	if len(u.GetLimits()) == 0 {
		return card(append(head, label(Tf("%s in · %s out", tokens(u.GetInput()+u.GetCachedInput()), tokens(u.GetOutput())))))
	}
	// Only the limits age, as their resets come nearer.
	return ggui.View(m.Minute, func(now time.Time) ggui.Widget {
		items := slices.Clone(head)
		for _, l := range u.GetLimits() {
			used, _ := limitNow(l, now)
			items = append(items, pair(label(limitName(l.GetMinutes())), Tf("%d%%", int(used+0.5)), used/100))
		}
		return card(items)
	})
}

// shareColor is the colour for a share spent: c, then amber from 80% on
// and red from 95%.
func shareColor(share float64, c uitheme.Color) uitheme.Color {
	switch {
	case share >= .95:
		return tokDiffRemoved
	case share >= .8:
		return tokDotWaiting
	}
	return c
}

// limitNow is a limit as it stands at now: what is spent, and how long
// until its window starts over. A window past its reset has started over,
// though the agent has not said so yet.
func limitNow(l *protocol.RateLimit, now time.Time) (used float64, resets time.Duration) {
	if l.GetResets() == 0 {
		return l.GetUsed(), 0
	}
	resets = time.Unix(l.GetResets(), 0).Sub(now)
	if resets <= 0 {
		return 0, 0
	}
	return l.GetUsed(), resets
}

// untilText writes a wait in its two largest units: 3d 4h, 2h 10m, 7m.
func untilText(d time.Duration) string {
	d = d.Round(time.Minute)
	days, hours, minutes := int(d/(24*time.Hour)), int(d%(24*time.Hour)/time.Hour), int(d%time.Hour/time.Minute)
	switch {
	case days > 0:
		return Tf("%dd %dh", days, hours)
	case hours > 0:
		return Tf("%dh %dm", hours, minutes)
	}
	return Tf("%dm", max(minutes, 1))
}
