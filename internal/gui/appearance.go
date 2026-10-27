package gui

import (
	"image/color"
	"maps"
	"os"
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

// backdropRows are a background image's settings: the file, and how much
// of it shows through the theme.
func backdropRows(m *model, title, hint string, slot func(*prefs) *backdrop) []ggui.Widget {
	path := prefField(m, func(p *prefs) *string { return &slot(p).Image })
	opacity := prefField(m, func(p *prefs) *float64 { return &slot(p).Opacity })
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
		row(T("Image opacity"), T("How much of the image shows through the theme's colours."), ggui.Row(
			ggui.Box(ui.Slider(opacity, 0, 1).Step(0.05).Name(T("Image opacity")).BindDisabled(none)).Width(160),
			ggui.Box(ggui.View(opacity, func(o float64) ggui.Widget {
				return muted(ggui.Text(Tf("%d%%", int(o*100+0.5))).Size(12.5))
			})).Width(36),
		).Gap(10).Align(ggui.AlignCenter)),
	}
}
