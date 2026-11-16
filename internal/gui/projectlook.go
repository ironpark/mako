package gui

import (
	"hash/fnv"
	"image/color"
	"maps"
	"math/rand/v2"
	"slices"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"
	"github.com/ironpark/ggui/widget"

	"github.com/ironpark/mako/internal/gui/skin"
)

// A project section's look: the icon the sidebar marks it with, and its
// colour, the user's to pick from its icon. A section with none picked
// draws one from its ID, a shape and a colour, so it keeps the same one;
// a project made from the sidebar's + is given one at random and keeps it.
// The looks are part of the workspace (spaceList), shared as the names are.

// projectLook is a section's icon and colour, by name among lookIcons and
// lookColors.
type projectLook struct {
	Icon  string `json:"icon,omitempty"`
	Color string `json:"color,omitempty"`
}

// lookShapes are the icons a look drawn for a section is one of: a
// triangle, square, pentagon or hexagon, upright or turned 45°.
var lookShapes = []string{
	"shape-triangle", "shape-triangle-45", "shape-square", "shape-square-45",
	"shape-pentagon", "shape-pentagon-45", "shape-hexagon", "shape-hexagon-45",
}

// lookIcon is an icon a look offers, with its name in English for T.
type lookIcon struct{ Icon, Label string }

// lookIcons are the icons a section's look offers, the shapes first, each
// with its name in English for T.
var lookIcons = []lookIcon{
	{"shape-triangle", "Triangle"}, {"shape-triangle-45", "Triangle, turned"},
	{"shape-square", "Square"}, {"shape-square-45", "Diamond"},
	{"shape-pentagon", "Pentagon"}, {"shape-pentagon-45", "Pentagon, turned"},
	{"shape-hexagon", "Hexagon"}, {"shape-hexagon-45", "Hexagon, turned"},
	{"folder", "Folder"}, {"package", "Package"}, {"code-xml", "Code"}, {"terminal", "Terminal"},
	{"globe", "Web"}, {"book-open", "Book"}, {"rocket", "Rocket"}, {"flask-conical", "Flask"},
	{"database", "Database"}, {"gamepad-2", "Game"}, {"star", "Star"}, {"heart", "Heart"},
}

// lookColor is a colour a look offers, by name, with its label in English
// for T.
type lookColor struct {
	Name, Label string
	Color       color.NRGBA
}

// lookColors are the colours a look offers, by name, each with its label
// in English for T. A drawn look is never the first, grey.
var lookColors = []lookColor{
	{"gray", "Grey", color.NRGBA{0x9a, 0xa0, 0xa6, 0xff}},
	{"orange", "Orange", color.NRGBA{0xe0, 0x8a, 0x5f, 0xff}},
	{"amber", "Amber", color.NRGBA{0xd9, 0xb4, 0x50, 0xff}},
	{"green", "Green", color.NRGBA{0x5f, 0xbf, 0x7a, 0xff}},
	{"teal", "Teal", color.NRGBA{0x4f, 0xb8, 0xb0, 0xff}},
	{"blue", "Blue", color.NRGBA{0x6a, 0xa7, 0xff, 0xff}},
	{"purple", "Purple", color.NRGBA{0xa9, 0x93, 0xff, 0xff}},
}

// drawnLook is the look a seed draws: one of the shapes, in a colour but
// grey.
func drawnLook(seed uint64) projectLook {
	shape := lookShapes[seed%uint64(len(lookShapes))]
	c := lookColors[1+int(seed/uint64(len(lookShapes))%uint64(len(lookColors)-1))]
	return projectLook{Icon: shape, Color: c.Name}
}

// randomLook is a look drawn at random, for a new project.
func randomLook() projectLook { return drawnLook(rand.Uint64()) }

// lookOf is the look of section key: what was picked for it, filled in
// with what its key draws.
func lookOf(looks map[string]projectLook, key string) projectLook {
	h := fnv.New64a()
	h.Write([]byte(key))
	l := drawnLook(h.Sum64())
	if own, ok := looks[key]; ok {
		if own.Icon != "" && slices.ContainsFunc(lookIcons, func(i lookIcon) bool { return i.Icon == own.Icon }) {
			l.Icon = own.Icon
		}
		if lookColorIndex(own.Color) >= 0 {
			l.Color = own.Color
		}
	}
	return l
}

// lookColorIndex is the index of the colour called name, -1 for none.
func lookColorIndex(name string) int {
	return slices.IndexFunc(lookColors, func(c lookColor) bool { return c.Name == name })
}

// color is the look's colour.
func (l projectLook) color() color.Color {
	return lookColors[max(lookColorIndex(l.Color), 0)].Color
}

// glyph is the look's icon in its colour, at size.
func (l projectLook) glyph(size float64) ggui.Widget {
	return icon(l.Icon).Size(size).Color(l.color())
}

// setLook keeps look for section key, in the workspace.
func (m *model) setLook(key string, look projectLook) {
	m.Spaces.Update(func(l spaceList) spaceList {
		l.Looks = maps.Clone(l.Looks)
		if l.Looks == nil {
			l.Looks = map[string]projectLook{}
		}
		l.Looks[key] = look
		return l
	})
}

// lookButton is a section's icon, which opens the picker of its look.
func lookButton(m *model, key, name string, look projectLook) ggui.Widget {
	picker := ui.PopoverOf(look.glyph(16), lookPicker(m, key, look)).
		Name(Tf("Icon of %s", name)).Width(248).Pad(10).Align(widget.PopupStart).Key("look:" + key)
	picker.Trigger().Ghost().Pad(5).Radius(6)
	return borderless(picker)
}

// lookPicker picks a section's icon, from a grid of them, and its colour.
func lookPicker(m *model, key string, look projectLook) ggui.Widget {
	const perRow = 6
	var rows []ggui.Widget
	var row []ggui.Widget
	for _, ic := range lookIcons {
		on := ic.Icon == look.Icon
		face := widget.Box(widget.Center(icon(ic.Icon).Size(16).Color(look.color()))).Size(32, 32).Radius(6)
		if on {
			face = face.Fill(skin.Surface3).Border(1, skin.Border)
		}
		next := look
		next.Icon = ic.Icon
		row = append(row, ui.ButtonOf(face, func() { m.setLook(key, next) }).Ghost().Pad(0).Radius(6).Name(T(ic.Label)))
		if len(row) == perRow {
			rows = append(rows, widget.Row(row...).Gap(4))
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, widget.Row(row...).Gap(4))
	}
	swatches := make([]ggui.Widget, len(lookColors))
	for i, c := range lookColors {
		face := widget.Box(widget.Center(widget.Box().Size(14, 14).Radius(7).Fill(c.Color))).Size(24, 24).Radius(12)
		if c.Name == look.Color {
			face = face.Border(1.5, skin.Fg)
		}
		next := look
		next.Color = c.Name
		swatches[i] = ui.ButtonOf(face, func() { m.setLook(key, next) }).Ghost().Pad(0).Radius(12).Name(T(c.Label))
	}
	heading := func(s string) ggui.Widget { return muted(widget.Text(s).Size(11.5)) }
	return widget.Column(
		heading(T("Icon")),
		widget.Column(rows...).Gap(4),
		heading(T("Colour")),
		widget.Row(swatches...).Gap(6),
	).Gap(8)
}
