package gui

import (
	"context"
	"strings"
	"time"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/prompttmpl"
	"github.com/ironpark/mako/internal/protocol"
)

// The composer's menu: a draft that begins with a slash, and has no space
// yet, lists the commands of the session's agent that it begins, or holds;
// one that begins with two lists the templates for it (templates.go). ↑
// and ↓ choose, Enter takes the one chosen and Tab completes the draft to
// it. The commands are those the session's node finds for its agent, read
// when a draft first begins with a slash, and again once they are a while
// old.

// commandMenu is the composer's list of the commands its draft matches.
type commandMenu struct {
	commands *ggui.StateValue[[]*protocol.Command]
	read     time.Time // when the commands were last asked for
	reading  bool
	pick     *ggui.StateValue[int] // the one chosen, of those matched
	// navigated is set once ↑ or ↓ moves the pick on the current draft.
	navigated bool
	// closed is the draft Escape closed the menu over; it opens again
	// once the draft changes.
	closed *ggui.StateValue[string]
}

func newCommandMenu() *commandMenu {
	return &commandMenu{
		commands: ggui.State[[]*protocol.Command](nil),
		pick:     ggui.State(0),
		closed:   ggui.State(""),
	}
}

// commandsStale is how old the commands are before a draft that begins
// with a slash asks for them again, for those written since.
const commandsStale = time.Minute

// maxMenuItems bounds what the menu lists.
const maxMenuItems = 8

// commandQuery is what draft asks of the commands: the name it begins,
// without its slash, and whether it asks at all.
func commandQuery(draft string) (string, bool) {
	if !strings.HasPrefix(draft, "/") || strings.ContainsAny(draft, " \t\n") {
		return "", false
	}
	return strings.ToLower(draft[1:]), true
}

// matchByName are the items whose names begin with query, then those that
// hold it elsewhere, each in the order given.
func matchByName[T any](items []T, query string, name func(T) string) []T {
	var begins, holds []T
	for _, it := range items {
		n := strings.ToLower(name(it))
		switch {
		case strings.HasPrefix(n, query):
			begins = append(begins, it)
		case strings.Contains(n, query):
			holds = append(holds, it)
		}
	}
	out := append(begins, holds...)
	if len(out) > maxMenuItems {
		out = out[:maxMenuItems]
	}
	return out
}

// menuItem is a line of the composer's menu: a command, template, or arg value.
type menuItem struct {
	name, usage, description, tag string
	complete                      string // the draft Tab completes to
	passEnter                     bool   // plain Enter submits when not navigated via arrows
	take                          func() // what Enter or a click does
}

// menuItems are what the composer's draft lists: the templates it calls,
// or the commands it begins; none while the menu is closed. Reactive.
func (m *model) menuItems(id string, state *chatState) []menuItem {
	draft := state.draft.Get()
	if draft == state.menu.closed.Get() {
		return nil
	}
	if _, ok := prompttmpl.ParseCall(draft); ok {
		return m.templateItems(id, state, draft)
	}
	query, ok := commandQuery(draft)
	if !ok {
		return nil
	}
	cmds := matchByName(state.menu.commands.Get(), query, (*protocol.Command).GetName)
	items := make([]menuItem, len(cmds))
	for i, c := range cmds {
		tag := ""
		if c.GetSource() != "" {
			tag = commandSource(c.GetSource())
		}
		items[i] = menuItem{
			name: "/" + c.GetName(), usage: c.GetArgs(), description: c.GetDescription(), tag: tag,
			complete: "/" + c.GetName() + " ", take: func() { m.takeCommand(id, c) },
		}
	}
	return items
}

// readCommands asks the session's node for its agent's commands, unless
// they were asked for lately.
func (m *model) readCommands(id string, state *chatState) {
	menu := state.menu
	if menu.reading || time.Since(menu.read) < commandsStale {
		return
	}
	info, ok := m.session(id)
	if !ok || !m.nodeHas(id, protocol.RevisionCommands) {
		return
	}
	menu.reading, menu.read = true, time.Now()
	req := &protocol.ListCommands{NodeId: m.remoteID(info.GetNodeId()), SessionId: id}
	fetch(m, 10*time.Second, func(ctx context.Context, c *client.Client) (*protocol.Commands, error) {
		return c.Commands(ctx, req)
	}, func(cmds *protocol.Commands, err error) {
		menu.reading = false
		if err == nil {
			menu.commands.Set(cmds.GetCommands())
		}
	})
}

// menuKey is the composer's keys while its menu lists anything: ↑ and ↓
// choose, Enter takes the one chosen, Tab completes the draft to it to
// write on, and Escape closes the menu. It reports whether it took the
// key.
func (m *model) menuKey(id string, state *chatState, ev ggui.KeyEvent) bool {
	items := m.menuKeys(id, state, ev)
	if len(items) == 0 {
		return false
	}
	if stepPick(state.menu.pick, len(items), ev.Key) {
		state.menu.navigated = true
		return true
	}
	pick := min(ggui.Peek(state.menu.pick), len(items)-1)
	switch ev.Key {
	case ggui.KeyEnter, ggui.KeyNumpadEnter:
		if items[pick].passEnter && !state.menu.navigated {
			return false
		}
		items[pick].take()
	case ggui.KeyTab:
		m.setComposerDraft(id, items[pick].complete)
	case ggui.KeyEscape:
		state.menu.closed.Set(ggui.Peek(state.draft))
	default:
		return false
	}
	return true
}

// stepPick moves pick, the row chosen among n, up for ↑ and down for ↓,
// round from either end; it reports whether key was either.
func stepPick(pick *ggui.StateValue[int], n int, key ggui.KeyboardKey) bool {
	at := min(ggui.Peek(pick), n-1)
	switch key {
	case ggui.KeyArrowUp:
		pick.Set((at + n - 1) % n)
	case ggui.KeyArrowDown:
		pick.Set((at + 1) % n)
	default:
		return false
	}
	return true
}

// plainKey reports whether ev is a key pressed with no modifier, as a
// menu's keys are.
func plainKey(ev ggui.KeyEvent) bool {
	return !ev.Mods.Shift && !ev.Mods.Alt && !ev.Mods.Ctrl && !ev.Mods.Meta
}

// menuKeys are what the menu lists while it takes ev, a key pressed with
// no modifier; none when it does not.
func (m *model) menuKeys(id string, state *chatState, ev ggui.KeyEvent) []menuItem {
	if !plainKey(ev) {
		return nil
	}
	return ggui.Untrack(func() []menuItem { return m.menuItems(id, state) })
}

// menuClaims reports whether the composer keeps ev from the window while
// its menu lists anything: Tab, which would move the focus.
func (m *model) menuClaims(id string, state *chatState, ev ggui.KeyEvent) bool {
	return ev.Key == ggui.KeyTab && len(m.menuKeys(id, state, ev)) > 0
}

// takeCommand puts c in the composer: sent at once when it takes no
// arguments, and otherwise followed by a space to write them after.
func (m *model) takeCommand(id string, c *protocol.Command) {
	if c.GetArgs() != "" {
		m.setComposerDraft(id, "/"+c.GetName()+" ")
		return
	}
	m.setComposerDraft(id, "/"+c.GetName())
	m.sendComposer(id)
}

// composerMenu shows what the composer's draft lists, over it, the one
// chosen marked; or, once a template's name has arguments after it, what
// the template takes.
func composerMenu(m *model, id string, state *chatState) ggui.Widget {
	// A draft that begins with two slashes reads the templates, one with a
	// slash the commands, and a new query chooses its first match again.
	ggui.Watch(state.draft, func(draft string) {
		if _, ok := prompttmpl.ParseCall(draft); ok {
			m.readTemplates(state, false)
		} else if _, ok := commandQuery(draft); ok {
			m.readCommands(id, state)
		}
		state.menu.pick.Set(0)
		state.menu.navigated = false
	})
	return ggui.Reactive(func() ggui.Widget {
		var content ggui.Widget
		if items := m.menuItems(id, state); len(items) > 0 {
			pick := min(state.menu.pick.Get(), len(items)-1)
			rows := make([]ggui.Widget, len(items))
			for i, it := range items {
				rows[i] = menuRow(it, i == pick)
			}
			rowsCol := widget.Column(rows...).Gap(2).Stretch()
			if hint := templateHint(state); hint != nil {
				content = widget.Column(hint, hline(), rowsCol).Gap(4).Stretch()
			} else {
				content = rowsCol
			}
		} else if content = templateHint(state); content == nil {
			return widget.Box()
		}
		return widget.Box(menuPanel(content)).Pad(0, 0, 6, 0)
	})
}

func menuRow(it menuItem, chosen bool) ggui.Widget {
	name := []ggui.Widget{widget.Text(it.name).Size(13).Weight(ggui.WeightMedium).NoWrap()}
	if it.usage != "" {
		name = append(name, widget.Text(it.usage).Size(12).Color(skin.ExtraMuted).NoWrap())
	}
	parts := []ggui.Widget{
		widget.Row(name...).Gap(6),
		widget.Expanded(muted(widget.Text(it.description).Size(12).NoWrap().Ellipsis())),
	}
	if it.tag != "" {
		parts = append(parts, widget.Text(it.tag).Size(11).Color(skin.ExtraMuted).NoWrap())
	}
	return menuRowBox(widget.Row(parts...).Gap(12), chosen, it.take)
}

// menuPanel is the box a menu's rows are listed in, as the composer's
// and a template prompt's are.
func menuPanel(rows ggui.Widget) ggui.Widget {
	return widget.Box(rows).Fill(skin.Surface1).Border(1, skin.Border).Radius(10).Pad(4)
}

// menuRowBox is a row of a menu, marked when chosen, which a tap takes.
func menuRowBox(content ggui.Widget, chosen bool, take func()) ggui.Widget {
	row := widget.Box(content).Pad(6, 10).Radius(6)
	if chosen {
		// As plain as a selected row in the sidebar, and as plain to see.
		row = row.Fill(skin.SidebarSel).Border(1, skin.BorderAccent)
	}
	return widget.Tap(row, take)
}

// commandSource names where a command comes from, for the menu.
func commandSource(source string) string {
	switch source {
	case "user":
		return T("user")
	case "project":
		return T("project")
	}
	return source // a plugin's name
}
