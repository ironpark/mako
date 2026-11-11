package gui

import (
	"context"
	"image/color"
	"strings"
	"time"
	"unicode"

	"github.com/ironpark/ggui"
	ggruntime "github.com/ironpark/ggui/runtime"
	"github.com/ironpark/ggui/ui"
	uitheme "github.com/ironpark/ggui/ui/theme"
	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agents"
	"github.com/ironpark/mako/internal/client"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// The composer's bar, under what is typed, as chat apps lay theirs out: on
// the left a + to attach files and what the agent may do unasked; on the
// right the model its latest reply was made with, at its effort, and the
// button that sends, or while the agent works on a turn and nothing is
// typed, stops it.

// composerRadius rounds the composer's box.
const composerRadius = 20

// composerBar is the bar of session id's composer.
func composerBar(m *model, id string, state *chatState, disabled ggui.Readable[bool]) ggui.Widget {
	attach := ui.Tooltip(ui.ButtonOf(icon("plus").Size(17).Color(skin.Fg), func() { m.attachFiles(id) }).
		Ghost().Pad(5).Radius(15).Name(T("Attach files")).BindDisabled(disabled), T("Attach files"))
	usage := ggui.Map(state.result, func(v chatResult) *protocol.Usage { return v.Chat.GetUsage() }).WithEqual(protocol.Same)
	// What the agent says it runs with, and what it may be set to.
	// Only these of its usage, whose counts move with every reply.
	type running struct {
		mode, model, effort string
		controls            *protocol.AgentControls
	}
	now := ggui.Derived(func() running {
		s, _ := sessionIn(m.Sessions.Get(), id)
		u := usage.Get()
		return running{u.GetPermissionMode(), u.GetModel(), u.GetEffort(), s.GetControls()}
	}).WithEqual(func(a, b running) bool {
		return a.mode == b.mode && a.model == b.model && a.effort == b.effort && proto.Equal(a.controls, b.controls)
	})
	mode := ggui.View(now, func(r running) ggui.Widget { return modeMenu(m, id, r.mode, r.controls.GetModes()) })
	model := ggui.View(now, func(r running) ggui.Widget { return modelMenu(m, id, r.model, r.effort, r.controls) })
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
	return ggui.Row(attach, mode, ggui.Spacer(), model, button).Gap(6).Align(ggui.AlignCenter)
}

// roundButton is the composer's main button: a light disc with glyph.
func roundButton(glyph, name string, fn func(), disabled ggui.Readable[bool]) ggui.Widget {
	return ui.ButtonOf(iconColored(glyph, uitheme.PrimaryFg).Size(16), fn).
		Pad(7).Radius(16).Name(name).BindDisabled(disabled)
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

// stopTurn interrupts the turn session id's agent is on, as its driver
// says to in its terminal.
func (m *model) stopTurn(id string) {
	s, _ := m.session(id)
	if conn := m.conns[id]; conn != nil {
		known, _ := agents.Lookup(s.GetAgent())
		conn.send(agent.Interrupt(known.Driver))
	}
}

// permissionChip says what the agent may do unasked, by the mode it names:
// everything, loud; edits; planning alone; or asking first, quiet. Nothing
// for an agent that says none.
func permissionChip(mode string) ggui.Widget {
	if mode == "" {
		return ggui.Box()
	}
	glyph, label, tip := "lock", modeLabel(mode), T("What the agent may do without asking, as its terminal sets it.")
	var c color.Color = skin.Muted
	switch mode {
	case "bypassPermissions":
		glyph, c = "shield-alert", skin.DotWaiting
	case "acceptEdits":
		glyph, c = "pencil", skin.AccentBright
	case "plan":
		glyph, c = "list", skin.AccentBright
	}
	chip := ggui.Row(
		icon(glyph).Size(15).Color(c),
		ggui.Text(label).Size(13).Color(c).NoWrap(),
	).Gap(5).Align(ggui.AlignCenter)
	return ui.Tooltip(ggui.Box(chip).Pad(4, 6), tip)
}

// modeMenu is the permission chip, which switches the agent to one of
// modes, where it has any.
func modeMenu(m *model, id, mode string, modes []string) ggui.Widget {
	if len(modes) == 0 {
		return permissionChip(mode)
	}
	if mode == "" {
		mode = "default"
	}
	items := make([]ggui.Widget, len(modes))
	for i, to := range modes {
		label := modeLabel(to)
		if to == mode {
			label = "✓ " + label
		}
		items[i] = ui.MenuItem(label, func() { m.configureAgent(id, &protocol.ConfigureAgent{Mode: to}) })
	}
	return iconMenu(ui.MenuOf(permissionChip(mode), items...).Width(200).Name(T("Permission mode")), 0)
}

// modelMenu is the model chip, which sets the agent's model or effort to
// one of those it offers, where it offers any; the agent keeps what is set
// as the user's default.
func modelMenu(m *model, id, current, effort string, c *protocol.AgentControls) ggui.Widget {
	chip := modelChip(current, effort)
	if len(c.GetModels())+len(c.GetEfforts()) == 0 {
		return chip
	}
	if current == "" {
		chip = ggui.Box(ggui.Text(T("Model")).Size(13).Color(skin.Muted).NoWrap()).Pad(4, 6)
	}
	var items []ggui.Widget
	for _, to := range c.GetModels() {
		label := capitalized(to)
		if strings.Contains(strings.ToLower(current), to) {
			label = "✓ " + label
		}
		items = append(items, ui.MenuItem(label, func() { m.configureAgent(id, &protocol.ConfigureAgent{Model: to}) }))
	}
	if len(items) > 0 && len(c.GetEfforts()) > 0 {
		items = append(items, ui.MenuDivider())
	}
	for _, to := range c.GetEfforts() {
		label := Tf("%s effort", capitalized(to))
		if to == effort {
			label = "✓ " + label
		}
		items = append(items, ui.MenuItem(label, func() { m.configureAgent(id, &protocol.ConfigureAgent{Effort: to}) }))
	}
	items = append(items, ui.MenuDivider(), ui.MenuItem(T("The agent keeps these as your default"), nil).Disabled(true))
	return iconMenu(ui.MenuOf(chip, items...).Width(260).Name(T("Model")), 0)
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
	}, func(_ *protocol.SessionInfo, err error) {
		if err != nil {
			m.connErr(err)
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

// modelChip names the model the latest reply was made with, and its
// effort, quieter; nothing before there is one.
func modelChip(model, effort string) ggui.Widget {
	if model == "" {
		return ggui.Box()
	}
	parts := []ggui.Widget{ggui.Text(modelName(model)).Size(13).NoWrap()}
	if effort != "" {
		parts = append(parts, ggui.Text(capitalized(effort)).Size(13).Color(skin.Muted).NoWrap())
	}
	return ui.Tooltip(ggui.Box(ggui.Row(parts...).Gap(5).Align(ggui.AlignCenter)).Pad(4, 6), model)
}

// modelName is a model as people say it: claude-opus-5-5 is Opus 5.5, and
// claude-sonnet-4-5-20250929 Sonnet 4.5. Another agent's stays as it is.
func modelName(id string) string {
	rest, ok := strings.CutPrefix(id, "claude-")
	if !ok {
		return id
	}
	var name, version []string
	for _, part := range strings.Split(rest, "-") {
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
