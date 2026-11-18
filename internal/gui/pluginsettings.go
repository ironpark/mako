package gui

import (
	"image/color"
	"strconv"
	"strings"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/plugin/pack"
	"github.com/ironpark/mako/internal/protocol"
)

// Settings ▸ Plugins lists the daemon's plugins, a card each: how it
// stands, what it asks for and the user allowed, and a page of its
// settings made from the schema its manifest declares. The page is in the
// settings' list only while the daemon has a plugin.

// pluginsSection is the settings' section of the plugins.
var pluginsSection = settingsSection{"plugins", "Plugins", "package"}

// settingsGroups are the settings' sections, Plugins among this node's
// while there are any.
func settingsGroups(hasPlugins bool) []settingsGroup {
	if !hasPlugins {
		return settingsNav
	}
	out := make([]settingsGroup, len(settingsNav))
	copy(out, settingsNav)
	for i, g := range out {
		if g.title == "This node" {
			out[i].sections = append(append([]settingsSection(nil), g.sections...), pluginsSection)
		}
	}
	return out
}

func pluginSettings(m *model) ggui.Widget {
	installed := ggui.View(m.plugins.List, func(list []*protocol.PluginInfo) ggui.Widget {
		cards := make([]ggui.Widget, 0, len(list))
		for _, p := range list {
			if m.storeOpen() {
				// Under the store's headings, a card is named by its own
				// head, not by a heading of its own.
				cards = append(cards, pluginCardBody(m, p))
			} else {
				cards = append(cards, pluginCard(m, p))
			}
		}
		if len(list) == 0 {
			cards = append(cards, card(widget.Box(muted(widget.Text(T("No plugins are in this node's plugins folder.")).Size(12.5))).Pad(14, 16)))
		}
		return widget.Column(cards...).Gap(sectionGap).Stretch()
	})
	about := T("The plugins this node's daemon runs, each in a sandbox of its own, from the plugins folder beside its configuration. A plugin runs once you allow what it asks for.")
	if !m.storeOpen() {
		return page(T("Plugins"), about, installed)
	}
	return page(T("Plugins"), about+" "+T("Installing is this node's own: another of your computers installs for itself."),
		section(T("Installed"), installed),
		section(T("Find plugins"), storeSection(m)),
		section(T("Registries"), registriesSection(m)),
	)
}

// stateLabels name a plugin's states.
var stateLabels = map[string]string{
	protocol.PluginWaiting:  "Waiting for you",
	protocol.PluginDenied:   "Refused",
	protocol.PluginReady:    "Ready",
	protocol.PluginRunning:  "Running",
	protocol.PluginFailed:   "Failed",
	protocol.PluginDisabled: "Disabled",
	protocol.PluginBlocked:  "Blocked",
}

func stateTone(state string) string {
	switch state {
	case protocol.PluginRunning:
		return "success"
	case protocol.PluginWaiting:
		return "warning"
	case protocol.PluginFailed, protocol.PluginDenied, protocol.PluginBlocked:
		return "danger"
	}
	return "muted"
}

// pluginCard is a plugin's card, under a heading of its ID.
func pluginCard(m *model, p *protocol.PluginInfo) ggui.Widget {
	return section(p.GetId(), pluginCardBody(m, p))
}

// pluginCardBody is a plugin's card.
func pluginCardBody(m *model, p *protocol.PluginInfo) ggui.Widget {
	id := p.GetId()
	act := func(op string) func() {
		return func() { m.actOnPlugin(&protocol.PluginAction{PluginId: id, Op: op}) }
	}
	toggle := ui.Button(T("Disable"), act(protocol.PluginDisable)).Outline()
	if p.GetState() == protocol.PluginDisabled || p.GetState() == protocol.PluginFailed {
		toggle = ui.Button(T("Enable"), act(protocol.PluginEnable)).Outline()
	}
	buttons := []ggui.Widget{pill(T(stateLabels[p.GetState()]), toneColor(stateTone(p.GetState())))}
	if p.GetInstalled() {
		// What it is, and whose, is the package's: there is nothing to
		// read again by hand.
		if p.GetUpdate() != "" {
			buttons = append(buttons, ui.Button(Tf("Update to %s", p.GetUpdate()), func() { m.installPlugin(protocol.UpdateOp, id, "") }).Secondary())
		}
		buttons = append(buttons, ui.Button(T("Remove"), func() { m.installPlugin(protocol.RemoveOp, id, "") }).Outline())
	} else {
		buttons = append(buttons, ui.Button(T("Reload"), act(protocol.PluginReload)).Outline())
	}
	if p.GetState() != protocol.PluginBlocked {
		buttons = append(buttons, toggle)
	}
	origin := muted(widget.Text(Tf("by %s", p.GetPublisher())).Size(12))
	var originRow ggui.Widget = origin
	if p.GetInstalled() {
		originRow = originLine(p.GetPublisher(), pack.Fingerprint(p.GetKey()), p.GetSource(), "")
	} else if m.storeOpen() {
		originRow = muted(widget.Text(Tf("by %s · a folder of plugins/, in development", p.GetPublisher())).Size(12))
	}
	head := widget.Box(widget.Row(
		icon("package").Size(18).Color(skin.Muted),
		widget.Expanded(widget.Column(
			widget.Row(widget.Text(id).Size(13.5).Weight(ggui.WeightMedium), muted(widget.Text(p.GetVersion()).Size(12))).Gap(8),
			originRow,
		).Gap(3).Stretch()),
		widget.Row(buttons...).Gap(10).Align(widget.AlignCenter),
	).Gap(10).Align(widget.AlignCenter)).Pad(14, 16)
	rows := []ggui.Widget{head}
	if p.GetBlocked() != "" {
		rows = append(rows, widget.Box(warnLine("circle-slash", skin.DiffRemoved,
			Tf("It does not run: %s. It cannot be enabled while the registry blocks it; removing that registry lifts its list.", p.GetBlocked()))).Pad(12, 16))
	}
	if p.GetError() != "" && p.GetState() != protocol.PluginBlocked {
		label := pick(p.GetStale(), T("Its newer version failed; the last that worked runs:"), T("It stopped:"))
		rows = append(rows, widget.Box(widget.Column(
			widget.Text(label).Size(12.5).Color(pick[color.Color](p.GetStale(), skin.DotWaiting, skin.DiffRemoved)),
			muted(widget.Text(p.GetError()).Size(12).Font(ggui.DefaultMonoFont())),
		).Gap(4).Stretch()).Pad(12, 16))
	}
	rows = append(rows, pluginPermissions(m, p, act))
	for _, s := range p.GetSettings() {
		rows = append(rows, pluginSettingRow(m, id, s))
	}
	return card(rows...)
}

// pluginPermissions lists what a plugin asks for, those allowed marked,
// and the answers.
func pluginPermissions(m *model, p *protocol.PluginInfo, act func(string) func()) ggui.Widget {
	if len(p.GetPermissions()) == 0 {
		return row(T("Permissions"), T("It asks for nothing beyond its blocks, its own settings and the log."), widget.Box())
	}
	var lines []ggui.Widget
	for _, perm := range p.GetPermissions() {
		allowed := contains(p.GetGranted(), perm)
		glyph := pick(allowed, "check", "circle")
		lines = append(lines, widget.Row(
			icon(glyph).Size(13).Color(pick[color.Color](allowed, skin.DiffAdded, skin.ExtraMuted)),
			widget.Expanded(widget.Text(permissionText(perm)).Size(12.5)),
			chip(perm),
		).Gap(8).Align(widget.AlignCenter))
	}
	var buttons []ggui.Widget
	if len(p.GetAsked()) > 0 {
		buttons = append(buttons, ui.Button(T("Deny"), act(protocol.PluginDeny)).Outline(), ui.Button(T("Allow"), act(protocol.PluginGrant)))
	} else {
		buttons = append(buttons, ui.Button(T("Revoke"), act(protocol.PluginDeny)).Outline())
	}
	hint := pick(len(p.GetAsked()) > 0, T("It runs once you allow these."), T("Revoking them stops it."))
	return stackedRow(T("Permissions"), hint, widget.Column(
		widget.Column(lines...).Gap(6).Stretch(),
		widget.Row(append([]ggui.Widget{widget.Spacer()}, buttons...)...).Gap(8),
	).Gap(10).Stretch())
}

// pluginSettingRow is a setting's row, its control as its type is.
func pluginSettingRow(m *model, id string, s *protocol.PluginSetting) ggui.Widget {
	key := s.GetKey()
	set := func(v string) {
		m.actOnPlugin(&protocol.PluginAction{PluginId: id, Op: protocol.PluginSet, Key: key, Value: v})
	}
	var control ggui.Widget
	switch s.GetType() {
	case "boolean":
		on := s.GetValue() == "true"
		control = ui.Switch(ggui.Bind(func() bool { return on }, func(v bool) { on = v; set(strconv.FormatBool(v)) }), "").Name(s.GetLabel())
	case "number":
		n, _ := strconv.ParseFloat(s.GetValue(), 64)
		control = widget.Box(ui.NumberInput(ggui.Bind(func() float64 { return n }, func(v float64) {
			n = v
			set(strconv.FormatFloat(v, 'f', -1, 64))
		})).Name(s.GetLabel())).Width(140)
	case "select":
		v := s.GetValue()
		control = widget.Box(ui.Select(ggui.Bind(func() string { return v }, func(x string) { v = x; set(x) })).
			Options(s.GetOptions()).Name(s.GetLabel())).Width(200)
	case "secret":
		draft := ggui.State("")
		save := func(v string) {
			if v != "" {
				set(v)
				draft.Set("")
			}
		}
		placeholder := pick(s.GetSet(), T("Set; type to replace it"), T("Not set"))
		field := ui.TextField(draft).Password().Placeholder(placeholder).Name(s.GetLabel()).OnSubmit(save).OnCommit(save)
		parts := []ggui.Widget{widget.Box(field).Width(220)}
		if s.GetSet() {
			parts = append(parts, ui.Button(T("Clear"), func() { set("") }).Outline())
		}
		control = widget.Row(parts...).Gap(8)
	default:
		draft := ggui.State(s.GetValue())
		save := func(v string) {
			if v != s.GetValue() {
				set(v)
			}
		}
		control = widget.Box(ui.TextField(draft).Name(s.GetLabel()).OnSubmit(save).OnCommit(save)).Width(220)
	}
	return row(s.GetLabel(), s.GetDescription(), control)
}

// pluginAskBar asks, over the window, to allow what a plugin asks for:
// the first that waits and was not put off in this window. The window
// draws it outside every document, as it does a layout change's bar.
func pluginAskBar(m *model) ggui.Widget {
	waiting := ggui.Derived(func() *protocol.PluginInfo {
		later := m.plugins.Later.Get()
		if m.LayoutStage.Get() != nil {
			return nil // a layout change is answered first
		}
		for _, p := range m.plugins.List.Get() {
			if p.GetState() == protocol.PluginWaiting && !later[p.GetId()] {
				return p
			}
		}
		return nil
	})
	return ggui.View(waiting, func(p *protocol.PluginInfo) ggui.Widget {
		if p == nil {
			return widget.Box()
		}
		id := p.GetId()
		var asks []string
		for _, perm := range p.GetAsked() {
			asks = append(asks, permissionText(perm))
		}
		head := Tf("The plugin %s %s asks to:", id, p.GetVersion())
		if len(p.GetGranted()) > 0 {
			// An update asks for what it adds alone.
			head = Tf("The plugin %s %s now also asks to:", id, p.GetVersion())
		}
		by := Tf("By %s. It does not run until you allow it.", p.GetPublisher())
		if p.GetInstalled() {
			by = Tf("By %s, key %s, from %s. It does not run until you allow it.", p.GetPublisher(),
				shortFingerprint(pack.Fingerprint(p.GetKey())), registryLabel(p.GetSource()))
		}
		body := widget.Column(
			widget.Text(head).Size(13.5).Weight(ggui.WeightMedium),
			muted(widget.Text(strings.Join(asks, " · ")).Size(12.5)),
			muted(widget.Text(by).Size(12.5)),
		).Gap(3).Stretch()
		later := func() {
			m.plugins.Later.Update(func(l map[string]bool) map[string]bool {
				next := map[string]bool{id: true}
				for k, v := range l {
					next[k] = v
				}
				return next
			})
		}
		buttons := []ggui.Widget{
			ui.Button(T("Later"), later).Ghost(),
			ui.Button(T("Deny"), func() { m.actOnPlugin(&protocol.PluginAction{PluginId: id, Op: protocol.PluginDeny}) }).Outline(),
			ui.Button(T("Allow"), func() { m.actOnPlugin(&protocol.PluginAction{PluginId: id, Op: protocol.PluginGrant}) }),
		}
		return askBarFrame("package", body, buttons)
	})
}

// askBarFrame frames a bar over the window as layoutBarFrame does, with
// another glyph.
func askBarFrame(glyph string, body ggui.Widget, buttons []ggui.Widget) ggui.Widget {
	row := widget.Row(
		icon(glyph).Size(18).Color(skin.DotWaiting),
		widget.Expanded(body),
		widget.Row(buttons...).Gap(8),
	).Gap(12).Align(widget.AlignCenter)
	card := widget.Box(row).Fill(skin.Surface1).Border(1, skin.DotWaiting).Radius(12).Pad(12, 16).MaxWidth(760)
	return widget.Padding(widget.Align(card).Top(), headerHeight+10, 16, 0, 16)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
