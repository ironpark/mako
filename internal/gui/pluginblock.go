package gui

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// A plugin's or an agent's block is drawn inside a frame the window
// draws, naming the block and where it comes from, the plugin or the
// agent's session, so that nothing either draws can pass for the window's
// own. Inside, the window draws the tree with its own components, in its
// theme's colours; and in the tree's place, how a plugin stands when it
// does not run: what it asks for, to allow it here, or why it stopped.

// blockFeed is a block as its stream last had it.
type blockFeed struct {
	Update *protocol.BlockUpdate // the last update, its tree whole
	Scope  string                // canonical scope key the feed belongs to
	Seq    int                   // counts the feeds, so each is new
}

// blockOrigin is where a block comes from, which its frame names: a
// plugin, or an agent's session.
type blockOrigin struct {
	plugin         string
	session, agent string
}

// blockScopeState describes the declared scope, optional pin, and resolved
// canonical scope key of a rendered block.
type blockScopeState struct {
	BlockScope string // "", "global", "space", "project", "session"
	Pinned     string // non-empty canonical scope key when pinned via layout props
	Key        string // resolved canonical scope key; "" for global or empty-scope
}

// scopeOption is one selectable scope target in a block chip or settings menu.
type scopeOption struct {
	Key   string // "" for global default, or "<kind>:<id>"
	Label string
}

// isScoped reports whether scope is one of the non-global scopes.
func isScoped(scope string) bool {
	return scope == protocol.ScopeSpace || scope == protocol.ScopeProject || scope == protocol.ScopeSession
}

// blockPropScope returns the raw string value of props["scope"], or "".
func blockPropScope(props map[string]any) string {
	if props == nil {
		return ""
	}
	s, _ := props["scope"].(string)
	return s
}

// pinnedBlockScope returns props["scope"] when it is a non-empty canonical
// scope key matching blockScope + ":", else "".
func pinnedBlockScope(blockScope string, props map[string]any) string {
	if !isScoped(blockScope) {
		return ""
	}
	raw := blockPropScope(props)
	if rest, ok := strings.CutPrefix(raw, blockScope+":"); ok && rest != "" {
		return raw
	}
	return ""
}

// activeTabID returns the active tab ID of the focused pane, falling back to
// m.Selected. Reactive: reads m.Layout, m.FocusGroup, and m.Selected.
func (m *model) activeTabID() string {
	selected := m.Selected.Get()
	focusID := m.FocusGroup.Get()
	if g := m.Layout.Get().group(focusID); g != nil && g.Active != "" {
		return g.Active
	}
	return selected
}

// resolveActiveScope resolves the active canonical scope key ("space:<id>",
// "project:<id>", or "session:<id>") from the window's active context.
// Returns "" when no active target exists for blockScope. Reactive.
func (m *model) resolveActiveScope(blockScope string) string {
	switch blockScope {
	case protocol.ScopeSpace:
		p := m.spaces()
		if sp, ok := p.spaceOf(p.Space); ok && sp.ID != "" {
			return protocol.ScopeSpace + ":" + sp.ID
		}
		return ""

	case protocol.ScopeProject:
		p := m.spaces()
		tabID := m.activeTabID()
		docs, diffs := m.Docs.Get(), m.Diffs.Get()
		sessions := m.Sessions.Get()
		curScope := m.Scope.Get()
		scopedHere := curScope.Section != "" && (curScope.Space == "" || curScope.Space == p.spaceID(p.Space))

		if d, ok := docs[tabID]; ok && d.Root.Path != "" {
			if pl := placeIn(p.Projects, d.Root.Node, d.Root.Path, "", ""); pl.At >= 0 {
				return protocol.ScopeProject + ":" + p.Projects[pl.At].ID
			}
		}
		if d, ok := diffs[tabID]; ok && d.Root.Path != "" {
			if pl := placeIn(p.Projects, d.Root.Node, d.Root.Path, "", ""); pl.At >= 0 {
				return protocol.ScopeProject + ":" + p.Projects[pl.At].ID
			}
		}
		if cur, ok := sessionIn(sessions, tabID); ok {
			if !scopedHere || curScope.holds(sessionScope(p.Projects, cur)) {
				if pl := placeSession(p.Projects, cur); pl.At >= 0 {
					return protocol.ScopeProject + ":" + p.Projects[pl.At].ID
				}
			}
		}
		if scopedHere {
			if i := projectByID(p.Projects, curScope.Section); i >= 0 {
				return protocol.ScopeProject + ":" + p.Projects[i].ID
			}
		}
		if cur, ok := sessionIn(sessions, tabID); ok {
			if pl := placeSession(p.Projects, cur); pl.At >= 0 {
				return protocol.ScopeProject + ":" + p.Projects[pl.At].ID
			}
		}
		return ""

	case protocol.ScopeSession:
		tabID := m.activeTabID()
		sessions := m.Sessions.Get()
		if isSessionTab(tabID) {
			if _, ok := sessionIn(sessions, tabID); ok {
				return protocol.ScopeSession + ":" + tabID
			}
		}
		return ""
	}
	return ""
}

// scopeOptions lists the available targets for a scope kind. Reactive.
func (m *model) scopeOptions(kind string) []scopeOption {
	var out []scopeOption
	switch kind {
	case protocol.ScopeSpace:
		p := m.spaces()
		for i, sp := range p.Spaces {
			if sp.ID != "" {
				out = append(out, scopeOption{
					Key:   protocol.ScopeSpace + ":" + sp.ID,
					Label: spaceLabel(sp, i+1),
				})
			}
		}
	case protocol.ScopeProject:
		p := m.spaces()
		for _, proj := range p.Projects {
			if proj.ID != "" {
				out = append(out, scopeOption{
					Key:   protocol.ScopeProject + ":" + proj.ID,
					Label: projectLabel(p.Projects, proj.ID),
				})
			}
		}
	case protocol.ScopeSession:
		sessions := m.Sessions.Get()
		titles := m.Titles.Get()
		for _, s := range sessions {
			if id := s.GetId(); id != "" {
				out = append(out, scopeOption{
					Key:   protocol.ScopeSession + ":" + id,
					Label: sessionTitle(s, titles[id]),
				})
			}
		}
	}
	return out
}

// scopeLabel returns a human-readable label for a canonical scope key. Reactive.
func (m *model) scopeLabel(scopeKey string) string {
	if scopeKey == "" {
		return T("Global default")
	}
	kind, id, ok := strings.Cut(scopeKey, ":")
	if !ok {
		return scopeKey
	}
	switch kind {
	case protocol.ScopeSpace:
		p := m.spaces()
		if i := spaceIndex(p.Spaces, id); i >= 0 {
			return spaceLabel(p.Spaces[i], i+1)
		}
	case protocol.ScopeProject:
		p := m.spaces()
		if i := projectByID(p.Projects, id); i >= 0 {
			return projectLabel(p.Projects, id)
		}
	case protocol.ScopeSession:
		sessions := m.Sessions.Get()
		titles := m.Titles.Get()
		if s, ok := sessionIn(sessions, id); ok {
			return sessionTitle(s, titles[id])
		}
		return short(id)
	}
	return id
}

// pluginBlock follows the block of the name, of plugin id, while it is
// drawn; title is its title as listed, until the daemon sends one.
func pluginBlock(m *model, name, id, title, blockScope string, props map[string]any) ggui.Widget {
	return treeBlock(m, name, blockOrigin{plugin: id}, title, blockScope, props)
}

// treeBlock follows a block of a tree while it is drawn.
func treeBlock(m *model, name string, origin blockOrigin, title, blockScope string, props map[string]any) ggui.Widget {
	return ggui.Component(func() ggui.Widget {
		pinned := pinnedBlockScope(blockScope, props)
		resolved := ggui.Derived(func() string {
			if !isScoped(blockScope) {
				return ""
			}
			if pinned != "" {
				return pinned
			}
			return m.resolveActiveScope(blockScope)
		})
		feed := ggui.State(blockFeed{})
		form := newBlockForm()
		ggui.Effect(func() ggui.Cleanup {
			scopeKey := resolved.Get()
			if ggui.Peek(feed).Scope != scopeKey {
				feed.Set(blockFeed{Scope: scopeKey})
				form.reset()
			}
			if isScoped(blockScope) && scopeKey == "" {
				return nil // empty-scope: do not open a stream
			}
			if m.post == nil {
				return nil // not started, as under a test
			}
			ctx, cancel := context.WithCancel(context.Background())
			go m.followBlockScoped(ctx, name, scopeKey, func(u *protocol.BlockUpdate) {
				m.post(func() {
					if ctx.Err() != nil {
						return
					}
					feed.Update(func(f blockFeed) blockFeed {
						return blockFeed{Update: u, Scope: scopeKey, Seq: f.Seq + 1}
					})
				})
			})
			return cancel
		})
		return ggui.Reactive(func() ggui.Widget {
			f := feed.Get()
			scopeKey := resolved.Get()
			var u *protocol.BlockUpdate
			if f.Scope == scopeKey {
				u = f.Update
			}
			st := blockScopeState{BlockScope: blockScope, Pinned: pinned, Key: scopeKey}
			return blockFrame(m, name, origin, title, st, u, form)
		})
	})
}

// followBlockScoped watches a block on the daemon under scope, applying each
// update to the tree it holds and handing on the whole, until ctx ends.
func (m *model) followBlockScoped(ctx context.Context, name, scope string, fn func(*protocol.BlockUpdate)) {
	followStream(ctx, func(ctx context.Context, each func(*protocol.BlockUpdate)) error {
		c, err := m.link.client(ctx)
		if err != nil {
			return err
		}
		return c.WatchBlockScoped(ctx, name, scope, each)
	}, time.Second, fn)
}

// followStream follows a block's stream, which watch opens, as
// docs/protocol.md says: the first update holds the tree whole, those
// after it the ops from one render to the next. An op that does not apply
// to the tree held, or an op before any tree, leaves the tree unknown,
// which the stream will never send whole again: the stream is closed and
// opened again at once, for a whole tree. A stream that breaks is opened
// again after retry.
func followStream(ctx context.Context, watch func(context.Context, func(*protocol.BlockUpdate)) error, retry time.Duration, fn func(*protocol.BlockUpdate)) {
	for ctx.Err() == nil {
		var tree *protocol.UiNode
		stream, restart := context.WithCancel(ctx)
		lost := false
		err := watch(stream, func(u *protocol.BlockUpdate) {
			if lost {
				return
			}
			switch {
			case u.GetTree() != nil:
				tree = u.GetTree()
			case len(u.GetOps()) > 0:
				next, err := protocol.ApplyUi(tree, u.GetOps())
				if tree == nil || err != nil {
					lost = true
					restart()
					return
				}
				tree = next
			}
			whole := &protocol.BlockUpdate{Block: u.GetBlock(), Seq: u.GetSeq(), Tree: tree, Plugin: u.GetPlugin(),
				SessionId: u.GetSessionId(), Agent: u.GetAgent(), Title: u.GetTitle(), State: u.GetState(),
				Error: u.GetError(), Stale: u.GetStale(), Gone: u.GetGone(), Scope: u.GetScope()}
			fn(whole)
		})
		restart()
		if lost {
			continue
		}
		if err != nil && ctx.Err() == nil && tree == nil {
			fn(&protocol.BlockUpdate{State: protocol.PluginFailed, Error: err.Error()})
		}
		select {
		case <-ctx.Done():
		case <-time.After(retry):
		}
	}
}

// pluginFrame is a plugin's block's frame, for a tree drawn once.
func pluginFrame(m *model, name, id, title string, u *protocol.BlockUpdate) ggui.Widget {
	return blockFrame(m, name, blockOrigin{plugin: id}, title, blockScopeState{}, u, newBlockForm())
}

// pluginFrameScoped is a scoped plugin block's frame, for a tree drawn once.
func pluginFrameScoped(m *model, name, id, title string, st blockScopeState, u *protocol.BlockUpdate) ggui.Widget {
	return blockFrame(m, name, blockOrigin{plugin: id}, title, st, u, newBlockForm())
}

// blockFrame is a block's frame: a strip naming it and where it comes
// from over what it draws, or how its plugin stands.
func blockFrame(m *model, name string, origin blockOrigin, title string, st blockScopeState, u *protocol.BlockUpdate, form *blockForm) ggui.Widget {
	if title == "" {
		title = name
	}
	if u != nil && u.GetTitle() != "" {
		title = u.GetTitle()
	}
	glyph, kind, who := "package", T("Plugin"), origin.plugin
	if origin.plugin == "" {
		glyph, kind = "bot", T("Agent")
		who = cmpTone(origin.agent, T("Agent")) + " · " + short(origin.session)
	}
	headItems := []ggui.Widget{
		icon(glyph).Size(14).Color(skin.Muted),
		widget.Expanded(widget.Text(title).Size(12.5).Weight(ggui.WeightMedium).NoWrap().Ellipsis()),
	}
	if origin.plugin != "" && isScoped(st.BlockScope) {
		headItems = append(headItems, blockScopeChip(m, name, st))
	}
	headItems = append(headItems,
		muted(widget.Text(kind).Size(11).NoWrap()),
		chip(who),
	)
	if origin.plugin != "" {
		pid := origin.plugin
		if p := m.pluginOf(pid); p != nil && !p.GetInstalled() {
			headItems = append(headItems, ui.Tooltip(
				ui.ButtonOf(icon("refresh-cw").Size(12).Color(skin.Muted), func() {
					m.actOnPlugin(&protocol.PluginAction{PluginId: pid, Op: protocol.PluginReload})
				}).Ghost().Pad(3).Radius(5).Name(T("Reload block")),
				T("Reload block"),
			))
		}
		headItems = append(headItems, ui.Tooltip(
			ui.ButtonOf(icon("x").Size(12).Color(skin.Muted), func() {
				m.removePluginBlock(name)
			}).Ghost().Pad(3).Radius(5).Name(T("Close block")),
			T("Close block"),
		))
	}
	head := widget.Box(widget.Row(headItems...).Gap(6).Align(widget.AlignCenter)).Pad(6, 10).Fill(skin.Surface1)
	return widget.Box(widget.Box(widget.Column(
		head,
		hline(),
		widget.Expanded(blockBody(m, name, origin, st, u, form)),
	).Stretch()).Border(1, skin.Border).Radius(8)).Pad(6).Fill(skin.Surface0)
}

// scopeKindIcon returns the bundled icon name for a scope kind.
func scopeKindIcon(kind string) string {
	switch kind {
	case protocol.ScopeSpace:
		return "layers"
	case protocol.ScopeProject:
		return "folder"
	case protocol.ScopeSession:
		return "terminal"
	default:
		return "globe"
	}
}

// scopeFollowLabel returns the "Follow active <kind>" menu label.
func scopeFollowLabel(kind string) string {
	switch kind {
	case protocol.ScopeSpace:
		return T("Follow active space")
	case protocol.ScopeProject:
		return T("Follow active project")
	case protocol.ScopeSession:
		return T("Follow active session")
	default:
		return T("Global default")
	}
}

// scopeEmptyChipLabel returns the chip label when no active target is selected.
func scopeEmptyChipLabel(kind string) string {
	switch kind {
	case protocol.ScopeSpace:
		return T("No space")
	case protocol.ScopeProject:
		return T("No project")
	case protocol.ScopeSession:
		return T("No session")
	default:
		return ""
	}
}

// blockScopeChip renders the compact clickable scope chip in a scoped block's
// header, letting the user switch between Follow active and Pin to a target.
func blockScopeChip(m *model, name string, st blockScopeState) ggui.Widget {
	opts := m.scopeOptions(st.BlockScope)
	items := []ggui.Widget{
		ui.MenuItem(scopeFollowLabel(st.BlockScope), func() {
			m.setPluginBlockScope(name, st.Pinned, "")
		}).Checked(st.Pinned == ""),
		ui.MenuDivider(),
	}
	if len(opts) == 0 {
		var none string
		switch st.BlockScope {
		case protocol.ScopeSpace:
			none = T("No spaces available")
		case protocol.ScopeProject:
			none = T("No projects available")
		default:
			none = T("No sessions available")
		}
		items = append(items, ui.MenuItem(none, func() {}).Disabled(true))
	} else {
		for _, opt := range opts {
			key := opt.Key
			items = append(items, ui.MenuItem(Tf("Pin to %s", opt.Label), func() {
				m.setPluginBlockScope(name, st.Pinned, key)
			}).Checked(st.Pinned == key))
		}
	}
	if st.Pinned != "" && !slices.ContainsFunc(opts, func(o scopeOption) bool { return o.Key == st.Pinned }) {
		pinnedKey := st.Pinned
		items = append(items, ui.MenuItem(Tf("Pin to %s", m.scopeLabel(pinnedKey)), func() {
			m.setPluginBlockScope(name, st.Pinned, pinnedKey)
		}).Checked(true))
	}

	label := scopeEmptyChipLabel(st.BlockScope)
	if st.Key != "" {
		label = m.scopeLabel(st.Key)
	}
	glyph := scopeKindIcon(st.BlockScope)
	glyphColor := skin.Muted
	if st.Pinned != "" {
		glyph = "pin"
		glyphColor = skin.AccentBright
	}
	face := widget.Box(widget.Row(
		icon(glyph).Size(11).Color(glyphColor),
		widget.Text(label).Size(10.5).Weight(ggui.WeightMedium).Color(skin.Fg).NoWrap().Ellipsis(),
		icon("chevron-down").Size(11).Color(skin.Muted),
	).Gap(4).Align(widget.AlignCenter)).Pad(2, 6).Radius(5).Fill(skin.Surface2).MaxWidth(150)
	menuName := Tf("Block scope: %s", label)
	if st.Pinned != "" {
		face = face.Border(1, skin.AccentBright)
		menuName = Tf("Block scope: %s (pinned)", label)
	}
	return iconMenu(ui.MenuOf(face, items...).Name(menuName), 0)
}

// emptyScopeCard renders the native empty-scope card when a scoped block has no
// active target, offering a quick Pin selector when targets exist.
func emptyScopeCard(m *model, name string, st blockScopeState) ggui.Widget {
	var title, hint, pinLabel string
	switch st.BlockScope {
	case protocol.ScopeSpace:
		title = T("Select a space to view this block")
		hint = T("Switch to a space in the sidebar, or pin this block to a space.")
		pinLabel = T("Pin to a space")
	case protocol.ScopeProject:
		title = T("Select a project to view this block")
		hint = T("Open a session or pick a project in the sidebar, or pin this block to a project.")
		pinLabel = T("Pin to a project")
	default:
		title = T("Select a session to view this block")
		hint = T("Focus a session tab, or pin this block to a session.")
		pinLabel = T("Pin to a session")
	}
	rows := []ggui.Widget{
		icon(scopeKindIcon(st.BlockScope)).Size(22).Color(skin.ExtraMuted),
		widget.Text(title).Size(13).Weight(ggui.WeightMedium),
		muted(widget.Text(hint).Size(12)),
	}
	if opts := m.scopeOptions(st.BlockScope); len(opts) > 0 {
		items := make([]ggui.Widget, 0, len(opts))
		for _, opt := range opts {
			key := opt.Key
			items = append(items, ui.MenuItem(opt.Label, func() {
				m.setPluginBlockScope(name, st.Pinned, key)
			}))
		}
		face := widget.Row(
			icon("pin").Size(13).Color(skin.Muted),
			widget.Text(pinLabel).Size(12.5).NoWrap(),
			icon("chevron-down").Size(13).Color(skin.Muted),
		).Gap(6).Align(widget.AlignCenter)
		rows = append(rows, widget.Box().Height(4), ui.MenuOf(face, items...).Name(pinLabel))
	}
	return widget.Center(widget.Box(widget.Column(rows...).Gap(6).Align(widget.AlignCenter)).
		Pad(18, 22).Fill(skin.Surface1).Border(1, skin.Border).Radius(10).MaxWidth(340))
}

// blockBody is what goes in the frame.
func blockBody(m *model, name string, origin blockOrigin, st blockScopeState, u *protocol.BlockUpdate, form *blockForm) ggui.Widget {
	if origin.plugin != "" && isScoped(st.BlockScope) && st.Key == "" {
		return emptyScopeCard(m, name, st)
	}
	if u == nil {
		return emptyNote("loader-circle", T("Starting…"))
	}
	if u.GetGone() {
		return emptyNote("circle-slash", T("This block was closed."))
	}
	id := origin.plugin
	switch u.GetState() {
	case protocol.PluginWaiting:
		return pluginAsks(m, m.pluginOf(id))
	case protocol.PluginDenied:
		return pluginNote(T("You refused what this plugin asks for."),
			ui.Button(T("Review in Settings"), func() { m.openSettings("plugins") }).Outline())
	case protocol.PluginBlocked:
		return pluginNote(Tf("This plugin is stopped: %s.", u.GetError()),
			ui.Button(T("Review in Settings"), func() { m.openSettings("plugins") }).Outline())
	case protocol.PluginDisabled:
		return pluginNote(T("This plugin is disabled."),
			ui.Button(T("Enable"), func() { m.actOnPlugin(&protocol.PluginAction{PluginId: id, Op: protocol.PluginEnable}) }).Outline())
	}
	var rows []ggui.Widget
	if u.GetError() != "" {
		rows = append(rows, pluginError(m, id, u))
	}
	if u.GetTree() != nil {
		form.redraw()
		v := &treeView{m: m, block: name, scope: st.Key, form: form}
		rows = append(rows, widget.Expanded(widget.Scroll(widget.Padding(v.node(u.GetTree(), false), 10, 12)).Bar(skin.Surface3)))
	} else if u.GetError() == "" {
		rows = append(rows, widget.Expanded(emptyNote("loader-circle", T("Starting…"))))
	} else {
		rows = append(rows, widget.Expanded(widget.Box()))
	}
	return widget.Column(rows...).Stretch()
}

// pluginError is the plugin's error over its tree: why it stopped, with a
// way to start it again, or why its newer version is not the one running.
func pluginError(m *model, id string, u *protocol.BlockUpdate) ggui.Widget {
	head := T("The plugin failed")
	tone := skin.DiffRemoved
	switch {
	case u.GetStale():
		head, tone = T("The plugin's newer version failed; the last that worked runs"), skin.DotWaiting
	case u.GetState() != protocol.PluginFailed:
		head = T("The block's last call failed")
	}
	text := widget.Column(
		widget.Text(head).Size(12).Weight(ggui.WeightMedium).Color(tone),
		muted(widget.Text(u.GetError()).Size(11.5).Font(ggui.DefaultMonoFont())),
	).Gap(3).Stretch()
	row := []ggui.Widget{icon("shield-alert").Size(15).Color(tone), widget.Expanded(text)}
	if u.GetState() == protocol.PluginFailed {
		row = append(row, ui.Button(T("Restart"), func() { m.actOnPlugin(&protocol.PluginAction{PluginId: id, Op: protocol.PluginEnable}) }).Outline())
	}
	return widget.Column(widget.Box(widget.Row(row...).Gap(8)).Pad(8, 10), hline()).Stretch()
}

// pluginNote says how a plugin stands, with what to do about it.
func pluginNote(text string, action ggui.Widget) ggui.Widget {
	return widget.Center(widget.Column(
		icon("package").Size(22).Color(skin.ExtraMuted),
		muted(widget.Text(text).Size(12.5)),
		action,
	).Gap(10).Align(widget.AlignCenter))
}

// pluginAsks is what a plugin asks for, in its block's place, to allow
// here: drawn by the window, not the plugin, which is not running.
func pluginAsks(m *model, p *protocol.PluginInfo) ggui.Widget {
	if p == nil {
		return emptyNote("package", T("Waiting for the plugin to be listed…"))
	}
	rows := []ggui.Widget{
		widget.Text(Tf("%s asks to", p.GetId())).Size(13).Weight(ggui.WeightMedium),
	}
	for _, perm := range p.GetAsked() {
		rows = append(rows, widget.Row(icon("check").Size(13).Color(skin.Muted),
			widget.Expanded(widget.Text(permissionText(perm)).Size(12.5)),
			chip(perm)).Gap(8).Align(widget.AlignCenter))
	}
	id := p.GetId()
	rows = append(rows, widget.Box().Height(4), widget.Row(
		widget.Spacer(),
		ui.Button(T("Deny"), func() { m.actOnPlugin(&protocol.PluginAction{PluginId: id, Op: protocol.PluginDeny}) }).Outline(),
		ui.Button(T("Allow"), func() { m.actOnPlugin(&protocol.PluginAction{PluginId: id, Op: protocol.PluginGrant}) }),
	).Gap(8))
	return widget.Padding(widget.Column(rows...).Gap(8).Stretch(), 12, 14)
}
