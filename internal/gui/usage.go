package gui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	uitheme "github.com/ironpark/ggui/ui/theme"
	"github.com/ironpark/sanbo/internal/client"
	"github.com/ironpark/sanbo/internal/protocol"
	"github.com/ironpark/sanbo/internal/transcript"
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

// usageDetail is a conversation's tokens in full.
func usageDetail(u *protocol.Usage) string {
	s := Tf("Input %s (cached %s) · output %s", tokens(u.GetInput()+u.GetCachedInput()), tokens(u.GetCachedInput()), tokens(u.GetOutput()))
	if u.GetContext() > 0 {
		s += "\n" + Tf("Latest prompt %s tokens", tokens(u.GetContext()))
		if u.GetWindow() > 0 {
			s += Tf(" of %s", tokens(u.GetWindow()))
		}
	}
	for _, l := range u.GetLimits() {
		s += "\n" + Tf("%s limit %d%% used", limitName(l.GetMinutes()), int(l.GetUsed()+0.5))
		if l.GetResets() > 0 {
			s += Tf(", resets %s", time.Unix(l.GetResets(), 0).Format("01-02 15:04"))
		}
	}
	return s
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
		return c.ReadChat(ctx, m.key(), req)
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

// usageCard is the overlay itself: the context filled, as a share of the
// window with a bar when the agent names the window, and each of the
// account's limits, such as Claude's five hours and week, with when it
// starts over. An account the agent reports no limits for, such as one on
// an API key, shows the tokens spent instead.
func usageCard(m *model, u *protocol.Usage) ggui.Widget {
	if u.IsZero() {
		return ggui.Box()
	}
	label := func(s string) ggui.Widget {
		return ggui.Box(ggui.Text(s).Size(11).Color(tokExtraMuted).NoWrap()).Width(52)
	}
	line := func(name, v string) ggui.Widget {
		return ggui.Row(label(name), ggui.Text(v).Size(11.5).Color(tokFg).NoWrap()).Gap(6).Align(ggui.AlignCenter)
	}
	bar := func(share float64) ggui.Widget { return ggui.Box(ui.Progress(ggui.Const(share)).Height(3)).Width(200) }
	var rows []ggui.Widget
	switch {
	case u.GetContext() > 0 && u.GetWindow() > 0:
		rows = append(rows, line(T("Context"), Tf("%s / %s · %d%%", tokens(u.GetContext()), tokens(u.GetWindow()), u.GetContext()*100/u.GetWindow())),
			bar(float64(u.GetContext())/float64(u.GetWindow())))
	case u.GetContext() > 0:
		rows = append(rows, line(T("Context"), tokens(u.GetContext())))
	}
	if limits := u.GetLimits(); len(limits) > 0 {
		// Only the limits age, as their resets come nearer.
		rows = append(rows, ggui.View(m.Minute, func(now time.Time) ggui.Widget {
			var rows []ggui.Widget
			for _, l := range limits {
				used, resets := limitNow(l, now)
				v := Tf("%d%%", int(used+0.5))
				if resets > 0 {
					v += " · " + Tf("resets in %s", untilText(resets))
				}
				rows = append(rows, line(limitName(l.GetMinutes()), v), bar(used/100))
			}
			return ggui.Column(rows...).Gap(5)
		}))
	} else {
		rows = append(rows, line(T("Total"), Tf("%s in · %s out", tokens(u.GetInput()+u.GetCachedInput()), tokens(u.GetOutput()))))
	}
	card := ggui.Reactive(func() ggui.Widget {
		return ggui.Box(ggui.Column(rows...).Gap(5)).
			Fill(tokSurface1).Border(1, tokBorder).Radius(8).Pad(7, 10).Shadow(uitheme.Use().PanelShadow)
	})
	return ui.Tooltip(card, usageDetail(u))
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
