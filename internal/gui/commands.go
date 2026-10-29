package gui

import (
	"context"
	"strings"
	"time"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/protocol"
)

// Slash commands: a composer's draft that begins with a slash, and has no
// space yet, lists the commands of the session's agent that it begins, or
// holds, for ↑ and ↓ to choose from and Enter to take. The commands are
// those the session's node finds for its agent, read when a draft first
// begins with a slash, and again once they are a while old.

// commandMenu is the composer's list of the commands its draft matches.
type commandMenu struct {
	commands *ggui.StateValue[[]*protocol.Command]
	read     time.Time // when the commands were last asked for
	reading  bool
	pick     *ggui.StateValue[int] // the one chosen, of those matched
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

// maxCommandMatches bounds the commands the menu lists.
const maxCommandMatches = 8

// commandQuery is what draft asks of the commands: the name it begins,
// without its slash, and whether it asks at all.
func commandQuery(draft string) (string, bool) {
	if !strings.HasPrefix(draft, "/") || strings.ContainsAny(draft, " \t\n") {
		return "", false
	}
	return strings.ToLower(draft[1:]), true
}

// matchCommands are the commands whose names begin with query, then those
// that hold it elsewhere, each in the order the agent lists them.
func matchCommands(commands []*protocol.Command, query string) []*protocol.Command {
	var begins, holds []*protocol.Command
	for _, c := range commands {
		name := strings.ToLower(c.GetName())
		switch {
		case strings.HasPrefix(name, query):
			begins = append(begins, c)
		case strings.Contains(name, query):
			holds = append(holds, c)
		}
	}
	out := append(begins, holds...)
	if len(out) > maxCommandMatches {
		out = out[:maxCommandMatches]
	}
	return out
}

// commandMatches are the commands the composer's draft lists, none while
// the menu is closed. Reactive.
func (m *model) commandMatches(state *chatState) []*protocol.Command {
	draft := state.draft.Get()
	query, ok := commandQuery(draft)
	if !ok || draft == state.menu.closed.Get() {
		return nil
	}
	return matchCommands(state.menu.commands.Get(), query)
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

// commandKey is the composer's keys while its menu lists commands: ↑ and
// ↓ choose, Enter takes the one chosen and Escape closes the menu. It
// reports whether it took the key.
func (m *model) commandKey(id string, state *chatState, ev ggui.KeyEvent) bool {
	matches := ggui.Untrack(func() []*protocol.Command { return m.commandMatches(state) })
	if len(matches) == 0 || ev.Mods.Shift || ev.Mods.Alt || ev.Mods.Ctrl || ev.Mods.Meta {
		return false
	}
	pick := min(ggui.Peek(state.menu.pick), len(matches)-1)
	switch ev.Key {
	case ggui.KeyArrowUp:
		state.menu.pick.Set((pick + len(matches) - 1) % len(matches))
	case ggui.KeyArrowDown:
		state.menu.pick.Set((pick + 1) % len(matches))
	case ggui.KeyEnter, ggui.KeyNumpadEnter:
		m.takeCommand(id, matches[pick])
	case ggui.KeyEscape:
		state.menu.closed.Set(ggui.Peek(state.draft))
	default:
		return false
	}
	return true
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

// commandList shows the commands the composer's draft matches, over it,
// the one chosen marked.
func commandList(m *model, id string, state *chatState) ggui.Widget {
	// A draft that begins with a slash reads the commands, and a new
	// query chooses its first match again.
	ggui.Watch(state.draft, func(draft string) {
		if _, ok := commandQuery(draft); ok {
			m.readCommands(id, state)
		}
		state.menu.pick.Set(0)
	})
	return ggui.Reactive(func() ggui.Widget {
		matches := m.commandMatches(state)
		if len(matches) == 0 {
			return ggui.Box()
		}
		pick := min(state.menu.pick.Get(), len(matches)-1)
		rows := make([]ggui.Widget, len(matches))
		for i, c := range matches {
			rows[i] = commandRow(m, id, c, i == pick)
		}
		list := ggui.Box(ggui.Column(rows...).Gap(2).Stretch()).
			Fill(tokSurface1).Border(1, tokBorder).Radius(10).Pad(4)
		return ggui.Box(list).Pad(0, 0, 6, 0)
	})
}

func commandRow(m *model, id string, c *protocol.Command, chosen bool) ggui.Widget {
	name := []ggui.Widget{ggui.Text("/" + c.GetName()).Size(13).Weight(ggui.WeightMedium).NoWrap()}
	if c.GetArgs() != "" {
		name = append(name, ggui.Text(c.GetArgs()).Size(12).Color(tokExtraMuted).NoWrap())
	}
	parts := []ggui.Widget{
		ggui.Row(name...).Gap(6),
		ggui.Expanded(muted(ggui.Text(c.GetDescription()).Size(12).NoWrap().Ellipsis())),
	}
	if c.GetSource() != "" {
		parts = append(parts, ggui.Text(commandSource(c.GetSource())).Size(11).Color(tokExtraMuted).NoWrap())
	}
	row := ggui.Box(ggui.Row(parts...).Gap(12)).Pad(6, 10).Radius(6)
	if chosen {
		row = row.Fill(tokSurface2)
	}
	return ggui.Tap(row, func() { m.takeCommand(id, c) })
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
