package gui

import (
	"fmt"
	"image/color"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

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

// settingsGroups are the settings' sections, with Plugins placed in
// Agents & extensions while there are any (or a store to install from).
func settingsGroups(hasPlugins bool) []settingsGroup {
	if !hasPlugins {
		return settingsNav
	}
	out := make([]settingsGroup, len(settingsNav))
	copy(out, settingsNav)
	for i, g := range out {
		if g.title == "Agents & extensions" {
			secs := make([]settingsSection, 0, len(g.sections)+1)
			inserted := false
			for _, s := range g.sections {
				if s.id == "extensions" {
					secs = append(secs, pluginsSection)
					inserted = true
				}
				secs = append(secs, s)
			}
			if !inserted {
				secs = append(secs, pluginsSection)
			}
			out[i].sections = secs
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
	installed = widget.Column(pluginSyncCard(m), secretsNote(m), installed).Gap(sectionGap).Stretch()
	about := T("The plugins this node's daemon runs, each in a sandbox of its own, from the plugins folder beside its configuration. A plugin runs once you allow what it asks for.")
	if !m.storeOpen() {
		return page(T("Plugins"), about, installed)
	}
	return page(T("Plugins"), about+" "+T("When sync is on, registries, store installs, non-secret settings and enabled states replicate across mutually trusted nodes while permissions and secrets stay on each node."),
		section(T("Installed"), installed),
		section(T("Find plugins"), storeSection(m)),
		section(T("Registries"), registriesSection(m)),
	)
}

func pluginSyncCard(m *model) ggui.Widget {
	return card(row(
		T("Sync plugins across nodes"),
		T("Share plugin registries, store-installed plugins, non-secret settings and enabled states with mutually trusted nodes. Permissions and secrets stay on each node."),
		ui.Switch(ggui.Bind(m.PluginSync.Get, m.setPluginSync), "").Name(T("Sync plugins across nodes")).
			BindDisabled(ggui.Not(m.WorkspaceSync)),
	))
}

// secretsNote says, over the plugins, that this node keeps their secrets
// in files, having no keychain: nothing while it has one.
func secretsNote(m *model) ggui.Widget {
	return ggui.View(m.plugins.Secrets, func(st secretStore) ggui.Widget {
		if st.Store != "file" {
			return widget.Box()
		}
		text := T("This node has no system keychain, so plugins' secrets are kept in a file of each plugin's that only you can read.")
		if st.Note != "" {
			text += " (" + st.Note + ")"
		}
		return card(widget.Box(warnLine("key-round", skin.DotWaiting, text)).Pad(12, 16))
	})
}

// secretWhere says where a secret setting is kept, under its label.
func secretWhere(st secretStore) string {
	switch st.Store {
	case "":
		return ""
	case "file":
		return T("Kept in a file only you can read: this node has no keychain.")
	}
	return Tf("Kept in %s, on this computer alone.", st.Store)
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
	switch {
	case p.GetBundled():
		// Bundled plugins ship inside mako: no Remove or Reload actions.
	case p.GetInstalled():
		// What it is, and whose, is the package's: there is nothing to
		// read again by hand.
		if p.GetUpdate() != "" {
			buttons = append(buttons, ui.Button(Tf("Update to %s", p.GetUpdate()), func() { m.installPlugin(protocol.UpdateOp, id, "") }).Secondary())
		}
		buttons = append(buttons, ui.Button(T("Remove"), func() { m.installPlugin(protocol.RemoveOp, id, "") }).Outline())
	default:
		buttons = append(buttons, ui.Button(T("Reload"), act(protocol.PluginReload)).Outline())
	}
	if p.GetState() != protocol.PluginBlocked {
		buttons = append(buttons, toggle)
	}
	origin := muted(widget.Text(Tf("by %s", p.GetPublisher())).Size(12))
	var originRow ggui.Widget = origin
	switch {
	case p.GetBundled():
		originRow = muted(widget.Text(Tf("by %s", p.GetPublisher()) + " · " + T("comes with mako")).Size(12))
	case p.GetInstalled():
		originRow = originLine(p.GetPublisher(), pack.Fingerprint(p.GetKey()), p.GetSource(), "")
	case m.storeOpen():
		originRow = muted(widget.Text(Tf("by %s · a folder of plugins/, in development", p.GetPublisher())).Size(12))
	}
	titleRow := []ggui.Widget{
		widget.Text(id).Size(13.5).Weight(ggui.WeightMedium),
		muted(widget.Text(p.GetVersion()).Size(12)),
	}
	if p.GetBundled() {
		titleRow = append(titleRow, pluginBundledBadge())
	}
	head := widget.Box(widget.Row(
		icon("package").Size(18).Color(skin.Muted),
		widget.Expanded(widget.Column(
			widget.Row(titleRow...).Gap(8).Align(widget.AlignCenter),
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
	if p.GetAgentsAllowed() {
		rows = append(rows, row(T("Agents"), T("You let it start agents without asking."),
			ui.Button(T("Ask again"), act(protocol.PluginForgetAgents)).Outline()))
	}
	for _, b := range p.GetBlocks() {
		rows = append(rows, pluginBlockRow(m, b))
	}
	if len(p.GetTools()) > 0 {
		rows = append(rows, pluginToolsRow(id, p.GetTools()))
	}
	if len(p.GetSettings()) > 0 {
		if hasScopedSettings(p.GetSettings()) {
			rows = append(rows, pluginScopedSettingsSection(m, id, p.GetSettings()))
		} else {
			for _, s := range p.GetSettings() {
				rows = append(rows, pluginSettingRow(m, id, s, ""))
			}
		}
	}
	rows = append(rows, pluginDiagnosticsRow(p))
	return card(rows...)
}

// pluginBlockRow lets the user place, focus, or remove a plugin's block from
// the window layout directly from Settings ▸ Plugins.
func pluginBlockRow(m *model, b *protocol.PluginBlock) ggui.Widget {
	name, title := b.GetName(), b.GetTitle()
	return ggui.Reactive(func() ggui.Widget {
		doc := m.WinDoc.Get()
		placed := doc != nil && doc.BlockPath(name) != nil
		var controls []ggui.Widget
		if placed {
			controls = append(controls,
				ui.Button(T("Show"), func() { m.revealBlock(name) }).Secondary(),
				ui.Button(T("Right tab"), func() { m.placePluginBlock(name, "right-stack") }).Outline(),
				ui.Button(T("Right column"), func() { m.placePluginBlock(name, "right-column") }).Outline(),
				ui.Button(T("Bottom"), func() { m.placePluginBlock(name, "bottom") }).Outline(),
				ui.Button(T("Remove from layout"), func() { m.removePluginBlock(name) }).Outline(),
			)
		} else {
			controls = append(controls,
				ui.Button(T("Right tab"), func() { m.placePluginBlock(name, "right-stack") }).Outline(),
				ui.Button(T("Right column"), func() { m.placePluginBlock(name, "right-column") }).Outline(),
				ui.Button(T("Bottom"), func() { m.placePluginBlock(name, "bottom") }).Outline(),
			)
		}
		return row(title, name, widget.Row(controls...).Gap(6).Align(widget.AlignCenter))
	})
}

// pluginToolsRow lists the CLI tools a plugin exposes to running agents.
func pluginToolsRow(pluginID string, tools []*protocol.PluginTool) ggui.Widget {
	var chips []ggui.Widget
	for _, tl := range tools {
		chips = append(chips, chip(pluginID+" "+tl.GetId()))
	}
	return stackedRow(T("Agent tools"),
		T("Available to agents via the mako-plugin CLI, automatically scoped to the active session."),
		widget.Wrap(chips...).Gap(6).RunGap(6))
}

// pluginDiagnosticsRow shows storage usage and a collapsible recent log viewer.
func pluginDiagnosticsRow(p *protocol.PluginInfo) ggui.Widget {
	open := ggui.State(false)
	used, quota := p.GetStorageUsed(), p.GetStorageQuota()
	storageSummary := fmt.Sprintf("%s / %s", formatPluginBytes(used), formatPluginBytes(quota))
	if quota == 0 {
		storageSummary = formatPluginBytes(used)
	}
	headerParts := []ggui.Widget{
		widget.Text(T("Diagnostics & logs")).Size(12.5).Weight(ggui.WeightMedium),
		muted(widget.Text(fmt.Sprintf("%s: %s", T("Storage"), storageSummary)).Size(12)),
	}
	if dbUsed, dbQuota := p.GetDbUsed(), p.GetDbQuota(); dbUsed > 0 || slices.Contains(p.GetPermissions(), "db") || slices.Contains(p.GetPermissions(), "db:sync") {
		dbSummary := fmt.Sprintf("%s / %s", formatPluginBytes(dbUsed), formatPluginBytes(dbQuota))
		if dbQuota == 0 {
			dbSummary = formatPluginBytes(dbUsed)
		}
		headerParts = append(headerParts, muted(widget.Text(fmt.Sprintf("· %s: %s", T("Database"), dbSummary)).Size(12)))
	}
	logs := p.GetLogs()
	headerParts = append(headerParts, muted(widget.Text(fmt.Sprintf("· %d", len(logs))).Size(12)))
	return widget.Box(widget.Column(
		widget.Row(
			widget.Expanded(widget.Row(headerParts...).Gap(8).Align(widget.AlignCenter)),
			ggui.View(open, func(o bool) ggui.Widget {
				return ui.Button(pick(o, T("Show less"), T("Show more")), func() { open.Set(!o) }).Ghost()
			}),
		).Gap(8).Align(widget.AlignCenter),
		reveal(open, func() ggui.Widget {
			if len(logs) == 0 {
				return widget.Padding(muted(widget.Text(T("No logs yet.")).Size(12)), 4, 0, 2, 0)
			}
			var lines []string
			for _, entry := range logs {
				ts := time.UnixMilli(entry.GetAt()).Local().Format("15:04:05")
				lines = append(lines, fmt.Sprintf("[%s] %s: %s", ts, strings.ToUpper(entry.GetLevel()), entry.GetMessage()))
			}
			return widget.Padding(widget.Box(widget.Scroll(toolBlock(strings.Join(lines, "\n"), false)).Shrink().Bar(skin.Surface3)).MaxHeight(180), 6, 0, 2, 0)
		}),
	).Gap(4).Stretch()).Pad(10, 16)
}

func formatPluginBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
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
	if p.GetBundled() {
		return stackedRow(T("Permissions"), T("Included with Mako."), widget.Column(lines...).Gap(6).Stretch())
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

// hasScopedSettings reports whether any setting declares a non-global scope.
func hasScopedSettings(settings []*protocol.PluginSetting) bool {
	return slices.ContainsFunc(settings, func(s *protocol.PluginSetting) bool {
		return isScoped(s.GetScope())
	})
}

// settingScopeLabel returns the human-readable display name for a scope kind.
func settingScopeLabel(scope string) string {
	switch scope {
	case protocol.ScopeSpace:
		return T("Space")
	case protocol.ScopeProject:
		return T("Project")
	case protocol.ScopeSession:
		return T("Session")
	default:
		return ""
	}
}

// pluginSettingScopes collects the selectable scope options for a plugin's
// settings: Global default, available targets for each declared scope kind,
// and any scope keys that already hold overrides. Reactive.
func pluginSettingScopes(m *model, settings []*protocol.PluginSetting) []scopeOption {
	var hasSpace, hasProject, hasSession bool
	var extraKeys []string
	for _, s := range settings {
		switch s.GetScope() {
		case protocol.ScopeSpace:
			hasSpace = true
		case protocol.ScopeProject:
			hasProject = true
		case protocol.ScopeSession:
			hasSession = true
		}
		for k := range s.GetScopedValues() {
			if k != "" {
				extraKeys = append(extraKeys, k)
			}
		}
		for _, k := range s.GetScopedSet() {
			if k != "" {
				extraKeys = append(extraKeys, k)
			}
		}
	}
	kinds := 0
	for _, on := range []bool{hasSpace, hasProject, hasSession} {
		if on {
			kinds++
		}
	}
	formatLabel := func(kind, name string) string {
		if kinds <= 1 {
			return name
		}
		switch kind {
		case protocol.ScopeSpace:
			return Tf("Space: %s", name)
		case protocol.ScopeProject:
			return Tf("Project: %s", name)
		case protocol.ScopeSession:
			return Tf("Session: %s", name)
		}
		return name
	}

	opts := []scopeOption{{Key: "", Label: T("Global default")}}
	seen := map[string]bool{"": true}
	addKind := func(kind string) {
		for _, o := range m.scopeOptions(kind) {
			if seen[o.Key] {
				continue
			}
			seen[o.Key] = true
			opts = append(opts, scopeOption{Key: o.Key, Label: formatLabel(kind, o.Label)})
		}
	}
	if hasSpace {
		addKind(protocol.ScopeSpace)
	}
	if hasProject {
		addKind(protocol.ScopeProject)
	}
	if hasSession {
		addKind(protocol.ScopeSession)
	}
	slices.Sort(extraKeys)
	for _, k := range slices.Compact(extraKeys) {
		if seen[k] {
			continue
		}
		seen[k] = true
		kind, _, _ := strings.Cut(k, ":")
		opts = append(opts, scopeOption{Key: k, Label: formatLabel(kind, m.scopeLabel(k))})
	}
	return opts
}

// setPluginSettingScope records the active settings scope for plugin id.
func (m *model) setPluginSettingScope(id, scopeKey string) {
	m.plugins.SettingScope.Update(func(cur map[string]string) map[string]string {
		next := maps.Clone(cur)
		if next == nil {
			next = map[string]string{}
		}
		if scopeKey == "" {
			delete(next, id)
		} else {
			next[id] = scopeKey
		}
		return next
	})
}

// pluginScopedSettingsSection renders the scope selector and every setting row
// for a plugin that declares at least one scoped setting.
func pluginScopedSettingsSection(m *model, id string, settings []*protocol.PluginSetting) ggui.Widget {
	return ggui.Reactive(func() ggui.Widget {
		selectedScope := m.plugins.SettingScope.Get()[id]
		opts := pluginSettingScopes(m, settings)
		currentLabel := T("Global default")
		for _, o := range opts {
			if o.Key == selectedScope {
				currentLabel = o.Label
				break
			}
		}
		if selectedScope != "" && currentLabel == T("Global default") {
			currentLabel = m.scopeLabel(selectedScope)
		}
		items := make([]ggui.Widget, 0, len(opts))
		for _, o := range opts {
			key := o.Key
			items = append(items, ui.MenuItem(o.Label, func() {
				m.setPluginSettingScope(id, key)
			}).Checked(key == selectedScope))
		}
		glyph := "globe"
		if selectedScope != "" {
			kind, _, _ := strings.Cut(selectedScope, ":")
			glyph = scopeKindIcon(kind)
		}
		face := widget.Row(
			icon(glyph).Size(13).Color(skin.Muted),
			widget.Text(currentLabel).Size(12.5).NoWrap(),
			icon("chevron-down").Size(13).Color(skin.Muted),
		).Gap(6).Align(widget.AlignCenter)
		selector := row(
			T("Settings scope"),
			T("Edit global defaults or override settings for a specific space or project."),
			ui.MenuOf(face, items...).Name(Tf("Settings scope: %s", currentLabel)),
		)
		children := make([]ggui.Widget, 0, 1+2*len(settings))
		children = append(children, selector)
		for _, s := range settings {
			children = append(children, hline(), pluginSettingRow(m, id, s, selectedScope))
		}
		return widget.Column(children...).Stretch()
	})
}

// settingRowWithBadges is a setting row with optional status pills/chips beside
// its label.
func settingRowWithBadges(title string, badges []ggui.Widget, hint string, control ggui.Widget) ggui.Widget {
	if len(badges) == 0 {
		return row(title, hint, control)
	}
	head := widget.Row(append([]ggui.Widget{widget.Text(title).Size(13.5)}, badges...)...).Gap(6).Align(widget.AlignCenter)
	text := []ggui.Widget{head}
	if hint != "" {
		text = append(text, muted(widget.Text(hint).Size(12)))
	}
	return widget.Box(widget.Row(
		widget.Expanded(widget.Column(text...).Gap(3).Stretch()),
		control,
	).Gap(24)).Pad(14, 16)
}

// pluginSettingRow is a setting's row, its control as its type is.
func pluginSettingRow(m *model, id string, s *protocol.PluginSetting, selectedScope string) ggui.Widget {
	key := s.GetKey()
	label, description := s.GetLabel(), s.GetDescription()
	if m.isBundledPlugin(id) {
		label = T(label)
		if description != "" {
			description = T(description)
		}
	}
	sScope := s.GetScope()
	applies := selectedScope != "" && isScoped(sScope) && strings.HasPrefix(selectedScope, sScope+":")
	actionScope := ""
	if applies {
		actionScope = selectedScope
	}
	set := func(v string) {
		m.actOnPlugin(&protocol.PluginAction{PluginId: id, Op: protocol.PluginSet, Key: key, Value: v, Scope: actionScope})
	}

	var badges []ggui.Widget
	effectiveVal := s.GetValue()
	overridden := false
	if applies {
		if s.GetType() == "secret" {
			overridden = slices.Contains(s.GetScopedSet(), selectedScope)
		} else if ov, ok := s.GetScopedValues()[selectedScope]; ok {
			effectiveVal = ov
			overridden = true
		}
		if overridden {
			badges = append(badges, pill(T("Overridden"), skin.AccentBright))
		} else if s.GetType() != "secret" || s.GetSet() {
			badges = append(badges, chip(T("Inherits default")))
		}
	} else if selectedScope != "" {
		badges = append(badges, chip(T("Applies globally")))
	} else if isScoped(sScope) {
		badges = append(badges, chip(settingScopeLabel(sScope)))
	}

	withReset := func(c ggui.Widget) ggui.Widget {
		if !applies || !overridden {
			return c
		}
		return widget.Row(c, ui.Button(T("Reset to default"), func() { set("") }).Outline()).Gap(8).Align(widget.AlignCenter)
	}

	var control ggui.Widget
	switch s.GetType() {
	case "boolean":
		on := effectiveVal == "true"
		control = withReset(ui.Switch(ggui.Bind(func() bool { return on }, func(v bool) { on = v; set(strconv.FormatBool(v)) }), "").Name(label))
	case "number":
		n, _ := strconv.ParseFloat(effectiveVal, 64)
		control = withReset(widget.Box(ui.NumberInput(ggui.Bind(func() float64 { return n }, func(v float64) {
			n = v
			set(strconv.FormatFloat(v, 'f', -1, 64))
		})).Name(label)).Width(140))
	case "select":
		v := effectiveVal
		control = withReset(widget.Box(ui.Select(ggui.Bind(func() string { return v }, func(x string) { v = x; set(x) })).
			Options(s.GetOptions()).Name(label)).Width(200))
	case "secret":
		draft := ggui.State("")
		save := func(v string) {
			if v != "" {
				set(v)
				draft.Set("")
			}
		}
		var placeholder string
		if applies {
			switch {
			case overridden:
				placeholder = T("Overridden; type to replace it")
			case s.GetSet():
				placeholder = T("Inherits default; type to override")
			default:
				placeholder = T("Not set")
			}
		} else {
			placeholder = pick(s.GetSet(), T("Set; type to replace it"), T("Not set"))
		}
		field := ui.TextField(draft).Key("secret:" + id + ":" + key + ":" + actionScope).Password().Placeholder(placeholder).Name(label).OnSubmit(save).OnCommit(save)
		parts := []ggui.Widget{widget.Box(field).Width(220)}
		if applies && overridden {
			parts = append(parts, ui.Button(T("Reset to default"), func() { set("") }).Outline())
		} else if !applies && s.GetSet() {
			parts = append(parts, ui.Button(T("Clear"), func() { set("") }).Outline())
		}
		control = widget.Row(parts...).Gap(8).Align(widget.AlignCenter)
		desc := strings.TrimSpace(description + " " + secretWhere(ggui.Peek(m.plugins.Secrets)))
		return settingRowWithBadges(label, badges, desc, control)
	default:
		draft := ggui.State(effectiveVal)
		save := func(v string) {
			if v != effectiveVal {
				set(v)
			}
		}
		field := ui.TextField(draft).Key("setting:" + id + ":" + key + ":" + actionScope + ":" + effectiveVal).Name(label).OnSubmit(save).OnCommit(save)
		control = withReset(widget.Box(field).Width(220))
	}
	return settingRowWithBadges(label, badges, description, control)
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
		lines := []ggui.Widget{
			widget.Text(head).Size(13.5).Weight(ggui.WeightMedium),
			muted(widget.Text(strings.Join(asks, " · ")).Size(12.5)),
			muted(widget.Text(by).Size(12.5)),
		}
		if k, ok := ggui.Peek(m.store.NewKey)[id]; ok && p.GetInstalled() {
			lines = append([]ggui.Widget{newKeyLine(k[0], k[1])}, lines...)
		}
		body := widget.Column(lines...).Gap(3).Stretch()
		later := func() {
			m.plugins.Later.Update(func(l map[string]bool) map[string]bool {
				next := map[string]bool{id: true}
				maps.Copy(next, l)
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

// pluginAgentBar asks, over the window, to allow an agent a plugin asks
// to start: starting one runs a program that costs money, so the first
// is the user's to allow, once or for the plugin from then on. It waits
// behind a layout change and a plugin asking for its permissions, which
// take the same place.
func pluginAgentBar(m *model) ggui.Widget {
	asking := ggui.Derived(func() *protocol.PluginInfo {
		if m.LayoutStage.Get() != nil {
			return nil
		}
		later := m.plugins.Later.Get()
		list := m.plugins.List.Get()
		for _, p := range list {
			if p.GetState() == protocol.PluginWaiting && !later[p.GetId()] {
				return nil
			}
		}
		for _, p := range list {
			if p.GetAgentAsk() != nil {
				return p
			}
		}
		return nil
	})
	return ggui.View(asking, func(p *protocol.PluginInfo) ggui.Widget {
		if p == nil {
			return widget.Box()
		}
		id, ask := p.GetId(), p.GetAgentAsk()
		answer := func(op, value string) func() {
			return func() {
				m.actOnPlugin(&protocol.PluginAction{PluginId: id, Op: op, Key: ask.GetId(), Value: value})
			}
		}
		agent := ask.GetAgent()
		if ask.GetProfile() != "" {
			agent += " · " + ask.GetProfile()
		}
		where := Tf("%s in %s (%s)", agent, ask.GetProject(), ask.GetRoot())
		if ask.GetName() != "" {
			where = ask.GetName() + " — " + where
		}
		isAsk := ask.GetKind() == "ask"
		heading := Tf("The plugin %s asks to start an agent", id)
		note := T("An agent runs programs on this computer and may cost money.")
		alwaysLabel := T("Always allow this plugin")
		if isAsk {
			heading = Tf("The plugin %s asks an agent a question", id)
			note = T("It runs once, without YOLO, in this project, and its answer goes to the plugin.")
			alwaysLabel = T("Always allow this agent")
		}
		lines := []ggui.Widget{
			widget.Text(heading).Size(13.5).Weight(ggui.WeightMedium),
			muted(widget.Text(where).Size(12.5).NoWrap().Ellipsis()),
		}
		if prompt := strings.Join(strings.Fields(ask.GetPrompt()), " "); prompt != "" && (!isAsk || prompt != ask.GetName()) {
			lines = append(lines, muted(widget.Text(prompt).Size(12.5).NoWrap().Ellipsis()))
		}
		lines = append(lines, muted(widget.Text(note).Size(12.5)))
		body := widget.Column(lines...).Gap(3).Stretch()
		buttons := []ggui.Widget{
			ui.Button(T("Deny"), answer(protocol.PluginDenyAgent, "")).Outline(),
			ui.Button(alwaysLabel, answer(protocol.PluginAllowAgent, protocol.AgentAlways)).Outline(),
			ui.Button(T("Allow once"), answer(protocol.PluginAllowAgent, protocol.AgentOnce)),
		}
		return askBarFrame("bot", body, buttons)
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
	return slices.Contains(list, s)
}
