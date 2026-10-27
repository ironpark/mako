package gui

import (
	"image/color"
	"maps"
	"os"
	"strconv"
	"strings"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/runtime"
	"github.com/ironpark/ggui/ui"
)

// Settings ▸ Appearance: the theme, the user's own themes and their
// colours, and the background images.

func appearanceSettings(m *model) ggui.Widget {
	theme := ui.Select(prefField(m, func(p *prefs) *string { return &p.Theme })).
		BindOptions(ggui.Map(m.Themes, themeNames)).Format(themeLabel).Name(T("Theme"))
	corner := ui.Select(prefField(m, func(p *prefs) *string { return &p.UsageCorner })).
		Options(usageCorners).Format(func(s string) string { return T(usageCornerLabels[s]) }).Name(T("Overlay position")).
		BindDisabled(ggui.Map(m.Prefs, func(p prefs) bool { return !p.UsageOverlay }))
	actions := ggui.Row(
		ui.Button(T("Duplicate"), m.duplicateTheme).Outline(),
		ui.Button(T("Delete"), m.deleteTheme).Outline().BindDisabled(ggui.Map(m.Prefs, func(p prefs) bool { return isBuiltinTheme(p.Theme) })),
		ui.Button(T("Open folder"), openThemesFolder).Ghost(),
		ui.Button(T("Reload"), m.reloadThemes).Ghost(),
	).Gap(6)
	return page(T("Appearance"), T("The window's colours, after Paseo's palettes, or your own."),
		section(T("Theme"),
			card(
				row(T("Theme"), T("System follows the desktop's light or dark appearance."), ggui.Box(theme).Width(200)),
				stackedRow(T("Your themes"), T("A theme of your own starts as a copy of another and keeps its colours in the themes folder, as JSON you can edit or share."), actions),
			),
			themeEditor(m),
		),
		section(T("Background"),
			card(backdropRows(m, T("Window"), T("Behind the whole window: the sidebar, the workspace and the settings."), windowBackdrop)...),
			card(backdropRows(m, T("Sessions"), T("Behind each session's terminal and chat, over the window's."), sessionBackdrop)...),
		),
		section(T("Agent usage"),
			card(
				row(T("Show usage overlay"), T("Float a Claude or Codex session's context and its account's 5-hour and weekly limits over its terminal and chat."),
					ui.Switch(prefField(m, func(p *prefs) *bool { return &p.UsageOverlay }), "").Name(T("Show usage overlay"))),
				row(T("Overlay position"), T("The corner of the session the overlay sits in."), ggui.Box(corner).Width(160)),
			),
		),
	)
}

// themeLabel names a theme: the app's own in the window's language, the
// user's by their names.
func themeLabel(name string) string {
	if isBuiltinTheme(name) {
		return T(themeLabels[name])
	}
	return name
}

// themeEditor is the colours of the theme in use: fields to change them
// for a theme of the user's, a swatch of them for one of the app's. It
// starts again when another theme is chosen or the themes are read again,
// not as a colour changes, so the field being typed in keeps its place.
func themeEditor(m *model) ggui.Widget {
	name := ggui.Map(m.Prefs, func(p prefs) string { return p.Theme })
	return ggui.Reactive(func() ggui.Widget {
		theme := name.Get()
		m.ThemesRead.Get()
		p, ok := ggui.Peek(m.Themes)[theme]
		if !ok {
			return themeSwatch(m.themePalette())
		}
		var groups, rows []ggui.Widget
		for i, c := range paletteColors {
			rows = append(rows, colorRow(m, theme, c, p))
			if i == len(paletteColors)-1 || paletteColors[i+1].group != c.group {
				groups = append(groups, section(T(c.group), card(rows...)))
				rows = nil
			}
		}
		return ggui.Column(groups...).Gap(20).Stretch()
	})
}

// colorRow is one colour of a theme: its swatch and its hex, saved to the
// theme as soon as the hex is whole.
func colorRow(m *model, theme string, c paletteColor, p palette) ggui.Widget {
	value := ggui.State(hexString(c.get(p)))
	ggui.Watch(value, func(s string) {
		if v, ok := parseHex(s); ok {
			m.setThemeColor(theme, c, v)
		}
	})
	swatch := ggui.View(value, func(s string) ggui.Widget {
		box := ggui.Box().Size(22, 22).Radius(5).Border(1, tokBorder)
		if v, ok := parseHex(s); ok {
			box = box.Fill(v)
		}
		return box
	})
	label := colorLabel(c)
	// Tighter than a setting's row: a theme has some fifty colours.
	return ggui.Box(ggui.Row(
		ggui.Expanded(ggui.Text(label).Size(13)),
		swatch,
		ggui.Box(ui.TextField(value).Name(label)).Width(96),
	).Gap(8).Align(ggui.AlignCenter)).Pad(5, 12, 5, 16)
}

// colorLabel is a colour's name in the window's language.
func colorLabel(c paletteColor) string {
	if strings.HasPrefix(c.key, "ansi-bright-") {
		return Tf("Bright %s", T(c.label))
	}
	return T(c.label)
}

// themeSwatch shows a palette's colours, for a theme that cannot be
// changed: the surfaces in order, then the terminal's sixteen.
func themeSwatch(p palette) ggui.Widget {
	var surfaces []ggui.Widget
	for _, c := range []color.RGBA{p.Sidebar, p.Surface0, p.Surface1, p.Surface2, p.Surface3} {
		surfaces = append(surfaces, ggui.Box().Size(40, 26).Radius(6).Fill(c).Border(1, tokBorder))
	}
	var ansi []ggui.Widget
	for _, c := range p.ANSI {
		ansi = append(ansi, ggui.Box().Size(20, 20).Radius(4).Fill(hex(c)))
	}
	return card(
		row(T("Surfaces"), T("Duplicate the theme to change its colours."), ggui.Row(surfaces...).Gap(6)),
		row(T("Terminal colours"), "", ggui.Row(ansi...).Gap(4)),
	)
}

// setThemeColor changes one colour of a theme of the user's, and saves it.
func (m *model) setThemeColor(theme string, c paletteColor, v color.RGBA) {
	themes := ggui.Peek(m.Themes)
	p, ok := themes[theme]
	if !ok || c.get(p) == v {
		return
	}
	c.set(&p, v)
	themes = maps.Clone(themes)
	themes[theme] = p
	m.Themes.Set(themes)
	if err := saveTheme(theme, p); err != nil {
		m.toaster.Push(ui.Toast(T("Could not save the theme"), err.Error()).Destructive())
	}
}

// duplicateTheme makes a theme of the user's from the one in use, and
// chooses it.
func (m *model) duplicateTheme() {
	themes := ggui.Peek(m.Themes)
	p := paletteFor(ggui.Peek(m.Prefs).Theme, ggui.Peek(ggui.SystemDark()), themes)
	name := newThemeName(themes)
	p.Name = name
	if err := saveTheme(name, p); err != nil {
		m.toaster.Push(ui.Toast(T("Could not save the theme"), err.Error()).Destructive())
		return
	}
	themes = maps.Clone(themes)
	themes[name] = p
	m.Themes.Set(themes)
	m.Prefs.Update(func(pr prefs) prefs { pr.Theme = name; return pr })
}

// deleteTheme removes the theme of the user's in use, and goes back to the
// system's.
func (m *model) deleteTheme() {
	name := ggui.Peek(m.Prefs).Theme
	if isBuiltinTheme(name) {
		return
	}
	if err := removeTheme(name); err != nil && !os.IsNotExist(err) {
		m.toaster.Push(ui.Toast(T("Could not delete the theme"), err.Error()).Destructive())
		return
	}
	m.Prefs.Update(func(pr prefs) prefs { pr.Theme = "system"; return pr })
	m.reloadThemes()
}

// openThemesFolder shows the themes folder in the desktop's file manager.
func openThemesFolder() {
	if dir, err := themesDir(); err == nil && os.MkdirAll(dir, 0o700) == nil {
		_ = runtime.OpenURL(dir)
	}
}

// backdropRows are a background image's settings: the file, and, once
// there is one, where it goes and how much of it shows through the theme,
// beside a preview of the window with it.
func backdropRows(m *model, title, hint string, slot backdropSlot) []ggui.Widget {
	path := prefField(m, func(p *prefs) *string { return &slot.of(p).Image })
	browse := browseButton(path, func(d runtime.Dialogs) (string, error) {
		return d.OpenFile(runtime.FileDialog{Title: title, Filters: []runtime.FileFilter{
			{Name: T("Images"), Extensions: []string{"png", "jpg", "jpeg", "gif", "webp"}},
		}})
	})
	none := ggui.Map(path, func(s string) bool { return s == "" })
	clear := ui.Button(T("Remove"), func() { path.Set("") }).Ghost().BindDisabled(none)
	return []ggui.Widget{
		stackedRow(title, hint, ggui.Row(
			ggui.Expanded(ui.TextField(path).Placeholder("~/Pictures/background.jpg").Name(title)),
			browse, clear,
		).Gap(8)),
		ggui.Reactive(func() ggui.Widget {
			if none.Get() {
				return ggui.Box()
			}
			return backdropPlacement(m, slot)
		}),
	}
}

// Labels of the fits and positions, translated where shown.
var (
	backdropFitLabels = map[string]string{
		"cover": "Cover", "contain": "Contain", "fill": "Stretch", "repeat": "Tile",
	}
	backdropPositionLabels = map[string]string{
		"top-left": "Top left", "top": "Top", "top-right": "Top right",
		"left": "Left", "center": "Centre", "right": "Right",
		"bottom-left": "Bottom left", "bottom": "Bottom", "bottom-right": "Bottom right",
	}
)

// The preview's width, and the bounds of its height, which otherwise takes
// the shape of what the image was last under.
const (
	backdropPreviewWidth                               = 176
	backdropPreviewMinHeight, backdropPreviewMaxHeight = 88, 132
)

// backdropPlacement is where the slot's image goes: a preview of it whose
// nine zones pick its position, beside its fit, size and margins, and
// how much of it shows through the theme's colours.
func backdropPlacement(m *model, slot backdropSlot) ggui.Widget {
	length := func(f func(*backdrop) *float64, placeholder, name string) ggui.Widget {
		return lengthField(prefField(m, func(p *prefs) *float64 { return f(slot.of(p)) }), placeholder, name)
	}
	line := func(label string, controls ...ggui.Widget) ggui.Widget {
		return ggui.Row(append([]ggui.Widget{ggui.Box(muted(ggui.Text(label).Size(12.5))).Width(52)}, controls...)...).
			Gap(6).Align(ggui.AlignCenter)
	}
	opacity := prefField(m, func(p *prefs) *float64 { return &slot.of(p).Opacity })
	fit := ui.ToggleGroup(prefField(m, func(p *prefs) *string { return &slot.of(p).Fit })).
		Options(backdropFits).Format(func(s string) string { return T(backdropFitLabels[s]) }).Name(T("Fit"))
	// Reset puts all but the image back as a new one starts.
	reset := ui.Button(T("Reset"), func() {
		m.Prefs.Update(func(p prefs) prefs {
			b := slot.of(&p)
			image := b.Image
			*b = defaultBackdrop()
			b.Image = image
			return p
		})
	}).Ghost()
	controls := ggui.Column(
		line(T("Fit"), fit),
		line(T("Size"), length(func(b *backdrop) *float64 { return &b.Width }, T("Width"), T("Width")),
			length(func(b *backdrop) *float64 { return &b.Height }, T("Height"), T("Height")),
			ggui.Box(muted(ggui.Text(T("Margins")).Size(12.5))).Pad(0, 0, 0, 14),
			length(func(b *backdrop) *float64 { return &b.MarginX }, T("Horizontal"), T("Horizontal margin")),
			length(func(b *backdrop) *float64 { return &b.MarginY }, T("Vertical"), T("Vertical margin"))),
		line(T("Opacity"),
			ggui.Box(ui.Slider(opacity, 0, 1).Step(0.05).Name(T("Image opacity"))).Width(200),
			ggui.Box(ggui.View(opacity, func(o float64) ggui.Widget {
				return muted(ggui.Text(Tf("%d%%", int(o*100+0.5))).Size(12.5))
			})).Width(36),
			ggui.Spacer(), reset),
	).Gap(8).Stretch()
	return ggui.Box(ggui.Row(backdropPreview(m, slot), ggui.Expanded(controls)).Gap(20).Align(ggui.AlignCenter)).Pad(12, 16)
}

// lengthField edits a length in pixels as plain digits, empty for 0, which
// shows the placeholder naming it: a size that follows the fit, or no
// margin. What is not a number is not taken.
func lengthField(b ggui.Binding[float64], placeholder, name string) ggui.Widget {
	text := ggui.Bind(
		func() string {
			if v := b.Get(); v > 0 {
				return strconv.Itoa(int(v))
			}
			return ""
		},
		func(s string) {
			if s = strings.TrimSpace(s); s == "" {
				b.Set(0)
			} else if v, err := strconv.Atoi(s); err == nil {
				b.Set(float64(min(max(v, 0), maxBackdropLength)))
			}
		},
	)
	return ggui.Box(ui.TextField(text).Placeholder(placeholder).Name(name)).Width(64)
}

// backdropPreview is the slot's image over the theme as it would be under
// the window or a session, shrunk to the preview's width; a click in one
// of its nine zones puts the image there.
func backdropPreview(m *model, slot backdropSlot) ggui.Widget {
	seen := m.backdropSeen[slot]
	if seen.W <= 0 || seen.H <= 0 {
		seen = ggui.Sz(1280, 800)
	}
	scale := backdropPreviewWidth / seen.W
	height := min(max(seen.H*scale, backdropPreviewMinHeight), backdropPreviewMaxHeight)
	position := prefField(m, func(p *prefs) *string { return &slot.of(p).Position })
	view := ggui.Stack(ggui.Box().Fill(tokSolidBg), backdropLayer(m, slot, scale), backdropScrim(m, slot), positionZones(position))
	return ggui.Box(view).Size(backdropPreviewWidth, height).Border(1, tokBorder).Radius(6)
}

// positionZones are nine buttons in thirds of their room, one a position,
// each a dot over the image, the chosen one larger and coloured.
func positionZones(value ggui.Binding[string]) ggui.Widget {
	var z thirds
	for i, pos := range backdropPositions {
		chosen := ggui.Map(value, func(s string) bool { return s == pos })
		dot := ggui.Reactive(func() ggui.Widget {
			on := chosen.Get()
			d := pick(on, 9.0, 5.0)
			return ggui.Box().Size(d, d).Radius(d/2).Border(1, color.RGBA{A: 0x70}).
				Fill(pick[color.Color](on, tokAccentBright, color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xb0}))
		})
		b := ui.ButtonOf(ggui.Center(dot), func() { value.Set(pos) }).Ghost().Pad(0).Radius(4).
			Name(T(backdropPositionLabels[pos]))
		z[i] = ui.Tooltip(b, T(backdropPositionLabels[pos]))
	}
	return &z
}

// thirds lays nine widgets out in a three-by-three grid filling its room.
type thirds [9]ggui.Widget

func (t *thirds) Layout(c ggui.Constraints, env ggui.Env) ggui.Size {
	size := c.Max()
	for _, w := range t {
		w.Layout(ggui.Tight(ggui.Sz(size.W/3, size.H/3)), env)
	}
	return size
}

func (t *thirds) Paint(dst *ggui.Canvas, r ggui.Rect) {
	w, h := r.Size.W/3, r.Size.H/3
	for i, c := range t {
		dst.Paint(c, ggui.Rct(ggui.Pt(r.Origin.X+float64(i%3)*w, r.Origin.Y+float64(i/3)*h), ggui.Sz(w, h)))
	}
}
