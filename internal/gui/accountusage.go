package gui

import (
	"cmp"
	"context"
	"fmt"
	"image/color"
	"slices"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	uitheme "github.com/ironpark/ggui/ui/theme"
	"github.com/ironpark/ggui/widget"
	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

const (
	accountUsageRefreshInterval = 3 * time.Minute
	quotaWarnThreshold          = 85.0
)

// projectUsageSummary is the tokens a repository's sessions on one node
// spent: its worktrees' sessions count as its own.
type projectUsageSummary struct {
	NodeID   string
	Project  string
	Sessions int
	Input    int64
	Output   int64
	CostUSD  float64
}

func agentPeakQuota(acc *protocol.AgentAccountUsage, now time.Time) float64 {
	if acc == nil || acc.GetStatus() != "available" {
		return -1
	}
	var maxUsed float64
	for _, lim := range acc.GetLimits() {
		if lim.GetDisabled() {
			continue
		}
		if used, _ := accountLimitNow(lim, now); used > maxUsed {
			maxUsed = used
		}
	}
	return maxUsed
}

// suggestFallbackAgent reports whether currentTarget's agent is at or above
// quotaWarnThreshold and suggests the lowest-utilization available
// alternative: one below the threshold, and so below the current agent too,
// or none when every other agent is as spent.
func suggestFallbackAgent(data *protocol.AccountUsage, currentTarget string, availableTargets []string, now time.Time) (bool, string) {
	if data == nil {
		return false, ""
	}
	curAgent, _ := agentspec.SplitTarget(currentTarget)
	usageByAgent := map[string]float64{}
	for _, acc := range data.GetAccounts() {
		if peak := agentPeakQuota(acc, now); peak >= 0 {
			usageByAgent[acc.GetAgent()] = peak
		}
	}
	curUsed, ok := usageByAgent[curAgent]
	if !ok || curUsed < quotaWarnThreshold {
		return false, ""
	}
	bestTarget := ""
	bestUsed := min(quotaWarnThreshold, curUsed)
	for _, tgt := range availableTargets {
		if isShellTarget(tgt) {
			continue
		}
		ag, _ := agentspec.SplitTarget(tgt)
		if ag == curAgent {
			continue
		}
		if u, ok := usageByAgent[ag]; ok && u < bestUsed {
			bestUsed = u
			bestTarget = tgt
		}
	}
	return true, bestTarget
}

// aggregateProjectUsage adds up the sessions' usage by node and repository:
// a session in a worktree counts for the repository the worktree is of,
// and one folder on two nodes is two rows.
func aggregateProjectUsage(sessions []*protocol.SessionInfo, usageBySession map[string]*protocol.Usage) []projectUsageSummary {
	type key struct{ node, project string }
	byProj := map[key]*projectUsageSummary{}
	for _, s := range sessions {
		u := usageBySession[s.GetId()]
		if u == nil || (u.GetInput() == 0 && u.GetCachedInput() == 0 && u.GetOutput() == 0 && u.GetCostUsd() == 0) {
			continue
		}
		k := key{s.GetNodeId(), cmp.Or(s.GetWorktree().GetRepo(), s.GetProject(), "(unknown)")}
		row := byProj[k]
		if row == nil {
			row = &projectUsageSummary{NodeID: k.node, Project: k.project}
			byProj[k] = row
		}
		row.Sessions++
		row.Input += u.GetInput() + u.GetCachedInput()
		row.Output += u.GetOutput()
		row.CostUSD += u.GetCostUsd()
	}
	out := make([]projectUsageSummary, 0, len(byProj))
	for _, row := range byProj {
		out = append(out, *row)
	}
	slices.SortFunc(out, func(a, b projectUsageSummary) int {
		if a.CostUSD != b.CostUSD {
			return cmp.Compare(b.CostUSD, a.CostUSD)
		}
		if a.Input+a.Output != b.Input+b.Output {
			return cmp.Compare(b.Input+b.Output, a.Input+a.Output)
		}
		return cmp.Or(strings.Compare(a.Project, b.Project), strings.Compare(a.NodeID, b.NodeID))
	})
	return out
}

type accountUsageState struct {
	Open     *ggui.StateValue[bool]
	Data     *ggui.StateValue[*protocol.AccountUsage]
	Loading  *ggui.StateValue[bool]
	Err      *ggui.StateValue[string]
	inFlight bool
	// Sessions is how far the Analytics tab's refresh of every session's
	// usage has come.
	Sessions *ggui.StateValue[usageRefresh]
}

// usageRefresh is a refresh of every chat session's usage: how many
// sessions it asks about, and how many have answered.
type usageRefresh struct{ Done, Total int }

// running reports whether the refresh has sessions left to hear from.
func (r usageRefresh) running() bool { return r.Done < r.Total }

// usageRefreshConcurrency is how many sessions a refresh asks at once.
const usageRefreshConcurrency = 4

func newAccountUsageState() *accountUsageState {
	return &accountUsageState{
		Open: ggui.State(false),
		Data: ggui.State((*protocol.AccountUsage)(nil)).WithEqual(func(a, b *protocol.AccountUsage) bool {
			return proto.Equal(a, b)
		}),
		Loading:  ggui.State(false),
		Err:      ggui.State(""),
		Sessions: ggui.State(usageRefresh{}),
	}
}

// refreshSessionUsages asks every chat session for its usage again, a few
// at a time, as Sessions counts.
func (m *model) refreshSessionUsages() {
	st := m.AccountUsage
	if ggui.Peek(st.Sessions).running() {
		return
	}
	var ids []string
	for _, s := range ggui.Peek(m.Sessions) {
		if hasCap(s, protocol.CapChat) {
			ids = append(ids, s.GetId())
		}
	}
	st.Sessions.Set(usageRefresh{Total: len(ids)})
	next := 0
	var ask func()
	ask = func() {
		if next >= len(ids) {
			return
		}
		id := ids[next]
		next++
		m.loadUsageThen(id, m.usageState(id), func() {
			st.Sessions.Update(func(r usageRefresh) usageRefresh { r.Done++; return r })
			ask()
		})
	}
	for range min(usageRefreshConcurrency, len(ids)) {
		ask()
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
	tab := ggui.State("quotas")
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
		// Built as it opens, so what it reads is as it is then.
		if !st.Open.Get() {
			return widget.Box()
		}
		data := st.Data.Get()
		loading := st.Loading.Get()
		errText := st.Err.Get()
		now := m.Minute.Get()
		prefsVal := m.Prefs.Get()
		mode := prefsVal.UsageDisplay
		curTab := tab.Get()
		defaultAgent := cmp.Or(prefsVal.DefaultAgent, "claude")

		tabBtn := func(id, label string) ggui.Widget {
			b := ui.Button(label, func() { tab.Set(id) }).Pad(4, 10).Radius(7)
			if curTab != id {
				b = b.Ghost()
			}
			return b
		}
		tabBar := widget.Row(
			tabBtn("quotas", T("Quotas & switching")),
			tabBtn("analytics", T("Project & session usage")),
			widget.Spacer(),
		).Gap(6).Align(widget.AlignCenter)

		if curTab == "analytics" {
			return widget.Column(tabBar, accountAnalyticsView(m)).Gap(10).Stretch()
		}

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

		rows := []ggui.Widget{tabBar, topBar}

		availTargets := sessionAgentTargets(nodeTargets(m.Nodes.Get(), m.Conn.Get().Self.GetId(), m.Agents.Get()))
		if high, fallback := suggestFallbackAgent(data, defaultAgent, availTargets, now); high {
			warnItems := []ggui.Widget{
				icon("shield-alert").Size(14).Color(skin.DotWaiting),
				widget.Expanded(widget.Text(Tf("%s is nearing its quota limit.", targetLabel(defaultAgent))).Size(12.5).Weight(ggui.WeightMedium)),
			}
			if fallback != "" {
				fb := fallback
				warnItems = append(warnItems, ui.Button(Tf("Switch default to %s", targetLabel(fb)), func() {
					m.Prefs.Update(func(p prefs) prefs { p.DefaultAgent = fb; return p })
					m.toaster.Push(ui.Toast(T("Default agent updated"), targetLabel(fb)))
				}).Pad(4, 10).Radius(6))
			}
			rows = append(rows, widget.Box(widget.Row(warnItems...).Gap(8).Align(widget.AlignCenter)).
				Fill(skin.DotWaiting.Alpha(0.12)).Border(1, skin.DotWaiting).Radius(8).Pad(8, 12))
		}

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
				rows = append(rows, accountUsageCard(m, acc, now, mode, defaultAgent))
			}
		}
		return widget.Column(rows...).Gap(10).Stretch()
	})
	// Many accounts or projects scroll, in a window of any height.
	content := widget.Box(widget.Scroll(body).Shrink().Bar(skin.Surface3)).MaxHeight(accountUsageMaxHeight)
	return keyedDialog(st.Open, content).Title(T("Account usage & quotas")).Width(600)
}

// accountUsageMaxHeight is the most of the window the Account usage
// dialog's body takes before it scrolls.
const accountUsageMaxHeight = 560.0

// accountAnalyticsView is the Analytics tab: the sessions' usage by
// repository, as usages has it, which Refresh and each chat's reads keep.
// It follows UsagesRev as well as each usage it reads, so a session whose
// usage a refresh or a chat reads for the first time shows once it is in.
func accountAnalyticsView(m *model) ggui.Widget {
	return ggui.Reactive(func() ggui.Widget {
		sessions := m.Sessions.Get()
		m.UsagesRev.Get()
		refresh := m.AccountUsage.Sessions.Get()
		usageBySession := map[string]*protocol.Usage{}
		for _, s := range sessions {
			if us := m.usages[s.GetId()]; us != nil {
				if u := us.usage.Get(); u != nil {
					usageBySession[s.GetId()] = u
				}
			}
		}
		projects := aggregateProjectUsage(sessions, usageBySession)
		var totalIn, totalOut int64
		var totalCost float64
		nodes := map[string]bool{}
		for _, p := range projects {
			totalIn += p.Input
			totalOut += p.Output
			totalCost += p.CostUSD
			nodes[p.NodeID] = true
		}

		summary := Tf("Loaded sessions total: %s in · %s out · $%.2f", tokens(totalIn), tokens(totalOut), totalCost)
		if refresh.running() {
			summary = Tf("Loading usage: %d of %d sessions…", refresh.Done, refresh.Total)
		}
		rows := []ggui.Widget{
			widget.Row(
				widget.Expanded(muted(widget.Text(summary).Size(12.5))),
				ui.Button(T("Refresh"), m.refreshSessionUsages).Ghost().Pad(4, 8).Radius(6).Disabled(refresh.running()),
			).Gap(8).Align(widget.AlignCenter),
		}
		if len(projects) == 0 {
			rows = append(rows, muted(widget.Text(T("No session token usage loaded yet. Click Refresh or open a session's Chat to populate usage metrics.")).Size(12.5)))
			return widget.Column(rows...).Gap(10).Stretch()
		}
		allNodes := m.Nodes.Get()
		for _, p := range projects[:min(len(projects), 10)] {
			costStr := "-"
			if p.CostUSD > 0 {
				costStr = fmt.Sprintf("$%.2f", p.CostUSD)
			}
			title := []ggui.Widget{widget.Flexible(widget.Text(projectName(p.Project)).Size(13).Weight(ggui.WeightMedium).NoWrap().Ellipsis())}
			if len(nodes) > 1 {
				// The same folder on two nodes is two rows: each says its node.
				title = append(title, muted(widget.Text(nodeName(allNodes, p.NodeID)).Size(11.5).NoWrap()))
			}
			rows = append(rows, widget.Box(widget.Row(
				widget.Expanded(widget.Column(
					widget.Row(title...).Gap(6).Align(widget.AlignCenter),
					muted(widget.Text(Tf("%d sessions · %s in · %s out", p.Sessions, tokens(p.Input), tokens(p.Output))).Size(11.5).NoWrap()),
				).Gap(2).Stretch()),
				widget.Text(costStr).Size(12.5).Weight(ggui.WeightSemibold).NoWrap(),
			).Gap(8).Align(widget.AlignCenter)).Fill(skin.Surface1).Border(1, skin.Border).Radius(8).Pad(8, 12))
		}
		return widget.Column(rows...).Gap(8).Stretch()
	})
}

func accountUsageCard(m *model, acc *protocol.AgentAccountUsage, now time.Time, displayMode, defaultAgent string) ggui.Widget {
	name := acc.GetAgent()
	defBase, _ := agentspec.SplitTarget(defaultAgent)
	isDefault := defBase == name
	headItems := []ggui.Widget{
		agentGlyph(name, skin.Muted).Size(16),
		widget.Text(name).Size(14).Weight(ggui.WeightSemibold).NoWrap(),
	}
	if plan := acc.GetPlan(); plan != "" {
		headItems = append(headItems, widget.Box(widget.Text(plan).Size(11).Color(skin.Muted).NoWrap()).
			Fill(skin.Surface2).Radius(5).Pad(2, 6))
	}
	if isDefault {
		headItems = append(headItems, widget.Box(widget.Text(T("Default")).Size(11).Color(skin.AccentBright).NoWrap()).
			Fill(skin.AccentBright.Alpha(0.14)).Radius(5).Pad(2, 6))
	}
	headItems = append(headItems, widget.Spacer())
	if acc.GetStatus() == "available" && !isDefault {
		headItems = append(headItems, ui.Button(T("Set as default"), func() {
			m.Prefs.Update(func(p prefs) prefs { p.DefaultAgent = name; return p })
			m.toaster.Push(ui.Toast(T("Default agent updated"), name))
		}).Ghost().Pad(3, 8).Radius(6))
	}
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
