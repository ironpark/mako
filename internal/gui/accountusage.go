package gui

import (
	"context"
	"fmt"
	"image/color"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	uitheme "github.com/ironpark/ggui/ui/theme"
	"github.com/ironpark/ggui/widget"
	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

const accountUsageRefreshInterval = 3 * time.Minute

type accountUsageState struct {
	Open     *ggui.StateValue[bool]
	Data     *ggui.StateValue[*protocol.AccountUsage]
	Loading  *ggui.StateValue[bool]
	Err      *ggui.StateValue[string]
	inFlight bool
}

func newAccountUsageState() *accountUsageState {
	return &accountUsageState{
		Open: ggui.State(false),
		Data: ggui.State((*protocol.AccountUsage)(nil)).WithEqual(func(a, b *protocol.AccountUsage) bool {
			return proto.Equal(a, b)
		}),
		Loading: ggui.State(false),
		Err:     ggui.State(""),
	}
}

func (m *model) openAccountUsage() {
	m.AccountUsage.Open.Set(true)
	m.loadAccountUsage(false)
}

func (m *model) loadAccountUsage(refresh bool) {
	st := m.AccountUsage
	if st == nil || st.inFlight {
		return
	}
	if !refresh && ggui.Peek(st.Data) != nil {
		// Re-use fresh snapshot if fetched recently.
		data := ggui.Peek(st.Data)
		var newest int64
		for _, a := range data.GetAccounts() {
			if a.GetFetchedAt() > newest {
				newest = a.GetFetchedAt()
			}
		}
		if newest > 0 && time.Since(time.Unix(newest, 0)) < 2*time.Minute {
			return
		}
	}
	st.inFlight = true
	st.Loading.Set(true)
	if refresh {
		st.Err.Set("")
	}
	fetch(m, 25*time.Second, func(ctx context.Context, c *client.Client) (*protocol.AccountUsage, error) {
		return c.ReadAccountUsage(ctx, &protocol.ReadAccountUsage{Refresh: refresh})
	}, func(res *protocol.AccountUsage, err error) {
		st.inFlight = false
		st.Loading.Set(false)
		if err != nil {
			st.Err.Set(nodeErr(err))
			return
		}
		st.Err.Set("")
		st.Data.Set(res)
	})
}

// accountUsageButton sits in the sidebar footer beside Settings and opens the
// global account quota overview across Claude, Codex, and Antigravity.
func accountUsageButton(m *model) ggui.Widget {
	return ggui.Reactive(func() ggui.Widget {
		data := m.AccountUsage.Data.Get()
		now := m.Minute.Get()
		peak := peakAccountUsageShare(data, now)
		c := shareColor(peak, skin.Muted)
		tip := T("Account usage & quotas")
		if summary := compactAccountUsageSummary(data, now); summary != "" {
			tip += "\n" + summary
		}
		btn := ui.ButtonOf(icon("radar").Color(c), m.openAccountUsage).Ghost().Pad(6).Radius(6).Name(T("Account usage & quotas"))
		return ui.Tooltip(btn, tip)
	})
}

func peakAccountUsageShare(data *protocol.AccountUsage, now time.Time) float64 {
	var peak float64
	for _, acc := range data.GetAccounts() {
		if acc.GetStatus() != "available" {
			continue
		}
		for _, lim := range acc.GetLimits() {
			if lim.GetDisabled() {
				continue
			}
			used, _ := accountLimitNow(lim, now)
			if s := used / 100.0; s > peak {
				peak = s
			}
		}
	}
	return peak
}

func compactAccountUsageSummary(data *protocol.AccountUsage, now time.Time) string {
	var parts []string
	for _, acc := range data.GetAccounts() {
		if acc.GetStatus() != "available" || len(acc.GetLimits()) == 0 {
			continue
		}
		var maxUsed float64
		for _, lim := range acc.GetLimits() {
			if lim.GetDisabled() {
				continue
			}
			used, _ := accountLimitNow(lim, now)
			if used > maxUsed {
				maxUsed = used
			}
		}
		parts = append(parts, fmt.Sprintf("%s %d%%", acc.GetAgent(), min(max(int(maxUsed+0.5), 0), 100)))
	}
	return strings.Join(parts, " · ")
}

func accountLimitNow(l *protocol.AccountLimit, now time.Time) (used float64, resets time.Duration) {
	if l.GetResets() == 0 {
		return l.GetUsed(), 0
	}
	resets = time.Unix(l.GetResets(), 0).Sub(now)
	if resets <= 0 {
		return 0, 0
	}
	return l.GetUsed(), resets
}

func accountUsageDialog(m *model) ggui.Widget {
	st := m.AccountUsage
	ggui.Effect(func() ggui.Cleanup {
		if !st.Open.Get() {
			return nil
		}
		m.loadAccountUsage(false)
		return m.every(accountUsageRefreshInterval, func() {
			m.loadAccountUsage(true)
		})
	})

	body := ggui.Reactive(func() ggui.Widget {
		data := st.Data.Get()
		loading := st.Loading.Get()
		errText := st.Err.Get()
		now := m.Minute.Get()
		mode := m.Prefs.Get().UsageDisplay

		var refreshIcon ggui.Widget = icon("refresh-cw").Size(13).Color(skin.Muted)
		if loading {
			refreshIcon = ui.Spinner().Size(13)
		}
		topBar := widget.Row(
			widget.Expanded(muted(widget.Text(T("Session-independent quota across installed agents.")).Size(12.5))),
			ui.ButtonOf(widget.Row(refreshIcon, widget.Text(T("Refresh")).Size(12.5)).Gap(6).Align(widget.AlignCenter), func() {
				m.loadAccountUsage(true)
			}).Ghost().Pad(4, 8).Radius(6).Disabled(loading).Name(T("Refresh")),
		).Gap(8).Align(widget.AlignCenter)

		rows := []ggui.Widget{topBar}
		if errText != "" {
			rows = append(rows, widget.Box(widget.Row(
				icon("info").Size(14).Color(skin.DotDown),
				widget.Expanded(widget.Text(errText).Size(12.5).Color(skin.DotDown).Selectable()),
			).Gap(8).Align(widget.AlignCenter)).Fill(skin.Surface0).Border(1, skin.Border).Radius(8).Pad(8, 10))
		}

		accounts := data.GetAccounts()
		if len(accounts) == 0 {
			if loading {
				rows = append(rows, widget.Box(widget.Row(
					ui.Spinner().Size(14),
					muted(widget.Text(T("Checking account quotas…")).Size(13)),
				).Gap(8).Align(widget.AlignCenter)).Pad(20, 0))
			} else if errText == "" {
				rows = append(rows, muted(widget.Text(T("No account quota data available yet.")).Size(13)))
			}
		} else {
			for _, acc := range accounts {
				rows = append(rows, accountUsageCard(acc, now, mode))
			}
		}
		return widget.Column(rows...).Gap(10).Stretch()
	})
	return keyedDialog(st.Open, body).Title(T("Account usage & quotas")).Width(560)
}

func accountUsageCard(acc *protocol.AgentAccountUsage, now time.Time, displayMode string) ggui.Widget {
	name := acc.GetAgent()
	headItems := []ggui.Widget{
		agentGlyph(name, skin.Muted).Size(16),
		widget.Text(name).Size(14).Weight(ggui.WeightSemibold).NoWrap(),
	}
	if plan := acc.GetPlan(); plan != "" {
		headItems = append(headItems, widget.Box(widget.Text(plan).Size(11).Color(skin.Muted).NoWrap()).
			Fill(skin.Surface2).Radius(5).Pad(2, 6))
	}
	headItems = append(headItems, widget.Spacer())
	if acc.Credits != nil {
		headItems = append(headItems, widget.Box(widget.Text(Tf("Credits $%.2f", acc.GetCredits())).Size(11.5).Color(skin.Fg).NoWrap()).
			Fill(skin.Surface2).Radius(5).Pad(2, 7))
	}
	var statusColor color.Color = skin.ExtraMuted
	statusText := T("Unavailable")
	switch acc.GetStatus() {
	case "available":
		statusColor = skin.DotRunning
		statusText = T("Connected")
	case "error":
		statusColor = skin.DotWaiting
		statusText = T("Error")
	}
	headItems = append(headItems, widget.Row(dot(statusColor), widget.Text(statusText).Size(11.5).Color(skin.Muted).NoWrap()).Gap(5).Align(widget.AlignCenter))

	rows := []ggui.Widget{widget.Row(headItems...).Gap(8).Align(widget.AlignCenter)}

	for _, lim := range acc.GetLimits() {
		rows = append(rows, accountLimitRow(lim, now, displayMode))
	}

	if detail := strings.TrimSpace(acc.GetDetail()); detail != "" {
		rows = append(rows, widget.Row(
			icon("info").Size(12).Color(skin.ExtraMuted),
			widget.Expanded(widget.Text(detail).Size(11.5).Color(skin.Muted).Selectable()),
		).Gap(6).Align(widget.AlignStart))
	}

	return widget.Box(widget.Column(rows...).Gap(8).Stretch()).
		Fill(skin.Surface1).Border(1, skin.Border).Radius(10).Pad(12, 14)
}

func accountLimitRow(lim *protocol.AccountLimit, now time.Time, displayMode string) ggui.Widget {
	label := lim.GetLabel()
	if grp := lim.GetGroup(); grp != "" {
		if label != "" {
			label = grp + " · " + label
		} else {
			label = grp
		}
	}
	if label == "" && lim.GetMinutes() > 0 {
		label = limitName(lim.GetMinutes())
	}

	if lim.GetDisabled() {
		return widget.Row(
			widget.Expanded(widget.Text(label).Size(12.5).Color(skin.Muted).NoWrap().Ellipsis()),
			widget.Text(T("Disabled")).Size(11.5).Color(skin.ExtraMuted).NoWrap(),
		).Gap(8).Align(widget.AlignCenter)
	}

	used, resets := accountLimitNow(lim, now)
	usedPct := min(max(int(used+0.5), 0), 100)
	share := min(max(used/100.0, 0), 1)
	barColor := shareColor(share, skin.AccentBright)

	pctText := Tf("%d%%", usedPct)
	if displayMode == "remaining" {
		pctText = Tf("%d%% left", 100-usedPct)
	}

	meta := []ggui.Widget{
		widget.Expanded(widget.Text(label).Size(12.5).NoWrap().Ellipsis()),
		widget.Text(pctText).Size(12).Weight(ggui.WeightMedium).Color(shareColor(share, skin.Fg)).NoWrap(),
	}
	if resets > 0 {
		meta = append(meta, muted(widget.Text(Tf("resets in %s", untilText(resets))).Size(11.5).NoWrap()))
	}

	bar := &quotaBar{share: share, fill: barColor}
	return widget.Column(
		widget.Row(meta...).Gap(8).Align(widget.AlignCenter),
		bar,
	).Gap(4).Stretch()
}

// quotaBar draws a thin rounded progress bar for an account quota window.
type quotaBar struct {
	share      float64
	fill       uitheme.Color
	trackColor color.Color
	fillColor  color.Color
}

func (q *quotaBar) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	theme := uitheme.Of(env)
	q.trackColor = theme.Muted
	q.fillColor = ggui.ResolveColor(q.fill, env)
	w := c.MaxW
	if w == ggui.Unbounded {
		w = 200
	}
	return c.Constrain(ggui.Sz(w, 5))
}

func (q *quotaBar) Paint(dst *ggui.Canvas, r ggui.Rect) {
	rad := r.Size.H / 2
	dst.FillRoundRect(r, rad, q.trackColor)
	if q.share <= 0 {
		return
	}
	fw := max(r.Size.W*min(q.share, 1.0), r.Size.H)
	if fw > r.Size.W {
		fw = r.Size.W
	}
	dst.FillRoundRect(ggui.Rct(r.Origin, ggui.Sz(fw, r.Size.H)), rad, q.fillColor)
}
