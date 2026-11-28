package gui

import (
	"cmp"
	"context"
	"image/color"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// Custom workspace tabs contributed by plugins (revision 49).
//
// Any plugin block whose manifest includes "tab" in its slots can be opened as
// a first-class workspace tab alongside sessions, files, and diffs: supporting
// multiple independent instances (by key + params), preview/keep double-click,
// split panes, drag-and-drop, project grouping, and detached/space windows.

const pluginTabPrefix = "ptab\x00"

// isPluginTab reports whether a workspace tab renders a plugin block.
func isPluginTab(id string) bool { return strings.HasPrefix(id, pluginTabPrefix) }

// pluginTabID builds the canonical tab ID for (block, key).
func pluginTabID(block, key string) string {
	if key == "" {
		key = "default"
	}
	return pluginTabPrefix + block + "\x00" + key
}

// pluginTab describes one open custom plugin tab in the workspace.
type pluginTab struct {
	Block    string   // registered block name: plugin:<id>/<block>
	PluginID string   // owning plugin ID
	Key      string   // instance key within block (e.g. "default", "pr-42")
	Title    string   // tab title
	Icon     string   // bundled icon name
	Scope    string   // canonical scope key (optional)
	Params   string   // JSON object params passed to ctx.tab.params (optional)
	Root     fileRoot // folder root when opened from a folder scope (for project grouping & sidebar sync)
	Preview  bool     // single-click preview (italics); next preview replaces until kept
}

// Name returns the tab's display title.
func (t pluginTab) Name() string {
	return cmp.Or(t.Title, t.Block)
}

func pluginIDFromBlock(block string) string {
	if rest, ok := strings.CutPrefix(block, "plugin:"); ok {
		id, _, _ := strings.Cut(rest, "/")
		return id
	}
	return ""
}

func (m *model) lookupPluginBlock(block string) (*protocol.PluginInfo, *protocol.PluginBlock) {
	for _, p := range ggui.Peek(m.plugins.List) {
		for _, b := range p.GetBlocks() {
			if b.GetName() == block {
				return p, b
			}
		}
	}
	return nil, nil
}

// openPluginTab opens (or focuses/updates) a plugin tab in the workspace.
// Preview tabs replace the previous preview plugin tab unless already open.
func (m *model) openPluginTab(pt pluginTab) {
	m.openPluginTabIn(nil, pt)
}

// openPluginTabIn opens (or focuses/updates) a plugin tab in sw (or the main
// workspace when sw is nil).
func (m *model) openPluginTabIn(sw *spaceWindowState, pt pluginTab) {
	if pt.Block == "" {
		return
	}
	if pt.Key == "" {
		pt.Key = "default"
	}
	if pt.PluginID == "" {
		pt.PluginID = pluginIDFromBlock(pt.Block)
	}
	if p, b := m.lookupPluginBlock(pt.Block); b != nil {
		if pt.PluginID == "" && p != nil {
			pt.PluginID = p.GetId()
		}
		if pt.Title == "" {
			pt.Title = b.GetTitle()
		}
		if pt.Icon == "" {
			pt.Icon = b.GetIcon()
		}
		if pt.Scope == "" && isScoped(b.GetScope()) {
			pt.Scope = ggui.Untrack(func() string { return m.resolveActiveScopeIn(sw, b.GetScope()) })
		}
	}
	if pt.Icon == "" {
		pt.Icon = "package"
	}
	if pt.Root.Path == "" && strings.HasPrefix(pt.Scope, protocol.ScopeFolder+":") {
		if node, folder, ok := protocol.ParseFolderScope(pt.Scope); ok {
			pt.Root = fileRoot{Node: m.pluginIntentNode(node), Path: folder}
		}
	}

	id := pluginTabID(pt.Block, pt.Key)
	var targetTabs []string
	if sw != nil {
		for _, g := range m.peekLayoutForKey(sw.peekLayoutKey()).groups() {
			targetTabs = append(targetTabs, g.Tabs...)
		}
	} else {
		targetTabs = ggui.Peek(m.Tabs)
	}
	var replaced string
	if !slices.Contains(targetTabs, id) && pt.Preview {
		for other, cur := range ggui.Peek(m.PluginTabs) {
			if cur.Preview && slices.Contains(targetTabs, other) {
				replaced = other
				break
			}
		}
	}
	m.PluginTabs.Update(func(v map[string]pluginTab) map[string]pluginTab {
		v = maps.Clone(v)
		if existing, ok := v[id]; ok {
			if !pt.Preview {
				pt.Preview = false
			} else {
				pt.Preview = existing.Preview
			}
			if pt.Title == "" {
				pt.Title = existing.Title
			}
			if pt.Params == "" {
				pt.Params = existing.Params
			}
			if pt.Scope == "" {
				pt.Scope = existing.Scope
			}
			if pt.Root.Path == "" {
				pt.Root = existing.Root
			}
		}
		v[id] = pt
		return v
	})
	if sw != nil {
		if sw.sidebarPage != nil && ggui.Peek(sw.sidebarPage) != "" {
			sw.sidebarPage.Set("")
		}
		key := sw.peekLayoutKey()
		if replaced != "" {
			var replacedTab bool
			focus := m.editLayoutForKey(key, func(root *layoutNode) int {
				g := root.groupOf(replaced)
				if g == nil {
					return 0
				}
				i := slices.Index(g.Tabs, replaced)
				g.Tabs[i] = id
				g.Active = id
				replacedTab = true
				return g.ID
			})
			if !replacedTab {
				focus = m.activateTabInKey(key, ggui.Peek(sw.focusGroup), id)
			}
			if sw.focusGroup != nil {
				sw.focusGroup.Set(focus)
			}
			if !slices.Contains(m.allTabs(), replaced) {
				m.PluginTabs.Update(func(v map[string]pluginTab) map[string]pluginTab {
					if _, has := v[replaced]; !has {
						return v
					}
					v = maps.Clone(v)
					delete(v, replaced)
					return v
				})
			}
		} else {
			focus := m.activateTabInKey(key, ggui.Peek(sw.focusGroup), id)
			if sw.focusGroup != nil {
				sw.focusGroup.Set(focus)
			}
		}
		return
	}
	if replaced != "" {
		m.replaceTab(replaced, id)
		if !slices.Contains(m.allTabs(), replaced) {
			m.PluginTabs.Update(func(v map[string]pluginTab) map[string]pluginTab {
				if _, has := v[replaced]; !has {
					return v
				}
				v = maps.Clone(v)
				delete(v, replaced)
				return v
			})
		}
	}
	m.showTab(id)
}

// keepPluginTab pins a preview plugin tab so subsequent previews open beside it.
func (m *model) keepPluginTab(id string) {
	m.PluginTabs.Update(func(v map[string]pluginTab) map[string]pluginTab {
		pt, ok := v[id]
		if !ok || !pt.Preview {
			return v
		}
		v = maps.Clone(v)
		pt.Preview = false
		v[id] = pt
		return v
	})
}

// closePluginTab closes a plugin tab and clears its state and cached tree.
func (m *model) closePluginTab(id string) {
	m.closePluginTabIn(nil, id)
}

// closePluginTabIn closes a plugin tab in sw (or the main workspace when sw is
// nil) and clears its state and cached tree once no layout holds it.
func (m *model) closePluginTabIn(sw *spaceWindowState, id string) {
	pt, ok := ggui.Peek(m.PluginTabs)[id]
	if sw != nil {
		m.closeTabInKey(sw.peekLayoutKey(), id)
	} else {
		m.closeTab(id)
	}
	if !slices.Contains(m.allTabs(), id) {
		m.PluginTabs.Update(func(v map[string]pluginTab) map[string]pluginTab {
			if _, has := v[id]; !has {
				return v
			}
			v = maps.Clone(v)
			delete(v, id)
			return v
		})
		if ok {
			m.deleteCachedBlockInstance(pt.Block, pt.Scope, pt.Key)
		}
	}
}

// closePluginTabByKeyIn closes the plugin tab matching (block, key) in sw (or
// the main workspace when sw is nil).
func (m *model) closePluginTabByKeyIn(sw *spaceWindowState, block, key string) {
	m.closePluginTabIn(sw, pluginTabID(block, key))
}

// pluginTabFace draws a custom plugin tab in a pane's tab bar.
func pluginTabFace(m *model, id string, pt pluginTab, selected, focused bool) ggui.Widget {
	label := pt.Name()
	fg := pick[color.Color](selected, skin.Fg, skin.Muted)
	glyph := pluginIcon(cmp.Or(pt.Icon, "package")).Size(14).Color(fg)
	return ui.EditorTab(label, func() {
		if last := m.lastTap; last.id == id && time.Since(last.at) < doubleTap {
			m.keepPluginTab(id)
		}
		m.lastTap = tabTap{id, time.Now()}
		m.showTab(id)
	}).Icon(glyph).MaxLabelWidth(190).
		Status(nil).Selected(selected).Current(focused).Preview(pt.Preview).
		OnClose(func() { m.closePluginTab(id) }).CloseName(Tf("Close %s", label))
}

// pluginTabView renders a custom workspace plugin tab's live UI tree stream.
func pluginTabView(m *model, id string) ggui.Widget {
	return pluginTabViewIn(m, id, nil)
}

// pluginTabViewIn renders a custom workspace plugin tab's live UI tree stream,
// routing session/tab intents to sw when hosted inside a Space window.
func pluginTabViewIn(m *model, id string, sw *spaceWindowState) ggui.Widget {
	return ggui.Component(func() ggui.Widget {
		tabSig := ggui.Map(m.PluginTabs, func(v map[string]pluginTab) pluginTab { return v[id] }).WithEqual(func(a, b pluginTab) bool {
			return a == b
		})
		initTab := ggui.Untrack(tabSig.Get)
		var initFeed blockFeed
		var form *blockForm
		if cached := m.cachedBlockInstance(initTab.Block, initTab.Scope, initTab.Key); cached != nil && cached.form != nil {
			initFeed = cached.feed
			form = cached.form
		} else {
			initFeed = blockFeed{Scope: initTab.Scope}
			form = newBlockForm()
			if initTab.Block != "" {
				m.saveCachedBlockInstance(initTab.Block, initTab.Scope, initTab.Key, initFeed, form)
			}
		}
		feed := ggui.State(initFeed)

		ggui.Effect(func() ggui.Cleanup {
			pt := tabSig.Get()
			if pt.Block == "" {
				return nil
			}
			locale := m.activeLocale()
			gen := blockTreeGen.Add(1)
			if m.post == nil && watchBlockFunc == nil && watchBlockRequestFunc == nil {
				return nil
			}
			post := m.post
			if post == nil {
				post = func(fn func()) { fn() }
			}
			ctx, cancel := context.WithCancel(context.Background())
			go m.followBlockInstance(ctx, pt.Block, pt.Scope, locale, true, pt.Key, pt.Params, func(u *protocol.BlockUpdate) {
				post(func() {
					if ctx.Err() != nil {
						return
					}
					recordBlockTreeInstance(m, pt.Block, pt.Scope, pt.Key, u.GetTree(), gen)
					var next blockFeed
					feed.Update(func(f blockFeed) blockFeed {
						next = blockFeed{Update: u, Scope: pt.Scope, Seq: f.Seq + 1}
						return next
					})
					if u.GetGone() {
						m.deleteCachedBlockInstance(pt.Block, pt.Scope, pt.Key)
					} else {
						m.saveCachedBlockInstance(pt.Block, pt.Scope, pt.Key, next, form)
					}
				})
			})
			return func() {
				cancel()
				clearBlockTreeInstance(m, pt.Block, pt.Scope, pt.Key, gen)
			}
		})

		return ggui.Reactive(func() ggui.Widget {
			pt := tabSig.Get()
			if pt.Block == "" {
				return widget.Box()
			}
			f := feed.Get()
			u := f.Update
			origin := blockOrigin{plugin: pt.PluginID}
			var blockScope string
			if _, decl := m.lookupPluginBlock(pt.Block); decl != nil {
				blockScope = decl.GetScope()
			}
			st := blockScopeState{BlockScope: blockScope, Pinned: pt.Scope, Key: pt.Scope}

			var rows []ggui.Widget
			var tb *protocol.UiNode
			if pluginUpdateShowsTree(u) {
				tb, _ = liftToolbar(u.GetTree())
			}
			badge := provenanceBadge(m, origin)
			if tb != nil || badge != nil {
				var bar []ggui.Widget
				if tb != nil {
					bar = append(bar, widget.Expanded(renderTreeNodeInstanceIn(m, pt.Block, pt.Scope, pt.Key, pt.PluginID, form, u.GetTree(), tb, sw)))
				} else {
					bar = append(bar, widget.Spacer())
				}
				if badge != nil {
					bar = append(bar, badge)
				}
				rows = append(rows,
					widget.Box(widget.Row(bar...).Gap(8).Align(widget.AlignCenter)).Pad(5, 10).Fill(skin.Surface1),
					hline(),
				)
			}
			body := blockBodyContentIn(m, pt.Block, pt.Key, origin, st, u, form, tb != nil, sw)
			rows = append(rows, widget.Expanded(widget.Box(body).Fill(skin.Surface0)))
			return widget.Column(rows...).Stretch()
		})
	})
}

// pluginTabHeaderBar renders the workspace header over an active plugin tab.
func pluginTabHeaderBar(m *model, id string, pt pluginTab) ggui.Widget {
	var items []ggui.Widget
	if pt.Preview {
		items = append(items, ui.MenuItem(T("Keep open"), func() { m.keepPluginTab(id) }))
	}
	if pt.PluginID != "" {
		pid := pt.PluginID
		if p := m.pluginOf(pid); p != nil && !p.GetInstalled() && !p.GetBundled() {
			items = append(items, ui.MenuItem(T("Reload plugin"), func() {
				m.actOnPlugin(&protocol.PluginAction{PluginId: pid, Op: protocol.PluginReload})
			}))
		}
	}
	if len(items) > 0 {
		items = append(items, ui.MenuDivider())
	}
	items = append(items, ui.MenuItem(T("Close tab"), func() { m.closePluginTab(id) }))

	row := []ggui.Widget{
		pluginIcon(cmp.Or(pt.Icon, "package")).Size(14).Color(skin.Muted),
		widget.Text(pt.Name()).Size(13.5).Weight(ggui.WeightMedium).NoWrap().Ellipsis(),
	}
	if pt.Root.Path != "" {
		host := nodeName(ggui.Peek(m.Nodes), pt.Root.Node)
		row = append(row, widget.Expanded(widget.Align(whereCrumb(host, prettyPath(pt.Root.Path))).Left()))
	} else {
		row = append(row, widget.Spacer())
	}
	if badge := provenanceBadge(m, blockOrigin{plugin: pt.PluginID}); badge != nil {
		row = append(row, badge)
	}
	row = append(row, iconMenu(ui.MenuOf(icon("ellipsis"), items...).Name(T("Tab actions")), 6))
	return widget.Row(row...).Gap(10).Align(widget.AlignCenter)
}
