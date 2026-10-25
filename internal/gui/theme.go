package gui

import (
	"image/color"

	"github.com/ironpark/ggui"
	uitheme "github.com/ironpark/ggui/ui/theme"
	"github.com/ironpark/gostty"
)

// palette is one of Paseo's colour schemes: surfaces stepping up from the
// workspace, the sidebar a shade apart, and an ANSI palette for terminals.
type palette struct {
	Name                           string
	Dark                           bool
	Surface0, Surface1, Surface2   color.RGBA // workspace; hovered rows; selected rows, inputs, popovers
	Surface3                       color.RGBA // scrollbar thumbs, strong separators
	Sidebar                        color.RGBA
	SidebarHover, SidebarSelected  color.RGBA
	Fg, Muted, ExtraMuted          color.RGBA
	Border, BorderAccent           color.RGBA
	Accent, AccentBright           color.RGBA
	Primary, PrimaryFg             color.RGBA
	Destructive, Ring              color.RGBA
	DotRunning, DotExited, DotDown color.RGBA
	Selection                      color.RGBA // selected terminal text
	DiffAdded, DiffRemoved         color.RGBA // changed lines' counts and boxes
	DiffModified                   color.RGBA
	DiffAddedBg, DiffRemovedBg     color.RGBA // added and removed lines' tint
	ANSI                           [16]uint32
}

var darkPalette = palette{
	Name: "dark", Dark: true,
	Surface0: hex(0x181B1A), Surface1: hex(0x1E2120), Surface2: hex(0x272A29), Surface3: hex(0x434645),
	Sidebar: hex(0x141716), SidebarHover: hex(0x1E2120), SidebarSelected: hex(0x272A29),
	Fg: hex(0xFAFAFA), Muted: hex(0xA1A5A4), ExtraMuted: hex(0x717574),
	Border: hex(0x252B2A), BorderAccent: hex(0x2F3534),
	Accent: hex(0x20744A), AccentBright: hex(0x7CCBA0),
	Primary: hex(0xFAFAFA), PrimaryFg: hex(0x181B1A),
	Destructive: hex(0xC64F43), Ring: hex(0xD4D4D8),
	DotRunning: hex(0x35C264), DotExited: hex(0x717574), DotDown: hex(0xF7796D),
	Selection: hex(0x3A5A48),
	DiffAdded: hex(0x6CC08A), DiffRemoved: hex(0xE57E7E), DiffModified: hex(0xE2C06A),
	DiffAddedBg: hex(0x1D3326), DiffRemovedBg: hex(0x3A2224),
	ANSI: [16]uint32{
		0x141716, 0xE07070, 0x5DBA80, 0xD4A44A, 0x6A9DE0, 0xB07AD0, 0x4AABB8, 0xD4D4D8,
		0x434645, 0xE89090, 0x7ECF9A, 0xE0BE6E, 0x8AB4E8, 0xC49AE0, 0x6EC2CC, 0xF0F0F2,
	},
}

var lightPalette = palette{
	Name:     "light",
	Surface0: hex(0xFFFFFF), Surface1: hex(0xFAFAFA), Surface2: hex(0xF4F4F5), Surface3: hex(0xE4E4E7),
	Sidebar: hex(0xF4F4F5), SidebarHover: hex(0xE4E4E7), SidebarSelected: hex(0xD4D4D8),
	Fg: hex(0x1A1A1E), Muted: hex(0x71717A), ExtraMuted: hex(0xA1A1AA),
	Border: hex(0xE4E4E7), BorderAccent: hex(0xD4D4D8),
	Accent: hex(0x20744A), AccentBright: hex(0x239956),
	Primary: hex(0x18181B), PrimaryFg: hex(0xFAFAFA),
	Destructive: hex(0xB04138), Ring: hex(0x18181B),
	DotRunning: hex(0x299F51), DotExited: hex(0xA1A1AA), DotDown: hex(0xF12E2F),
	Selection: hex(0xC8E6D2),
	DiffAdded: hex(0x16A34A), DiffRemoved: hex(0xDC2626), DiffModified: hex(0xCA8A04),
	DiffAddedBg: hex(0xE6F4EA), DiffRemovedBg: hex(0xFBE9E9),
	ANSI: [16]uint32{
		0x1A1A1E, 0xDC2626, 0x16A34A, 0xCA8A04, 0x2563EB, 0x9333EA, 0x0891B2, 0xD4D4D8,
		0x3F3F46, 0xEF4444, 0x22C55E, 0xF59E0B, 0x3B82F6, 0xA855F7, 0x06B6D4, 0xFAFAFA,
	},
}

func hex(v uint32) color.RGBA {
	return color.RGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 0xff}
}

// Colours are given to widgets as theme tokens, which resolve when the
// widget is laid out, so switching the theme recolours the window without
// rebuilding it. Paseo's extra steps ride on the theme as variables.
var (
	tokSurface0     = uitheme.Bg
	tokSurface1     = uitheme.Card
	tokSurface2     = uitheme.Muted
	tokSurface3     = uitheme.Var("surface-3")
	tokSidebar      = uitheme.Sidebar
	tokSidebarSel   = uitheme.Var("sidebar-selected")
	tokFg           = uitheme.Fg
	tokMuted        = uitheme.MutedFg
	tokExtraMuted   = uitheme.Var("extra-muted")
	tokBorder       = uitheme.Border
	tokBorderAccent = uitheme.InputBorder
	tokAccentBright = uitheme.Var("accent-bright")
	tokDotRunning   = uitheme.Var("dot-running")
	tokDotExited    = uitheme.Var("dot-exited")
	tokDotDown      = uitheme.Var("dot-down")

	tokDiffAdded     = uitheme.Var("diff-added")
	tokDiffRemoved   = uitheme.Var("diff-removed")
	tokDiffModified  = uitheme.Var("diff-modified")
	tokDiffAddedBg   = uitheme.Var("diff-added-bg")
	tokDiffRemovedBg = uitheme.Var("diff-removed-bg")
)

// paletteKey carries the palette on the theme, for the terminal, which
// paints with concrete colours.
var paletteKey = ggui.NewEnvKey[palette]("sanbo palette")

// themeFor maps a palette onto ggui's semantic tokens, so the stock
// controls (buttons, selects, dialogs, fields) match the custom chrome.
func themeFor(p palette) uitheme.Theme {
	t := uitheme.Default()
	if p.Dark {
		t = uitheme.Dark()
	}
	t.Bg, t.Fg = p.Surface0, p.Fg
	t.Card, t.CardFg = p.Surface1, p.Fg
	t.Popover, t.PopoverFg = p.Surface0, p.Fg
	if p.Dark {
		t.Popover = p.Surface2
	}
	t.Primary, t.PrimaryFg = p.Primary, p.PrimaryFg
	t.Secondary, t.SecondaryFg = p.Surface2, p.Fg
	t.Muted, t.MutedFg = p.Surface2, p.Muted
	t.Accent, t.AccentFg = p.Surface2, p.Fg
	t.Destructive, t.DestructiveFg = p.Destructive, hex(0xFFFFFF)
	t.Border = p.Border
	t.InputBorder = p.BorderAccent
	t.Input = p.Surface1
	t.Ring = p.Ring
	t.Selection = p.Selection
	t.Sidebar, t.SidebarFg = p.Sidebar, p.Fg
	t.SidebarAccent, t.SidebarAccentFg = p.SidebarHover, p.Fg
	t.SidebarBorder = p.Border
	t.Radius = 8
	t.Text.Size = 13
	t.Caption.Size = 12
	set := func(name string, c color.Color) { t = t.Set(uitheme.CSSColor(name), c) }
	set("surface-3", p.Surface3)
	set("sidebar-selected", p.SidebarSelected)
	set("extra-muted", p.ExtraMuted)
	set("accent-bright", p.AccentBright)
	set("dot-running", p.DotRunning)
	set("dot-exited", p.DotExited)
	set("dot-down", p.DotDown)
	set("diff-added", p.DiffAdded)
	set("diff-removed", p.DiffRemoved)
	set("diff-modified", p.DiffModified)
	set("diff-added-bg", p.DiffAddedBg)
	set("diff-removed-bg", p.DiffRemovedBg)
	return t.Set(paletteKey, p)
}

// paletteFor picks the palette a theme preference names; "system" follows
// the desktop's appearance.
func paletteFor(pref string, systemDark bool) palette {
	switch pref {
	case "light":
		return lightPalette
	case "dark":
		return darkPalette
	}
	if systemDark {
		return darkPalette
	}
	return lightPalette
}

// applyPalette gives a terminal the window's colours: its defaults and the
// sixteen ANSI entries. The 256-colour cube and grays stay standard.
func applyPalette(vt *gostty.Terminal, p palette) {
	for i, c := range p.ANSI {
		_ = vt.SetDefaultPaletteColor(uint8(i), gostty.RGBFromUint32(c))
	}
	_ = vt.ResetPalette()
	_ = vt.SetDefaultBackgroundColor(rgb(p.Surface0))
	_ = vt.SetDefaultForegroundColor(rgb(p.Fg))
}

func rgb(c color.RGBA) gostty.RGB { return gostty.RGB{R: c.R, G: c.G, B: c.B} }
