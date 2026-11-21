package gui

import (
	"context"
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
	Seq    int                   // counts the feeds, so each is new
}

// blockOrigin is where a block comes from, which its frame names: a
// plugin, or an agent's session.
type blockOrigin struct {
	plugin         string
	session, agent string
}

// pluginBlock follows the block of the name, of plugin id, while it is
// drawn; title is its title as listed, until the daemon sends one.
func pluginBlock(m *model, name, id, title string) ggui.Widget {
	return treeBlock(m, name, blockOrigin{plugin: id}, title)
}

// treeBlock follows a block of a tree while it is drawn.
func treeBlock(m *model, name string, origin blockOrigin, title string) ggui.Widget {
	return ggui.Component(func() ggui.Widget {
		feed := ggui.State(blockFeed{})
		form := newBlockForm()
		ggui.Effect(func() ggui.Cleanup {
			if m.post == nil {
				return nil // not started, as under a test
			}
			ctx, cancel := context.WithCancel(context.Background())
			go m.followBlock(ctx, name, func(u *protocol.BlockUpdate) {
				m.post(func() { feed.Update(func(f blockFeed) blockFeed { return blockFeed{Update: u, Seq: f.Seq + 1} }) })
			})
			return cancel
		})
		return ggui.View(feed, func(f blockFeed) ggui.Widget { return blockFrame(m, name, origin, title, f.Update, form) })
	})
}

// followBlock watches a block on the daemon, applying each update to the
// tree it holds and handing on the whole, until ctx ends.
func (m *model) followBlock(ctx context.Context, name string, fn func(*protocol.BlockUpdate)) {
	followStream(ctx, func(ctx context.Context, each func(*protocol.BlockUpdate)) error {
		c, err := m.link.client(ctx)
		if err != nil {
			return err
		}
		return c.WatchBlock(ctx, name, each)
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
		_ = watch(stream, func(u *protocol.BlockUpdate) {
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
				Error: u.GetError(), Stale: u.GetStale(), Gone: u.GetGone()}
			fn(whole)
		})
		restart()
		if lost {
			continue
		}
		select {
		case <-ctx.Done():
		case <-time.After(retry):
		}
	}
}

// pluginFrame is a plugin's block's frame, for a tree drawn once.
func pluginFrame(m *model, name, id, title string, u *protocol.BlockUpdate) ggui.Widget {
	return blockFrame(m, name, blockOrigin{plugin: id}, title, u, newBlockForm())
}

// blockFrame is a block's frame: a strip naming it and where it comes
// from over what it draws, or how its plugin stands.
func blockFrame(m *model, name string, origin blockOrigin, title string, u *protocol.BlockUpdate, form *blockForm) ggui.Widget {
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
		muted(widget.Text(kind).Size(11).NoWrap()),
		chip(who),
	}
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
		widget.Expanded(blockBody(m, name, origin, u, form)),
	).Stretch()).Border(1, skin.Border).Radius(8)).Pad(6).Fill(skin.Surface0)
}

// blockBody is what goes in the frame.
func blockBody(m *model, name string, origin blockOrigin, u *protocol.BlockUpdate, form *blockForm) ggui.Widget {
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
		v := &treeView{m: m, block: name, form: form}
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
