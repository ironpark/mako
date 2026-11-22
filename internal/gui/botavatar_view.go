package gui

import (
	"cmp"
	"fmt"
	"image/color"
	"math"
	"strings"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/gui/botavatar"
	"github.com/ironpark/mako/internal/gui/skin"
	"github.com/ironpark/mako/internal/protocol"
)

// botAvatarPrefs stores the user's Open Bot Avatar preferences in gui.json.
type botAvatarPrefs struct {
	Enabled    bool   `json:"enabled"`
	Avatar     string `json:"avatar,omitempty"`      // "" = auto per agent, built-in ID, or "custom"
	Color      string `json:"color,omitempty"`       // optional #rrggbb override
	Mood       string `json:"mood,omitempty"`        // "neutral" or "happy"
	CustomJSON string `json:"custom_json,omitempty"` // custom open-bot-avatar v1 JSON
}

func defaultBotAvatarPrefs() botAvatarPrefs {
	return botAvatarPrefs{
		Enabled: true,
		Mood:    string(botavatar.MoodNeutral),
	}
}

func (p botAvatarPrefs) normalized() botAvatarPrefs {
	if !botavatar.IsMood(botavatar.Mood(p.Mood)) {
		p.Mood = string(botavatar.MoodNeutral)
	}
	if p.Avatar != "" && p.Avatar != "custom" {
		if _, ok := botavatar.Lookup(p.Avatar); !ok {
			p.Avatar = ""
		}
	}
	if p.Color != "" {
		if rgb, ok := botavatar.ParseHex(p.Color); ok {
			p.Color = botavatar.ToHex(rgb)
		} else {
			p.Color = ""
		}
	}
	return p
}

// botAvatarStateLabels maps each Open Bot Avatar state to its translatable label.
var botAvatarStateLabels = map[botavatar.State]string{
	botavatar.StateIdle:     "Idle",
	botavatar.StateThinking: "Thinking",
	botavatar.StateWorking:  "Working",
	botavatar.StateWaiting:  "Waiting",
	botavatar.StateBlocked:  "Blocked",
	botavatar.StateDone:     "Done",
}

// resolveBotAvatar picks the avatar definition, body color, and mood for an
// agent name according to the user's preferences.
func resolveBotAvatar(p botAvatarPrefs, agentName string) (botavatar.Definition, string, botavatar.Mood) {
	baseAgent, _ := agentspec.SplitTarget(agentName)
	def, col := botavatar.ForAgent(baseAgent)

	// Prefer the agent driver's signature color when auto-assigning.
	if _, own, _ := agentLook(baseAgent); own != nil {
		n := color.NRGBAModel.Convert(own).(color.NRGBA)
		col = botavatar.ToHex(botavatar.RGB{float64(n.R), float64(n.G), float64(n.B)})
	}

	switch p.Avatar {
	case "":
		// Auto per agent (already resolved above).
	case "custom":
		if custom, err := botavatar.ParseJSON([]byte(p.CustomJSON)); err == nil {
			def = custom
			col = botavatar.DefaultColorHex
		}
	default:
		if picked, ok := botavatar.Lookup(p.Avatar); ok {
			def = picked
			col = botavatar.DefaultColor(picked.ID)
		}
	}

	if p.Color != "" {
		if rgb, ok := botavatar.ParseHex(p.Color); ok {
			col = botavatar.ToHex(rgb)
		}
	}

	mood := botavatar.Mood(p.Mood)
	if !botavatar.IsMood(mood) {
		mood = botavatar.MoodNeutral
	}
	return def, col, mood
}

// sessionAvatarState maps a mako session's live status into an Open Bot Avatar state.
func (m *model) sessionAvatarState(s *protocol.SessionInfo) botavatar.State {
	if s == nil || s.GetState() == protocol.StateExited {
		return botavatar.StateIdle
	}
	id := s.GetId()
	switch {
	case s.GetApproval() != nil:
		return botavatar.StateBlocked
	case s.GetStatus() == protocol.AgentCompacting:
		return botavatar.StateThinking
	case s.GetStatus() == protocol.AgentWorking || m.busy(id):
		return botavatar.StateWorking
	case s.GetStatus() == protocol.AgentWaiting:
		return botavatar.StateWaiting
	case s.GetStatus() == protocol.AgentDone || ggui.Peek(m.Unread)[id]:
		return botavatar.StateDone
	default:
		return botavatar.StateIdle
	}
}

// liveBotAvatar wraps widget.SVG with optional smooth pointer-tracking gaze
// driven via widget.SVGController.Node("oba-gaze") and Node("oba-root").
type liveBotAvatar struct {
	svg           *widget.SVGWidget
	ctrl          *widget.SVGController
	def           botavatar.Definition
	size          float64
	followPointer bool
	reduced       bool
	gx, gy        float64 // current eased gaze in -1..1
}

func newLiveBotAvatar(def botavatar.Definition, opts botavatar.Options, size float64, fps int, followPointer bool, key any) ggui.Widget {
	doc, err := botavatar.Document(def, opts)
	if err != nil {
		return widget.Box().Size(size, size)
	}
	ctrl := &widget.SVGController{}
	svgW := widget.SVG(doc).Size(size, size).MaxFPS(fps).Key(key).Controller(ctrl)
	if followPointer {
		svgW = svgW.Alt(def.Name)
	}
	if !followPointer {
		return svgW
	}
	return &liveBotAvatar{
		svg:           svgW,
		ctrl:          ctrl,
		def:           def,
		size:          size,
		followPointer: true,
	}
}

func (a *liveBotAvatar) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	a.reduced = env.ReducedMotion()
	return a.svg.Layout(c, env)
}

func (a *liveBotAvatar) Paint(dst *ggui.Canvas, r ggui.Rect) {
	if a.followPointer && !a.reduced && dst != nil {
		tx, ty := 0.0, 0.0
		if ptr, ok := dst.Pointer(); ok && r.Size.W > 0 && r.Size.H > 0 {
			// Radial soft saturation matching open-bot-avatar's pointerGaze.
			reach := max(r.Size.W, r.Size.H, 160.0)
			cx := r.Origin.X + r.Size.W*(a.def.Eyes.X/100.0)
			cy := r.Origin.Y + r.Size.H*(a.def.Eyes.Y/100.0)
			dx := (ptr.X - cx) / reach
			dy := (ptr.Y - cy) / reach
			if mag := math.Hypot(dx, dy); mag > 1e-6 {
				k := math.Tanh(mag) / mag
				tx, ty = dx*k, dy*k
			}
		}
		a.gx += (tx - a.gx) * 0.25
		a.gy += (ty - a.gy) * 0.25
		maxX, maxY := botavatar.GazeTravel(a.def)
		a.ctrl.Node("oba-gaze").Offset(ggui.Pt(a.gx*maxX, a.gy*maxY))
		a.ctrl.Node("oba-root").Origin(0.5, 0.85).Rotate(a.gx * 0.08)
	}
	dst.Paint(a.svg, r)
}

// sessionAvatar draws an animated Open Bot Avatar for an AI agent session row,
// or falls back to the classic busyGlyph for shell sessions or when disabled.
func sessionAvatar(m *model, s *protocol.SessionInfo, size float64) ggui.Widget {
	id := s.GetId()
	agent := s.GetAgent()
	fallback := func() ggui.Widget {
		return widget.Padding(&busyGlyph{
			icon: agentGlyph(agent, skin.Muted).Size(15),
			busy: func() bool { return m.busy(id) },
		}, 0, 2.5, 0, 2.5)
	}
	if agent == "" || agent == "shell" {
		return fallback()
	}
	return ggui.Reactive(func() ggui.Widget {
		cfg := m.Prefs.Get().BotAvatar
		if !cfg.Enabled {
			return fallback()
		}
		m.Unread.Get()
		def, col, mood := resolveBotAvatar(cfg, agent)
		st := m.sessionAvatarState(s)
		opts := botavatar.Options{
			Color: col,
			State: st,
			Mood:  mood,
			Seed:  botavatar.HashString(id),
		}
		return newLiveBotAvatar(def, opts, size, 20, false, fmt.Sprintf("sess-avatar:%s:%s:%s", id, def.ID, st))
	})
}

// launchAvatar draws a large interactive pointer-following bot avatar on the
// New Session greeting screen (or the classic sparkles icon when disabled).
func launchAvatar(m *model, agent string) ggui.Widget {
	cfg := m.Prefs.Get().BotAvatar
	if !cfg.Enabled {
		return icon("sparkles").Size(34).Color(skin.ExtraMuted)
	}
	def, col, mood := resolveBotAvatar(cfg, cmp.Or(agent, "claude"))
	opts := botavatar.Options{
		Color: col,
		State: botavatar.StateIdle,
		Mood:  mood,
	}
	return newLiveBotAvatar(def, opts, 76, 30, true, fmt.Sprintf("launch-avatar:%s:%s", def.ID, col))
}

// avatarBlockPanel implements the built-in mako.avatar layout block: a large
// interactive bot avatar tracking the active session's live state, with quick
// mood and avatar controls.
func avatarBlockPanel(m *model) ggui.Widget {
	return ggui.Reactive(func() ggui.Widget {
		cfg := m.Prefs.Get().BotAvatar
		m.Unread.Get()
		sel := m.Selected.Get()
		sessions := m.Sessions.Get()
		titles := m.Titles.Get()

		s, hasSession := sessionIn(sessions, sel)
		agent := ""
		title := T("No active session")
		state := botavatar.StateIdle
		seed := uint32(0)
		if hasSession {
			agent = s.GetAgent()
			title = sessionTitle(s, titles[sel])
			state = m.sessionAvatarState(s)
			seed = botavatar.HashString(sel)
		}
		def, col, mood := resolveBotAvatar(cfg, cmp.Or(agent, "claude"))
		opts := botavatar.Options{
			Color: col,
			State: state,
			Mood:  mood,
			Seed:  seed,
		}
		avatar := newLiveBotAvatar(def, opts, 128, 30, true, fmt.Sprintf("block-avatar:%s:%s:%s:%s", sel, def.ID, state, mood))

		moodToggle := ui.Button(pick(mood == botavatar.MoodHappy, T("Happy"), T("Neutral")), func() {
			next := string(botavatar.MoodHappy)
			if mood == botavatar.MoodHappy {
				next = string(botavatar.MoodNeutral)
			}
			m.Prefs.Update(func(p prefs) prefs { p.BotAvatar.Mood = next; return p })
		}).Outline()

		items := []ggui.Widget{
			ui.MenuItem(T("Auto per agent"), func() {
				m.Prefs.Update(func(p prefs) prefs { p.BotAvatar.Avatar = ""; return p })
			}).Checked(cfg.Avatar == ""),
			ui.MenuDivider(),
		}
		for _, d := range botavatar.Builtin {
			id := d.ID
			items = append(items, ui.MenuItem(d.Name, func() {
				m.Prefs.Update(func(p prefs) prefs { p.BotAvatar.Avatar = id; return p })
			}).Checked(cfg.Avatar == id))
		}
		picker := ui.MenuOf(widget.Row(
			widget.Text(def.Name).Size(12.5).NoWrap(),
			icon("chevron-down").Size(12).Color(skin.Muted),
		).Gap(4).Align(widget.AlignCenter), items...).Name(T("Avatar"))

		return widget.Center(widget.Padding(widget.Column(
			avatar,
			widget.Text(title).Size(13.5).Weight(ggui.WeightSemibold).NoWrap().Ellipsis(),
			ui.Badge(T(botAvatarStateLabels[state])).Secondary(),
			widget.Row(picker, moodToggle).Gap(8).Align(widget.AlignCenter),
		).Gap(10).Align(widget.AlignCenter), 16))
	})
}

// botAvatarSettings renders the Bot Avatar section inside Settings ▸ Appearance.
func botAvatarSettings(m *model) ggui.Widget {
	previewState := ggui.State(botavatar.StateIdle)

	enabled := ggui.Bind(
		func() bool { return m.Prefs.Get().BotAvatar.Enabled },
		func(v bool) { m.Prefs.Update(func(p prefs) prefs { p.BotAvatar.Enabled = v; return p }) },
	)
	mood := ggui.Bind(
		func() string { return m.Prefs.Get().BotAvatar.Mood },
		func(v string) { m.Prefs.Update(func(p prefs) prefs { p.BotAvatar.Mood = v; return p.normalized() }) },
	)
	colorText := ggui.State(ggui.Peek(m.Prefs).BotAvatar.Color)
	ggui.Watch(colorText, func(s string) {
		trimmed := strings.TrimSpace(s)
		if trimmed == "" {
			if ggui.Peek(m.Prefs).BotAvatar.Color != "" {
				m.Prefs.Update(func(p prefs) prefs { p.BotAvatar.Color = ""; return p })
			}
			return
		}
		if rgb, ok := botavatar.ParseHex(trimmed); ok {
			hex := botavatar.ToHex(rgb)
			if ggui.Peek(m.Prefs).BotAvatar.Color != hex {
				m.Prefs.Update(func(p prefs) prefs { p.BotAvatar.Color = hex; return p })
			}
		}
	})
	customText := ggui.Bind(
		func() string { return m.Prefs.Get().BotAvatar.CustomJSON },
		func(v string) {
			m.Prefs.Update(func(p prefs) prefs {
				p.BotAvatar.CustomJSON = v
				if _, err := botavatar.ParseJSON([]byte(v)); err == nil {
					p.BotAvatar.Avatar = "custom"
				}
				return p
			})
		},
	)

	preview := ggui.Reactive(func() ggui.Widget {
		cfg := m.Prefs.Get().BotAvatar
		st := previewState.Get()
		def, col, md := resolveBotAvatar(cfg, "claude")
		opts := botavatar.Options{
			Color: col,
			State: st,
			Mood:  md,
		}
		av := newLiveBotAvatar(def, opts, 96, 30, true, fmt.Sprintf("settings-avatar:%s:%s:%s:%s", def.ID, col, st, md))

		stateBtns := make([]ggui.Widget, 0, len(botavatar.AllStates))
		for _, s := range botavatar.AllStates {
			cur := s
			btn := ui.Button(T(botAvatarStateLabels[cur]), func() { previewState.Set(cur) })
			if cur != st {
				btn = btn.Outline()
			}
			stateBtns = append(stateBtns, btn)
		}

		return widget.Row(
			widget.Box(widget.Center(av)).Size(116, 116).Radius(12).Fill(skin.Surface2).Border(1, skin.Border),
			widget.Expanded(widget.Column(
				widget.Text(def.Name).Size(14).Weight(ggui.WeightSemibold),
				muted(widget.Text(T("Preview state")).Size(12)),
				widget.Wrap(stateBtns...).Gap(6),
			).Gap(8).Stretch()),
		).Gap(16).Align(widget.AlignCenter)
	})

	gallery := ggui.Reactive(func() ggui.Widget {
		cfg := m.Prefs.Get().BotAvatar
		chips := make([]ggui.Widget, 0, len(botavatar.Builtin)+2)

		autoBtn := ui.Button(T("Auto per agent"), func() {
			m.Prefs.Update(func(p prefs) prefs { p.BotAvatar.Avatar = ""; return p })
		})
		if cfg.Avatar != "" {
			autoBtn = autoBtn.Outline()
		}
		chips = append(chips, autoBtn)

		for _, d := range botavatar.Builtin {
			def := d
			selected := cfg.Avatar == def.ID
			col := botavatar.DefaultColor(def.ID)
			if cfg.Color != "" {
				col = cfg.Color
			}
			var thumb ggui.Widget
			if doc, err := botavatar.Document(def, botavatar.Options{Color: col, ReducedMotion: true}); err == nil {
				thumb = widget.SVG(doc).Size(20, 20).Key("thumb:" + def.ID + ":" + col)
			} else {
				thumb = widget.Box().Size(20, 20)
			}
			face := widget.Row(thumb, widget.Text(def.Name).Size(12.5).NoWrap()).Gap(6).Align(widget.AlignCenter)
			btn := ui.ButtonOf(face, func() {
				m.Prefs.Update(func(p prefs) prefs { p.BotAvatar.Avatar = def.ID; return p })
			}).Pad(5, 9).Radius(7).Name(def.Name)
			if !selected {
				btn = btn.Outline()
			}
			chips = append(chips, btn)
		}

		customBtn := ui.Button(T("Custom JSON"), func() {
			m.Prefs.Update(func(p prefs) prefs { p.BotAvatar.Avatar = "custom"; return p })
		})
		if cfg.Avatar != "custom" {
			customBtn = customBtn.Outline()
		}
		chips = append(chips, customBtn)
		return widget.Wrap(chips...).Gap(6)
	})

	moodSelect := ui.Select(mood).
		Options([]string{string(botavatar.MoodNeutral), string(botavatar.MoodHappy)}).
		Format(func(s string) string {
			if s == string(botavatar.MoodHappy) {
				return T("Happy")
			}
			return T("Neutral")
		}).Name(T("Mood"))

	customStatus := ggui.Reactive(func() ggui.Widget {
		raw := strings.TrimSpace(m.Prefs.Get().BotAvatar.CustomJSON)
		if raw == "" {
			return widget.Box()
		}
		if def, err := botavatar.ParseJSON([]byte(raw)); err == nil {
			return widget.Text(Tf("Valid avatar: %s", def.Name)).Size(12).Color(skin.DotRunning)
		} else {
			return widget.Text(err.Error()).Size(12).Color(skin.DotDown)
		}
	})

	return section(T("Bot avatar"),
		card(
			row(T("Show bot avatars"),
				T("Display animated Open Bot Avatars for AI agents in the sidebar, new-session screen, and avatar block."),
				ui.Switch(enabled, "").Name(T("Show bot avatars"))),
			widget.Box(preview).Pad(12, 16),
			row(T("Mood"), "", widget.Box(moodSelect).Width(160)),
			row(T("Body color"), T("Optional #rrggbb override; leave empty for the agent or avatar default."),
				widget.Box(ui.TextField(colorText).Placeholder("#ff6a1a").Name(T("Body color"))).Width(140)),
			stackedRow(T("Avatar"), "", gallery),
			stackedRow(T("Custom avatar JSON"),
				T("Paste an Open Bot Avatar v1 JSON definition to use a custom shape."),
				widget.Column(
					ui.TextField(customText).Lines(3).Placeholder(`{"version":1,"id":"box","name":"Box","body":{"path":"M20 20 H80 V80 H20 Z"},"eyes":{"x":50,"y":46,"spacing":15,"scale":1}}`).Name(T("Custom avatar JSON")),
					customStatus,
				).Gap(6).Stretch()),
		),
	)
}
