package gui

import (
	"context"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/gui/i18n"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// activeBlockTrees tracks the last full UiNode tree rendered for each live
// (model, block, scope, instance) stream so GUI tests can inspect rendered
// plugin trees without modifying model.go. Entries are cleared when the
// block's Effect ends.
type blockTreeKey struct {
	model    *model
	block    string
	scope    string
	instance string
}

type blockTreeEntry struct {
	tree *protocol.UiNode
	gen  uint64
}

var (
	activeBlockTreesMu sync.RWMutex
	activeBlockTrees   = map[blockTreeKey]blockTreeEntry{}
	blockTreeGen       atomic.Uint64
	// watchBlockFunc overrides Client.WatchBlockView in unit tests when non-nil.
	watchBlockFunc func(ctx context.Context, block, scope, locale string, standalone bool, fn func(*protocol.BlockUpdate)) error
	// watchBlockRequestFunc overrides Client.WatchBlockRequest in unit tests when non-nil.
	watchBlockRequestFunc func(ctx context.Context, req *protocol.WatchBlock, fn func(*protocol.BlockUpdate)) error
)

func recordBlockTree(m *model, block, scope string, tree *protocol.UiNode, gen uint64) {
	recordBlockTreeInstance(m, block, scope, "", tree, gen)
}

func recordBlockTreeInstance(m *model, block, scope, instance string, tree *protocol.UiNode, gen uint64) {
	if m == nil || block == "" {
		return
	}
	activeBlockTreesMu.Lock()
	defer activeBlockTreesMu.Unlock()
	k := blockTreeKey{model: m, block: block, scope: scope, instance: instance}
	if tree == nil {
		if cur, ok := activeBlockTrees[k]; ok && (gen == 0 || cur.gen == gen) {
			delete(activeBlockTrees, k)
		}
		return
	}
	if cur, ok := activeBlockTrees[k]; ok && gen != 0 && cur.gen > gen {
		return
	}
	activeBlockTrees[k] = blockTreeEntry{tree: tree, gen: gen}
}

func clearBlockTree(m *model, block, scope string, gen uint64) {
	clearBlockTreeInstance(m, block, scope, "", gen)
}

func clearBlockTreeInstance(m *model, block, scope, instance string, gen uint64) {
	if m == nil || block == "" {
		return
	}
	activeBlockTreesMu.Lock()
	k := blockTreeKey{model: m, block: block, scope: scope, instance: instance}
	if cur, ok := activeBlockTrees[k]; ok && (gen == 0 || cur.gen == gen) {
		delete(activeBlockTrees, k)
	}
	activeBlockTreesMu.Unlock()
}

// blockTreeFor returns the latest full UiNode tree rendered for block in this
// model. If an explicit non-empty scope is given, it matches that canonical
// scope key; otherwise it returns the active tree for block under any scope.
func (m *model) blockTreeFor(name string, scope ...string) *protocol.UiNode {
	if m == nil || name == "" {
		return nil
	}
	activeBlockTreesMu.RLock()
	defer activeBlockTreesMu.RUnlock()
	if len(scope) > 0 && scope[0] != "" {
		if e, ok := activeBlockTrees[blockTreeKey{model: m, block: name, scope: scope[0]}]; ok && e.tree != nil {
			return e.tree
		}
		for k, e := range activeBlockTrees {
			if k.model == m && k.block == name && k.scope == scope[0] && e.tree != nil {
				return e.tree
			}
		}
		return nil
	}
	if e, ok := activeBlockTrees[blockTreeKey{model: m, block: name, scope: ""}]; ok && e.tree != nil {
		return e.tree
	}
	for k, e := range activeBlockTrees {
		if k.model == m && k.block == name && e.tree != nil {
			return e.tree
		}
	}
	return nil
}

type blockCacheKey struct {
	block    string
	scope    string
	instance string
}

type cachedBlockState struct {
	feed blockFeed
	form *blockForm
}

func (m *model) cachedBlock(name, scope string) *cachedBlockState {
	return m.cachedBlockInstance(name, scope, "")
}

func (m *model) cachedBlockInstance(name, scope, instance string) *cachedBlockState {
	if m == nil || m.plugins == nil || name == "" {
		return nil
	}
	return m.plugins.blockCache[blockCacheKey{block: name, scope: scope, instance: instance}]
}

func (m *model) saveCachedBlock(name, scope string, feed blockFeed, form *blockForm) {
	m.saveCachedBlockInstance(name, scope, "", feed, form)
}

func (m *model) saveCachedBlockInstance(name, scope, instance string, feed blockFeed, form *blockForm) {
	if m == nil || m.plugins == nil || name == "" {
		return
	}
	if m.plugins.blockCache == nil {
		m.plugins.blockCache = map[blockCacheKey]*cachedBlockState{}
	}
	m.plugins.blockCache[blockCacheKey{block: name, scope: scope, instance: instance}] = &cachedBlockState{
		feed: feed,
		form: form,
	}
}

func (m *model) deleteCachedBlock(name, scope string) {
	m.deleteCachedBlockInstance(name, scope, "")
}

func (m *model) deleteCachedBlockInstance(name, scope, instance string) {
	if m == nil || m.plugins == nil || name == "" {
		return
	}
	delete(m.plugins.blockCache, blockCacheKey{block: name, scope: scope, instance: instance})
}

func (m *model) evictCachedBlock(name string) {
	if m == nil || m.plugins == nil || name == "" {
		return
	}
	for k := range m.plugins.blockCache {
		if k.block == name {
			delete(m.plugins.blockCache, k)
		}
	}
	if name == gitTab {
		m.evictCachedBlock(gitPluginBlock)
	}
}

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
	return scope == protocol.ScopeSpace || scope == protocol.ScopeProject || scope == protocol.ScopeSession || scope == protocol.ScopeFolder
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
		if pt, ok := m.PluginTabs.Get()[tabID]; ok {
			if pt.Root.Path != "" {
				if pl := placeIn(p.Projects, pt.Root.Node, pt.Root.Path, "", ""); pl.At >= 0 {
					return protocol.ScopeProject + ":" + p.Projects[pl.At].ID
				}
			}
			if rest, ok := strings.CutPrefix(pt.Scope, protocol.ScopeProject+":"); ok && rest != "" {
				return pt.Scope
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

	case protocol.ScopeFolder:
		r := m.repoFolder()
		if r.Path == "" {
			return ""
		}
		return protocol.FolderScope(m.remoteID(r.Node), r.Path)
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
	case protocol.ScopeFolder:
		if roots := m.Roots.Get().Roots; len(roots) > 0 {
			for _, r := range roots {
				if r.Path == "" {
					continue
				}
				key := protocol.FolderScope(m.remoteID(r.Node), r.Path)
				if key == "" {
					continue
				}
				label := r.Label
				if label == "" {
					label = projectName(r.Path)
				}
				out = append(out, scopeOption{Key: key, Label: label})
			}
			break
		}
		seen := map[string]bool{}
		for _, proj := range m.spaces().Projects {
			for _, r := range proj.Roots {
				for _, loc := range r.Locations {
					if loc.Path == "" {
						continue
					}
					key := protocol.FolderScope(m.remoteID(loc.Node), loc.Path)
					if key == "" || seen[key] {
						continue
					}
					seen[key] = true
					label := r.label()
					if label == "" {
						label = projectName(loc.Path)
					}
					out = append(out, scopeOption{Key: key, Label: label})
				}
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
	case protocol.ScopeFolder:
		node, path, ok := protocol.ParseFolderScope(scopeKey)
		if !ok {
			return id
		}
		for _, r := range m.Roots.Get().Roots {
			if r.Path == path && (m.remoteID(r.Node) == node || (node == protocol.LocalScopeNode && m.remoteID(r.Node) == "")) {
				if r.Label != "" {
					return r.Label
				}
			}
		}
		return projectName(path)
	}
	return id
}

// activeLocale returns the active GUI locale (BCP-47 tag), reacting to m.Prefs
// language changes while also honoring direct i18n.Set overrides in tests.
func (m *model) activeLocale() string {
	p := m.Prefs.Get()
	if p.Language != "" && p.Language != "system" {
		return i18n.Resolve(p.Language)
	}
	return i18n.Current()
}

// currentLocale returns the active GUI locale without subscribing to m.Prefs.
func (m *model) currentLocale() string {
	p := ggui.Peek(m.Prefs)
	if p.Language != "" && p.Language != "system" {
		return i18n.Resolve(p.Language)
	}
	return i18n.Current()
}

// pluginBlock follows the block of the name, of plugin id, while it is
// drawn in standalone card-framed contexts (previews, store, compact views).
func pluginBlock(m *model, name, id, title, blockScope string, props map[string]any) ggui.Widget {
	return treeBlock(m, name, blockOrigin{plugin: id}, title, blockScope, props)
}

// pluginBlockView follows a plugin block inside layout chrome, returning a
// blockView whose Tools, Accessory, Menu, and TitleInTools integrate directly
// with the solo dock header or tabbed stack bar.
func pluginBlockView(m *model, name, id, title, blockScope string, props map[string]any, at blockPlace) blockView {
	return treeBlockView(m, name, blockOrigin{plugin: id}, title, blockScope, props, at)
}

// pluginUpdateShowsTree reports whether u is in a state that renders its tree
// (rather than a waiting/denied/blocked/disabled notice).
func pluginUpdateShowsTree(u *protocol.BlockUpdate) bool {
	if u == nil || u.GetGone() || u.GetTree() == nil {
		return false
	}
	switch u.GetState() {
	case protocol.PluginWaiting, protocol.PluginDenied, protocol.PluginBlocked, protocol.PluginDisabled:
		return false
	}
	return true
}

// provenanceBadge renders the non-droppable provenance indicator for third-party
// plugins (package glyph + plugin id chip with "Plugin · <id>" tooltip) and for
// agent blocks (bot glyph + agent · session chip). Bundled first-party plugins
// return nil.
func provenanceBadge(m *model, origin blockOrigin) ggui.Widget {
	if origin.plugin != "" {
		if m.isBundledPlugin(origin.plugin) {
			return nil
		}
		tip := Tf("Plugin · %s", origin.plugin)
		badge := widget.Row(
			icon("package").Size(12).Color(skin.Muted),
			chip(origin.plugin),
		).Gap(4).Align(widget.AlignCenter)
		return ui.Tooltip(&semLabeled{child: badge, label: tip}, tip)
	}
	who := cmpTone(origin.agent, T("Agent")) + " · " + short(origin.session)
	return widget.Row(
		icon("bot").Size(12).Color(skin.Muted),
		chip(who),
	).Gap(4).Align(widget.AlignCenter)
}

// treeBlockView follows a plugin or agent UI tree block and returns a blockView
// integrated with layout chrome: Body has no outer card frame, a leading
// variant:"toolbar" row is lifted into Tools, Accessory holds the scope chip
// and third-party/agent provenance badge, Menu provides Reload and Pin scope,
// and at.Solo is propagated to WatchBlock.Standalone.
func treeBlockView(m *model, name string, origin blockOrigin, title, blockScope string, props map[string]any, at blockPlace) blockView {
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
	initScope := ggui.Untrack(resolved.Get)
	var initFeed blockFeed
	var form *blockForm
	if cached := m.cachedBlock(name, initScope); cached != nil && cached.form != nil {
		initFeed = cached.feed
		form = cached.form
	} else {
		initFeed = blockFeed{Scope: initScope}
		form = newBlockForm()
		m.saveCachedBlock(name, initScope, initFeed, form)
	}
	feed := ggui.State(initFeed)
	solo := ggui.State(at.Solo)

	currentUpdate := func() *protocol.BlockUpdate {
		f := feed.Get()
		scopeKey := resolved.Get()
		if f.Scope == scopeKey {
			return f.Update
		}
		return nil
	}

	showScopeChip := origin.plugin != "" && isScoped(blockScope) && name != gitPluginBlock
	thirdParty := origin.plugin != "" && !m.isBundledPlugin(origin.plugin)
	isAgent := origin.plugin == ""
	hasStaticAccessory := showScopeChip || thirdParty || isAgent

	body := ggui.Component(func() ggui.Widget {
		ggui.Effect(func() ggui.Cleanup {
			scopeKey := resolved.Get()
			locale := m.activeLocale()
			isSolo := solo.Get()
			gen := blockTreeGen.Add(1)
			if ggui.Peek(feed).Scope != scopeKey {
				if cached := m.cachedBlock(name, scopeKey); cached != nil && cached.feed.Scope == scopeKey {
					feed.Set(cached.feed)
					form.reset()
					if cached.form != nil && cached.form != form {
						form.copyFrom(cached.form)
					}
					m.saveCachedBlock(name, scopeKey, cached.feed, form)
				} else {
					next := blockFeed{Scope: scopeKey}
					feed.Set(next)
					form.reset()
					m.saveCachedBlock(name, scopeKey, next, form)
				}
			}
			if isScoped(blockScope) && scopeKey == "" {
				clearBlockTree(m, name, scopeKey, 0)
				return nil // empty-scope: do not open a stream
			}
			if cur := ggui.Peek(feed); cur.Scope == scopeKey && cur.Update != nil && cur.Update.GetTree() != nil {
				recordBlockTree(m, name, scopeKey, cur.Update.GetTree(), gen)
			}
			if m.post == nil && watchBlockFunc == nil {
				return nil // not started, as under a test without a watch hook
			}
			post := m.post
			if post == nil {
				post = func(fn func()) { fn() }
			}
			ctx, cancel := context.WithCancel(context.Background())
			go m.followBlockScoped(ctx, name, scopeKey, locale, isSolo, func(u *protocol.BlockUpdate) {
				post(func() {
					if ctx.Err() != nil {
						return
					}
					recordBlockTree(m, name, scopeKey, u.GetTree(), gen)
					var next blockFeed
					feed.Update(func(f blockFeed) blockFeed {
						next = blockFeed{Update: u, Scope: scopeKey, Seq: f.Seq + 1}
						return next
					})
					if u.GetGone() {
						m.deleteCachedBlock(name, scopeKey)
					} else {
						m.saveCachedBlock(name, scopeKey, next, form)
					}
				})
			})
			return func() {
				cancel()
				clearBlockTree(m, name, scopeKey, gen)
			}
		})
		return ggui.Reactive(func() ggui.Widget {
			scopeKey := resolved.Get()
			u := currentUpdate()
			st := blockScopeState{BlockScope: blockScope, Pinned: pinned, Key: scopeKey}
			return blockBodyContent(m, name, origin, st, u, form, true)
		})
	})

	tools := ggui.Reactive(func() ggui.Widget {
		u := currentUpdate()
		if !pluginUpdateShowsTree(u) {
			return widget.Box()
		}
		tree := u.GetTree()
		tb, _ := liftToolbar(tree)
		if tb == nil {
			return widget.Box()
		}
		return renderTreeNode(m, name, resolved.Get(), origin.plugin, form, tree, tb)
	})

	var accessory ggui.Widget
	if hasStaticAccessory {
		accessory = ggui.Reactive(func() ggui.Widget {
			var parts []ggui.Widget
			if showScopeChip {
				st := blockScopeState{BlockScope: blockScope, Pinned: pinned, Key: resolved.Get()}
				parts = append(parts, blockScopeChip(m, name, st))
			}
			if badge := provenanceBadge(m, origin); badge != nil {
				parts = append(parts, badge)
			}
			if len(parts) == 0 {
				return widget.Box()
			}
			if len(parts) == 1 {
				return parts[0]
			}
			return widget.Row(parts...).Gap(6).Align(widget.AlignCenter)
		})
	}

	hasTools := ggui.Derived(func() bool {
		if hasStaticAccessory {
			return true
		}
		u := currentUpdate()
		if !pluginUpdateShowsTree(u) {
			return false
		}
		tb, _ := liftToolbar(u.GetTree())
		return tb != nil
	})

	titleInToolsOf := ggui.Derived(func() bool {
		u := currentUpdate()
		if !pluginUpdateShowsTree(u) {
			return false
		}
		return u.GetChrome() == "toolbar"
	})

	menu := func() []ggui.Widget {
		var items []ggui.Widget
		if origin.plugin != "" {
			pid := origin.plugin
			if p := m.pluginOf(pid); p != nil && !p.GetInstalled() && !p.GetBundled() {
				items = append(items, ui.MenuItem(T("Reload block"), func() {
					m.actOnPlugin(&protocol.PluginAction{PluginId: pid, Op: protocol.PluginReload})
				}))
			}
			if showScopeChip {
				st := blockScopeState{BlockScope: blockScope, Pinned: pinned, Key: ggui.Untrack(resolved.Get)}
				items = append(items, ui.MenuSub(T("Pin scope"), blockScopeMenuItems(m, name, st)...))
			}
		}
		return items
	}

	return blockView{
		Body:           body,
		Tools:          tools,
		Accessory:      accessory,
		Menu:           menu,
		hasTools:       hasTools,
		titleInToolsOf: titleInToolsOf,
	}
}

// treeBlock follows a block of a tree while it is drawn inside the standalone
// card frame (for contexts without layout chrome such as previews and tests).
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
			locale := m.activeLocale()
			if ggui.Peek(feed).Scope != scopeKey {
				feed.Set(blockFeed{Scope: scopeKey})
				form.reset()
			}
			if isScoped(blockScope) && scopeKey == "" {
				return nil // empty-scope: do not open a stream
			}
			if m.post == nil && watchBlockFunc == nil {
				return nil // not started, as under a test
			}
			post := m.post
			if post == nil {
				post = func(fn func()) { fn() }
			}
			ctx, cancel := context.WithCancel(context.Background())
			gen := blockTreeGen.Add(1)
			go m.followBlockScoped(ctx, name, scopeKey, locale, false, func(u *protocol.BlockUpdate) {
				post(func() {
					if ctx.Err() != nil {
						return
					}
					recordBlockTree(m, name, scopeKey, u.GetTree(), gen)
					feed.Update(func(f blockFeed) blockFeed {
						return blockFeed{Update: u, Scope: scopeKey, Seq: f.Seq + 1}
					})
				})
			})
			return func() {
				cancel()
				clearBlockTree(m, name, scopeKey, gen)
			}
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

// followBlockScoped watches a block on the daemon under scope, locale, and
// standalone view flag, applying each update to the tree it holds and handing
// on the whole, until ctx ends.
func (m *model) followBlockScoped(ctx context.Context, name, scope, locale string, standalone bool, fn func(*protocol.BlockUpdate)) {
	m.followBlockInstance(ctx, name, scope, locale, standalone, "", "", fn)
}

// followBlockInstance watches a block instance (such as a custom plugin tab
// with an instance key and params JSON) on the daemon.
func (m *model) followBlockInstance(ctx context.Context, name, scope, locale string, standalone bool, instance, params string, fn func(*protocol.BlockUpdate)) {
	req := &protocol.WatchBlock{
		Block:      name,
		Scope:      scope,
		Locale:     locale,
		Standalone: standalone,
		Instance:   instance,
		Params:     params,
	}
	followStream(ctx, func(ctx context.Context, each func(*protocol.BlockUpdate)) error {
		if watchBlockRequestFunc != nil {
			return watchBlockRequestFunc(ctx, req, each)
		}
		if watchBlockFunc != nil {
			return watchBlockFunc(ctx, name, scope, locale, standalone, each)
		}
		c, err := m.link.client(ctx)
		if err != nil {
			return err
		}
		return c.WatchBlockRequest(ctx, req, each)
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
	const (
		lostBaseDelay = 50 * time.Millisecond
		lostMaxDelay  = 2 * time.Second
	)
	lostStreak := 0
	for ctx.Err() == nil {
		started := time.Now()
		var tree *protocol.UiNode
		var chrome string
		stream, restart := context.WithCancel(ctx)
		lost := false
		err := watch(stream, func(u *protocol.BlockUpdate) {
			if lost {
				return
			}
			if c := u.GetChrome(); c != "" {
				chrome = c
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
				Error: u.GetError(), Stale: u.GetStale(), Gone: u.GetGone(), Scope: u.GetScope(), Chrome: chrome,
				Instance: u.GetInstance()}
			fn(whole)
		})
		restart()
		if lost {
			if time.Since(started) >= time.Second {
				lostStreak = 0
			}
			var delay time.Duration
			if lostStreak > 0 {
				delay = min(lostBaseDelay<<min(lostStreak-1, 6), lostMaxDelay)
			}
			lostStreak++
			if delay == 0 {
				continue
			}
			t := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
			continue
		}
		lostStreak = 0
		if err != nil && ctx.Err() == nil && tree == nil {
			fn(&protocol.BlockUpdate{State: protocol.PluginFailed, Error: err.Error()})
		}
		t := time.NewTimer(retry)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// pluginFrame is a plugin's block's frame, for a tree drawn once.
func pluginFrame(m *model, name, id, title string, u *protocol.BlockUpdate) ggui.Widget {
	return blockFrame(m, name, blockOrigin{plugin: id}, title, blockScopeState{}, u, newBlockForm())
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
		if p := m.pluginOf(pid); p != nil && !p.GetInstalled() && !p.GetBundled() {
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
	case protocol.ScopeProject, protocol.ScopeFolder:
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
	case protocol.ScopeFolder:
		return T("Follow active folder")
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
	case protocol.ScopeFolder:
		return T("No folder")
	default:
		return ""
	}
}

// blockScopeMenuItems returns the menu items for switching between Follow
// active and Pin to a target (shared by blockScopeChip and Pin scope submenus).
func blockScopeMenuItems(m *model, name string, st blockScopeState) []ggui.Widget {
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
		case protocol.ScopeFolder:
			none = T("No folders available")
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
	return items
}

// blockScopeChip renders the compact clickable scope chip in a scoped block's
// header, letting the user switch between Follow active and Pin to a target.
func blockScopeChip(m *model, name string, st blockScopeState) ggui.Widget {
	items := blockScopeMenuItems(m, name, st)

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
	case protocol.ScopeFolder:
		title = T("Select a folder to view this block")
		hint = T("Open a session to see its changes.")
		pinLabel = T("Pin to a folder")
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
	if name != gitPluginBlock {
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
	}
	return widget.Center(widget.Box(widget.Column(rows...).Gap(6).Align(widget.AlignCenter)).
		Pad(18, 22).Fill(skin.Surface1).Border(1, skin.Border).Radius(10).MaxWidth(340))
}

// blockBody is what goes in the frame (without lifting a leading toolbar).
func blockBody(m *model, name string, origin blockOrigin, st blockScopeState, u *protocol.BlockUpdate, form *blockForm) ggui.Widget {
	return blockBodyContent(m, name, origin, st, u, form, false)
}

// blockBodyContent renders the block's inner body; when liftTB is true, a
// leading toolbar row is stripped because the layout chrome draws it in Tools.
func blockBodyContent(m *model, name string, origin blockOrigin, st blockScopeState, u *protocol.BlockUpdate, form *blockForm, liftTB bool) ggui.Widget {
	return blockBodyContentInstance(m, name, "", origin, st, u, form, liftTB)
}

func blockBodyContentInstance(m *model, name, instance string, origin blockOrigin, st blockScopeState, u *protocol.BlockUpdate, form *blockForm, liftTB bool) ggui.Widget {
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
	if tree := u.GetTree(); tree != nil {
		form.redraw()
		renderRoot := tree
		lifted := false
		if liftTB {
			if tb, rest := liftToolbar(tree); tb != nil {
				renderRoot = rest
				lifted = true
			}
		}
		v := newTreeView(m, name, st.Key, form, tree)
		v.plugin = origin.plugin
		v.instance = instance
		if tree.GetFill() {
			content := v.node(renderRoot, false)
			if !(lifted && u.GetChrome() == "toolbar") {
				content = widget.Padding(content, 10, 12)
			}
			rows = append(rows, widget.Expanded(content))
		} else {
			rows = append(rows, widget.Expanded(widget.Scroll(widget.Padding(v.node(renderRoot, false), 10, 12)).Bar(skin.Surface3)))
		}
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
