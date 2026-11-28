package gui

import (
	"context"
	"math"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/ironpark/ggui"
	ggruntime "github.com/ironpark/ggui/runtime"
	"github.com/ironpark/ggui/ui"
	uitheme "github.com/ironpark/ggui/ui/theme"
	"github.com/ironpark/ggui/widget"
	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agents"
	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// The composer's bar, under what is typed: on the left a cohesive inline
// strip with +, the session's model picker, effort picker, permission mode
// picker and optional fast mode toggle; on the right the optional terminal
// escape hatch and the send/stop button.

// composerRadius rounds the composer's box.
const composerRadius = 20

// composerBar is the bar of session id's composer.
func composerBar(m *model, id string, state *chatState, input *widget.TextInputWidget, disabled ggui.Readable[bool]) ggui.Widget {
	attach := ui.Tooltip(ui.ButtonOf(icon("plus").Size(17).Color(skin.Muted), func() { m.attachFiles(id) }).
		Ghost().Pad(5).Radius(8).Name(T("Attach files")).BindDisabled(disabled), T("Attach files"))
	usage := ggui.Map(state.result, func(v chatResult) *protocol.Usage { return v.Chat.GetUsage() }).WithEqual(protocol.Same)
	// What the agent says it runs with, what it may be set to, and whether
	// fast mode is enabled. Only these of its usage, whose counts move with
	// every reply.
	type running struct {
		agent, mode, model, effort string
		fastMode                   bool
		linked                     bool
		controls                   *protocol.AgentControls
		catalog                    []*protocol.AgentModel
	}
	now := ggui.Derived(func() running {
		s, _ := sessionIn(m.Sessions.Get(), id)
		u := usage.Get()
		agentName := s.GetAgent()
		fastMode := s.GetFastMode()
		if !s.GetControls().GetFastMode() && u != nil && u.FastMode != nil {
			fastMode = u.GetFastMode()
		}
		var catalog []*protocol.AgentModel
		if agentName != "" {
			catalog = m.AgentCatalogs.Get()[agentName]
		}
		linked := m.Conn.Get().OK || (m.link != nil && m.link.isLinked())
		return running{
			agent:    agentName,
			mode:     u.GetPermissionMode(),
			model:    u.GetModel(),
			effort:   u.GetEffort(),
			fastMode: fastMode,
			linked:   linked,
			controls: s.GetControls(),
			catalog:  catalog,
		}
	}).WithEqual(func(a, b running) bool {
		return a.agent == b.agent && a.mode == b.mode && a.model == b.model &&
			a.effort == b.effort && a.fastMode == b.fastMode && a.linked == b.linked &&
			proto.Equal(a.controls, b.controls) &&
			slices.EqualFunc(a.catalog, b.catalog, func(x, y *protocol.AgentModel) bool { return proto.Equal(x, y) })
	})
	leftControls := ggui.View(now, func(r running) ggui.Widget {
		if r.linked && r.agent != "" && len(r.catalog) == 0 {
			m.ensureAgentCatalog(r.agent)
		}
		models := r.controls.GetModels()
		if len(models) == 0 && len(r.catalog) > 0 {
			if _, ok := agents.As[agent.Models](r.agent); ok {
				models = make([]string, 0, len(r.catalog))
				for _, cm := range r.catalog {
					if cm.GetId() != "" {
						models = append(models, cm.GetId())
					}
				}
			}
		}
		efforts := r.controls.GetEfforts()
		if len(efforts) > 0 && r.model != "" && len(r.catalog) > 0 {
			if cm := matchCatalogModel(r.model, r.catalog); cm != nil {
				efforts = intersectEfforts(cm.GetEfforts(), efforts)
			}
		}
		items := []ggui.Widget{attach}
		if w := modelMenu(m, id, r.agent, r.model, r.effort, models, r.catalog); w != nil {
			items = append(items, w)
		}
		if w := effortMenu(m, id, r.effort, efforts); w != nil {
			items = append(items, w)
		}
		if w := modeMenu(m, id, r.mode, r.controls.GetModes()); w != nil {
			items = append(items, w)
		}
		if w := fastModeButton(m, id, r.fastMode, r.controls.GetFastMode(), disabled); w != nil {
			items = append(items, w)
		}
		return widget.Row(items...).Gap(4).Align(widget.AlignCenter)
	})
	leftScroll := &hscroll{child: widget.Scroll(leftControls).Horizontal().Shrink().Bar(nil).FadeEdges(12)}
	// The agent at work on a turn, with nothing to send, can be stopped.
	stoppable := ggui.Derived(func() bool {
		s, ok := sessionIn(m.Sessions.Get(), id)
		return ok && s.GetStatus() == protocol.AgentWorking && !state.sendable()
	})
	button := ggui.View(stoppable, func(stop bool) ggui.Widget {
		if stop {
			return ui.Tooltip(roundButton("square", T("Stop"), func() { m.stopTurn(id) }, disabled), T("Stop the agent's turn"))
		}
		return roundButton("arrow-up", T("Send"), func() { m.sendComposer(id) }, disabled)
	})
	row := []ggui.Widget{widget.Expanded(leftScroll), ghostAcceptChip(m, id, state, input, disabled), sideQuestionButton(m, id, state, disabled)}
	if state.inTerminal != nil {
		// The agent's terminal, here, for what only it takes: a dialog no
		// card shows, a key the agent asks for.
		row = append(row, ui.Tooltip(ui.ButtonOf(icon("square-terminal").Size(16).Color(skin.Muted), state.inTerminal).
			Ghost().Pad(5).Radius(8).Name(T("Type in the terminal")).BindDisabled(disabled), T("Type in the terminal")))
	}
	row = append(row, button)
	return widget.Row(row...).Gap(6).Align(widget.AlignCenter)
}

// roundButton is the composer's main button: a light disc with glyph.
func roundButton(glyph, name string, fn func(), disabled ggui.Readable[bool]) ggui.Widget {
	return ui.ButtonOf(iconColored(glyph, uitheme.PrimaryFg).Size(16), fn).
		Pad(7).Radius(16).Name(name).BindDisabled(disabled)
}

// hscroll wraps a horizontal ScrollWidget so it takes its content's natural
// height rather than stretching to a bounded parent's MaxH.
type hscroll struct{ child *widget.ScrollWidget }

func (h *hscroll) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	sz := h.child.Layout(ggui.Constraints{MinW: c.MinW, MaxW: c.MaxW, MaxH: ggui.Unbounded}, env)
	return c.Constrain(sz)
}

func (h *hscroll) Paint(dst *ggui.Canvas, r ggui.Rect) { dst.Paint(h.child, r) }

// composerMenuTrigger styles a composer bar menu button like a borderless
// ghost control in both light and dark themes, while preserving theme borders
// for dividers inside its popup menu.
func composerMenuTrigger(menu *ui.MenuWidget) ggui.Widget {
	return uitheme.Override(func(t *uitheme.Theme) {
		t.ButtonPad = ggui.Insets(0)
		t.CardShadow = ggui.ShadowStyle{}
		*t = *ui.ButtonOutline.Restyle(t, func(th *uitheme.Theme) ui.ButtonStyle {
			return ui.ButtonGhost.Style(th)
		})
	}, menu)
}

// composerChip is a calm, monochrome inline toolbar chip: leading icon, label,
// and an optional trailing dropdown chevron when selectable.
func composerChip(glyph, label string, selectable bool) ggui.Widget {
	parts := []ggui.Widget{
		icon(glyph).Size(15).Color(skin.Muted),
		widget.Text(label).Size(13).Color(skin.Muted).NoWrap(),
	}
	if selectable {
		parts = append(parts, icon("chevron-down").Size(13).Color(skin.Muted))
	}
	return widget.Box(widget.Row(parts...).Gap(5).Align(widget.AlignCenter)).Pad(4, 6)
}

// attachFiles asks for files on this machine and attaches them to session
// id's draft, sending them over first to a session on another node.
func (m *model) attachFiles(id string) {
	dialogs := ggui.UseHost().Dialogs()
	ggui.Async(func() ([]string, error) {
		return dialogs.OpenFiles(ggruntime.FileDialog{Title: T("Attach files")})
	}, func(paths []string, err error) {
		if err == nil && len(paths) > 0 {
			m.handOver(id, paths, func(paths []string) { m.attachTo(id, paths) })
		}
	})
}

// stopTurn interrupts the turn session id's agent is on, routing through the
// daemon's Send RPC when linked (so managed agents such as Codex can run
// graceful turn/interrupt with force-restart fallback) and falling back to
// writing the interrupt key directly to the terminal attachment.
func (m *model) stopTurn(id string) {
	s, ok := m.session(id)
	known, _ := agents.Lookup(s.GetAgent())
	keys := agent.Interrupt(known.Driver)
	conn := m.conns[id]
	if !ok || !m.link.isLinked() {
		if conn != nil {
			conn.send(keys)
		}
		return
	}
	ggui.Async(func() (struct{}, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		c, err := m.link.client(ctx)
		if err != nil {
			return struct{}{}, err
		}
		_, err = c.Send(ctx, &protocol.Send{NodeId: s.GetNodeId(), SessionId: id, Keys: keys})
		return struct{}{}, err
	}, func(_ struct{}, err error) {
		if err != nil && conn != nil {
			conn.send(keys)
		}
	})
}

// modeIcon returns the Lucide icon name for a permission mode.
func modeIcon(mode string) string {
	switch mode {
	case "bypassPermissions":
		return "shield-off"
	case "readOnly":
		return "lock"
	case "acceptEdits":
		return "pencil"
	case "plan":
		return "list"
	case "default", "auto", "dontAsk":
		return "shield"
	default:
		return "shield"
	}
}

// permissionChip says what the agent may do unasked, by the mode it names.
// Nothing for an agent that says none.
func permissionChip(mode string, selectable bool) ggui.Widget {
	if mode == "" {
		return widget.Box()
	}
	return composerChip(modeIcon(mode), modeLabel(mode), selectable)
}

// modeMenu is the permission chip, which switches the agent to one of
// modes, where it has any.
func modeMenu(m *model, id, mode string, modes []string) ggui.Widget {
	tip := T("What the agent may do without asking, as its terminal sets it.")
	if len(modes) == 0 {
		if mode == "" {
			return nil
		}
		return ui.Tooltip(permissionChip(mode, false), tip)
	}
	if mode == "" {
		mode = "default"
	}
	items := make([]ggui.Widget, len(modes))
	for i, to := range modes {
		items[i] = ui.MenuItem(modeLabel(to), func() {
			m.configureAgent(id, &protocol.ConfigureAgent{Mode: to})
		}).Icon(icon(modeIcon(to)).Size(14)).Checked(to == mode)
	}
	menu := ui.MenuOf(permissionChip(mode, true), items...).Width(220).Name(T("Permission mode"))
	return ui.Tooltip(composerMenuTrigger(menu), tip)
}

// modelChip names the model the latest reply was made with alongside the
// session's agent glyph, or the fallback label when selectable and unset.
func modelChip(agentName, current string, selectable bool, catalog ...*protocol.AgentModel) ggui.Widget {
	return modelChipFor(agentName, current, "", selectable, catalog)
}

func modelChipFor(agentName, current, effort string, selectable bool, catalog []*protocol.AgentModel) ggui.Widget {
	label := modelChipLabel(current, effort, catalog)
	if label == "" {
		if !selectable {
			return widget.Box()
		}
		label = T("Model")
	}
	return composerChip(agentIcon(agentName), label, selectable)
}

// modelMenu is the dedicated model chip, which sets the agent's model to one
// of those it offers; the agent keeps what is set as the user's default.
func modelMenu(m *model, id, agentName, current, effort string, models []string, catalog []*protocol.AgentModel) ggui.Widget {
	if len(models) == 0 {
		if current == "" {
			return nil
		}
		return ui.Tooltip(modelChipFor(agentName, current, effort, false, catalog), current)
	}
	items := make([]ggui.Widget, 0, len(models)+2)
	for _, to := range models {
		items = append(items, ui.MenuItem(modelOptionLabel(to, catalog), func() {
			m.configureAgent(id, &protocol.ConfigureAgent{Model: to})
		}).Checked(modelMatches(current, to, catalog)))
	}
	items = append(items, ui.MenuDivider(), ui.MenuItem(T("The agent keeps these as your default"), nil).Disabled(true))
	tip := current
	if tip == "" {
		tip = T("Model")
	}
	menu := ui.MenuOf(modelChipFor(agentName, current, effort, true, catalog), items...).Width(260).Name(T("Model"))
	return ui.Tooltip(composerMenuTrigger(menu), tip)
}

// effortChip names the reasoning effort alongside the brain icon, or the
// fallback label when selectable and unset.
func effortChip(effort string, selectable bool) ggui.Widget {
	label := effortLabel(effort)
	if label == "" {
		if !selectable {
			return widget.Box()
		}
		label = T("Effort")
	}
	return composerChip("brain", label, selectable)
}

// effortMenu is the dedicated reasoning effort chip, opening a stepped slider
// popover over the offered effort tiers; the agent keeps what is set as default.
func effortMenu(m *model, id, current string, efforts []string) ggui.Widget {
	if len(efforts) == 0 {
		if current == "" {
			return nil
		}
		return ui.Tooltip(effortChip(current, false), Tf("%s effort", effortLabel(current)))
	}
	initialIdx := effortIndex(current, efforts)
	sliderVal := ggui.State(float64(initialIdx))
	activeIdx := ggui.Map(sliderVal, func(v float64) int {
		return clampEffortIndex(int(math.Round(v)), len(efforts))
	})
	lastCommitted := current
	commit := func(idx int) {
		idx = clampEffortIndex(idx, len(efforts))
		sliderVal.Set(float64(idx))
		target := efforts[idx]
		if !strings.EqualFold(target, lastCommitted) {
			lastCommitted = target
			m.configureAgent(id, &protocol.ConfigureAgent{Effort: target})
		}
	}

	header := widget.Row(
		widget.Text(T("Reasoning effort")).Size(12.5).Weight(ggui.WeightMedium).Color(skin.Muted).NoWrap(),
		widget.Spacer(),
		ggui.View(activeIdx, func(idx int) ggui.Widget {
			return widget.Text(effortLabel(efforts[idx])).Size(12.5).Weight(ggui.WeightSemibold).Color(skin.AccentBright).NoWrap()
		}),
	).Align(widget.AlignCenter)

	var sliderFrom, sliderDerived *uitheme.Theme
	slider := ggui.WithEnv(func(env ggui.Env) ggui.Env {
		cur := uitheme.Of(env)
		if cur != sliderFrom || sliderDerived == nil {
			t := cur.Clone()
			if track := ggui.ResolveColor(skin.Surface3, env); track != nil {
				t.Muted = track
			}
			sliderFrom, sliderDerived = cur, t
		}
		return sliderDerived.Apply(env)
	}, ui.Slider(sliderVal, 0, float64(max(len(efforts)-1, 0))).
		Step(1).LargeStep(1).
		Name(T("Reasoning effort")).
		Disabled(len(efforts) <= 1).
		OnCommit(func(v float64) {
			commit(int(math.Round(v)))
		}))

	ticks := ggui.View(activeIdx, func(idx int) ggui.Widget {
		cols := make([]ggui.Widget, 0, max(len(efforts)*2-1, 1))
		for i, to := range efforts {
			if i > 0 {
				cols = append(cols, widget.Spacer())
			}
			label := effortLabel(to)
			txt := widget.Text(label).Size(11).NoWrap()
			if i == idx {
				txt = txt.Weight(ggui.WeightSemibold).Color(skin.Fg)
			} else {
				txt = txt.Color(skin.Muted)
			}
			stop := i
			cols = append(cols, ui.ButtonOf(txt, func() { commit(stop) }).
				Ghost().Pad(2, 3).Radius(6).Name(label))
		}
		return widget.Row(cols...).Align(widget.AlignCenter)
	})

	body := widget.Column(
		header,
		widget.Column(slider, ticks).Gap(4).Stretch(),
		widget.Box().Height(1).Fill(skin.Surface3),
		widget.Text(T("The agent keeps these as your default")).Size(11.5).Color(skin.ExtraMuted).NoWrap().Ellipsis(),
	).Gap(10).Stretch()

	tip := T("Effort")
	if current != "" {
		tip = Tf("%s effort", effortLabel(current))
	}
	pop := ui.PopoverOf(effortChip(current, true), body).
		Width(264).Pad(12).
		Side(widget.PopupTop).
		Align(widget.PopupStart).
		Name(T("Effort")).
		OnClose(func() {
			sliderVal.Set(float64(effortIndex(lastCommitted, efforts)))
		})
	pop.Trigger().Ghost().Pad(0).Radius(8)
	return ui.Tooltip(pop, tip)
}

func effortIndex(current string, efforts []string) int {
	if len(efforts) == 0 {
		return 0
	}
	if current != "" {
		for i, e := range efforts {
			if strings.EqualFold(e, current) {
				return i
			}
		}
	}
	for i, e := range efforts {
		if strings.EqualFold(e, "medium") {
			return i
		}
	}
	return 0
}

func clampEffortIndex(idx, n int) int {
	if n <= 1 || idx < 0 {
		return 0
	}
	if idx >= n {
		return n - 1
	}
	return idx
}

// fastModeButton is the lightning toggle at the trailing slot of the composer
// toolbar, shown only for fast-mode-capable sessions (such as managed Codex
// sessions) or sessions whose latest turn explicitly reported fast mode.
func fastModeButton(m *model, id string, enabled, selectable bool, disabled ggui.Readable[bool]) ggui.Widget {
	if !selectable && !enabled {
		return nil
	}
	if !selectable {
		return ui.Tooltip(
			widget.Box(icon("zap").Size(16).Color(skin.AccentBright)).Pad(5),
			T("Fast mode"),
		)
	}
	glyphColor := skin.Muted
	tip := T("Fast mode: off")
	if enabled {
		glyphColor = skin.AccentBright
		tip = T("Fast mode: on")
	}
	var btn ggui.Widget = ui.ButtonOf(icon("zap").Size(16).Color(glyphColor), func() {
		m.configureAgent(id, &protocol.ConfigureAgent{FastMode: proto.Bool(!enabled)})
	}).Ghost().Pad(5).Radius(8).Name(T("Fast mode")).BindDisabled(disabled)
	if enabled {
		btn = &washed{child: btn, color: skin.AccentBright, radius: 8}
	}
	return ui.Tooltip(btn, tip)
}

// configureAgent sets what req gives of session id's agent.
func (m *model) configureAgent(id string, req *protocol.ConfigureAgent) {
	info, ok := m.session(id)
	if !ok {
		return
	}
	req.NodeId, req.SessionId = m.remoteID(info.GetNodeId()), id
	fetch(m, 15*time.Second, func(ctx context.Context, c *client.Client) (*protocol.SessionInfo, error) {
		return c.ConfigureAgent(ctx, req)
	}, func(updated *protocol.SessionInfo, err error) {
		if err != nil {
			m.connErr(err)
		} else if updated != nil {
			m.editSession(id, func(cur *protocol.SessionInfo) {
				cur.FastMode = updated.GetFastMode()
				cur.Controls = proto.CloneOf(updated.GetControls())
			})
		}
		m.link.pollNow()
	})
}

// modeLabel names a permission mode.
func modeLabel(mode string) string {
	switch mode {
	case "bypassPermissions":
		return T("Full access")
	case "acceptEdits":
		return T("Accept edits")
	case "plan":
		return T("Plan mode")
	case "default":
		return T("Ask first")
	case "auto":
		return T("Auto")
	case "dontAsk":
		return T("Don't ask")
	case "readOnly":
		return T("Read only")
	}
	return mode
}

// effortLabel formats a reasoning effort tier for display.
func effortLabel(effort string) string {
	trimmed := strings.TrimSpace(effort)
	switch strings.ToLower(trimmed) {
	case "":
		return ""
	case "xhigh":
		return "XHigh"
	default:
		return capitalized(trimmed)
	}
}

// modelOptionLabel formats a selectable model alias or ID for the model menu,
// preferring the agent catalog's human-readable display name when present.
func modelOptionLabel(id string, catalog []*protocol.AgentModel) string {
	if cm := catalogModelByID(catalog, id); cm != nil && cm.GetName() != "" && !strings.EqualFold(cm.GetName(), cm.GetId()) {
		return cm.GetName()
	}
	if strings.HasPrefix(id, "claude-") {
		return modelName(id)
	}
	if strings.ContainsAny(id, "-._/") {
		return id
	}
	return capitalized(id)
}

func intersectEfforts(catalogEfforts, allowed []string) []string {
	if len(catalogEfforts) == 0 || len(allowed) == 0 {
		return nil
	}
	out := make([]string, 0, len(catalogEfforts))
	for _, e := range catalogEfforts {
		for _, a := range allowed {
			if strings.EqualFold(e, a) {
				if !slices.Contains(out, a) {
					out = append(out, a)
				}
				break
			}
		}
	}
	return out
}

func catalogModelByID(catalog []*protocol.AgentModel, id string) *protocol.AgentModel {
	if id == "" {
		return nil
	}
	for _, cm := range catalog {
		if strings.EqualFold(cm.GetId(), id) {
			return cm
		}
	}
	return nil
}

func isKnownEffortTier(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "low", "medium", "high", "xhigh", "max":
		return true
	}
	return false
}

func stripEffortParen(s string) (base, tier string, ok bool) {
	trimmed := strings.TrimSpace(s)
	if !strings.HasSuffix(trimmed, ")") {
		return "", "", false
	}
	open := strings.LastIndexByte(trimmed, '(')
	if open <= 0 {
		return "", "", false
	}
	inside := strings.TrimSpace(trimmed[open+1 : len(trimmed)-1])
	if !isKnownEffortTier(inside) {
		return "", "", false
	}
	return strings.TrimSpace(trimmed[:open]), strings.ToLower(inside), true
}

func stripEffortDash(s string) (base, tier string, ok bool) {
	trimmed := strings.TrimSpace(s)
	i := strings.LastIndexByte(trimmed, '-')
	if i <= 0 || i+1 >= len(trimmed) {
		return "", "", false
	}
	inside := strings.ToLower(strings.TrimSpace(trimmed[i+1:]))
	if !isKnownEffortTier(inside) {
		return "", "", false
	}
	return trimmed[:i], inside, true
}

func containsFold(list []string, target string) bool {
	for _, item := range list {
		if strings.EqualFold(item, target) {
			return true
		}
	}
	return false
}

func matchCatalogModel(current string, catalog []*protocol.AgentModel) *protocol.AgentModel {
	cur := strings.TrimSpace(current)
	if cur == "" || len(catalog) == 0 {
		return nil
	}
	// Pass 1: exact ID or exact display name match (preserves single-tier models
	// like "GPT-OSS 120B (Medium)" and non-effort variants like "Orchestrator (Neon)").
	for _, cm := range catalog {
		if strings.EqualFold(cm.GetId(), cur) || (cm.GetName() != "" && strings.EqualFold(cm.GetName(), cur)) {
			return cm
		}
	}
	// Pass 2: multi-tier variant ID ("gemini-4-neon-high") or display name with
	// trailing effort suffix ("Gemini 4 Neon (High)").
	if base, tier, ok := stripEffortDash(cur); ok {
		for _, cm := range catalog {
			if len(cm.GetEfforts()) > 0 && strings.EqualFold(cm.GetId(), base) && containsFold(cm.GetEfforts(), tier) {
				return cm
			}
		}
	}
	if base, tier, ok := stripEffortParen(cur); ok {
		for _, cm := range catalog {
			if len(cm.GetEfforts()) > 0 && cm.GetName() != "" && strings.EqualFold(cm.GetName(), base) && containsFold(cm.GetEfforts(), tier) {
				return cm
			}
		}
		for _, cm := range catalog {
			if len(cm.GetEfforts()) > 0 && cm.GetName() != "" && strings.EqualFold(cm.GetName(), base) {
				return cm
			}
		}
	}
	return nil
}

// modelChipLabel formats the active model for the composer chip, stripping
// redundant multi-tier effort suffixes while preserving single-tier and
// non-effort parenthetical qualifiers.
func modelChipLabel(current, effort string, catalog []*protocol.AgentModel) string {
	cur := strings.TrimSpace(current)
	if cur == "" {
		return ""
	}
	if strings.HasPrefix(cur, "claude-") {
		return modelName(cur)
	}
	if cm := matchCatalogModel(cur, catalog); cm != nil {
		if cm.GetName() != "" {
			return cm.GetName()
		}
		return cm.GetId()
	}
	if effort != "" {
		if base, tier, ok := stripEffortParen(cur); ok && strings.EqualFold(tier, effort) {
			return base
		}
	}
	return modelName(cur)
}

// modelMatches reports whether the active model name/ID matches a selectable
// option, using catalog metadata when available and supporting Claude aliases.
func modelMatches(current, option string, catalog []*protocol.AgentModel) bool {
	cur := strings.ToLower(strings.TrimSpace(current))
	opt := strings.ToLower(strings.TrimSpace(option))
	if cur == "" || opt == "" {
		return false
	}
	if cur == opt {
		return true
	}
	if cm := catalogModelByID(catalog, option); cm != nil {
		optName := strings.ToLower(strings.TrimSpace(cm.GetName()))
		if optName != "" && cur == optName {
			return true
		}
		if len(cm.GetEfforts()) > 0 {
			if base, _, ok := stripEffortDash(cur); ok && base == opt {
				return true
			}
			if base, _, ok := stripEffortParen(cur); ok && optName != "" && base == optName {
				return true
			}
		}
	}
	if base, _, ok := stripEffortDash(cur); ok && base == opt {
		return true
	}
	// Short alias matching (e.g. Claude's "opus"/"sonnet"/"haiku" against "claude-opus-5-5").
	if !strings.ContainsAny(opt, "-._/ ") {
		for _, tok := range strings.FieldsFunc(cur, func(r rune) bool {
			return r == '-' || r == '_' || r == ' ' || r == '/'
		}) {
			if tok == opt {
				return true
			}
		}
	}
	return false
}

// modelName is a model as people say it: claude-opus-5-5 is Opus 5.5, and
// claude-sonnet-4-5-20250929 Sonnet 4.5. Another agent's stays as it is.
func modelName(id string) string {
	rest, ok := strings.CutPrefix(id, "claude-")
	if !ok {
		return id
	}
	var name, version []string
	for part := range strings.SplitSeq(rest, "-") {
		switch {
		case part == "":
		case isDigits(part) && len(part) >= 8: // a date
		case isDigits(part):
			version = append(version, part)
		default:
			name = append(name, capitalized(part))
		}
	}
	if len(name) == 0 {
		return id
	}
	if len(version) == 0 {
		return strings.Join(name, " ")
	}
	return strings.Join(name, " ") + " " + strings.Join(version, ".")
}

func isDigits(s string) bool {
	return s != "" && strings.IndexFunc(s, func(r rune) bool { return !unicode.IsDigit(r) }) < 0
}

// capitalized is s with its first letter upper case.
func capitalized(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}
