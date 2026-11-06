// Package skin is how the window looks: its palettes, the theme tokens
// widgets are coloured with, and the user's themes on disk.
package skin

import (
	"image/color"

	"github.com/ironpark/ggui"
	uitheme "github.com/ironpark/ggui/ui/theme"
	"github.com/ironpark/gostty"
)

// Palette is one of Paseo's colour schemes: surfaces stepping up from the
// workspace, the sidebar a shade apart, and an ANSI palette for terminals.
type Palette struct {
	Name                           string
	Dark                           bool
	Surface0, Surface1, Surface2   color.RGBA // workspace; hovered rows; selected rows, inputs, popovers
	Surface3                       color.RGBA // scrollbar thumbs, strong separators
	Sidebar                        color.RGBA
	SidebarHover, SidebarSelected  color.RGBA
	Fg, Muted, ExtraMuted          color.RGBA
	Border, BorderAccent           color.RGBA
	Accent, AccentBright           color.RGBA
	Highlight                      color.RGBA // the item under the pointer in a menu or list, over a popover
	Primary, PrimaryFg             color.RGBA
	Destructive, Ring              color.RGBA
	DotRunning, DotExited, DotDown color.RGBA
	DotWaiting, DotDone            color.RGBA // an agent asking the user; one that finished unseen
	Selection                      color.RGBA // selected terminal text
	DiffAdded, DiffRemoved         color.RGBA // changed lines' counts and boxes
	DiffModified                   color.RGBA
	DiffAddedBg, DiffRemovedBg     color.RGBA // added and removed lines' tint
	ANSI                           [16]uint32
}

var Dark = Palette{
	Name: "dark", Dark: true,
	Surface0: Hex(0x181B1A), Surface1: Hex(0x1E2120), Surface2: Hex(0x272A29), Surface3: Hex(0x434645),
	Sidebar: Hex(0x141716), SidebarHover: Hex(0x1E2120), SidebarSelected: Hex(0x272A29),
	Fg: Hex(0xFAFAFA), Muted: Hex(0xA1A5A4), ExtraMuted: Hex(0x717574),
	Border: Hex(0x252B2A), BorderAccent: Hex(0x2F3534),
	Accent: Hex(0x20744A), AccentBright: Hex(0x7CCBA0), Highlight: Hex(0x383C3B),
	Primary: Hex(0xFAFAFA), PrimaryFg: Hex(0x181B1A),
	Destructive: Hex(0xC64F43), Ring: Hex(0xD4D4D8),
	DotRunning: Hex(0x35C264), DotExited: Hex(0x717574), DotDown: Hex(0xF7796D),
	DotWaiting: Hex(0xE2B04A), DotDone: Hex(0x6A9DE0),
	Selection: Hex(0x3A5A48),
	DiffAdded: Hex(0x6CC08A), DiffRemoved: Hex(0xE57E7E), DiffModified: Hex(0xE2C06A),
	DiffAddedBg: Hex(0x1D3326), DiffRemovedBg: Hex(0x3A2224),
	ANSI: [16]uint32{
		0x141716, 0xE07070, 0x5DBA80, 0xD4A44A, 0x6A9DE0, 0xB07AD0, 0x4AABB8, 0xD4D4D8,
		0x434645, 0xE89090, 0x7ECF9A, 0xE0BE6E, 0x8AB4E8, 0xC49AE0, 0x6EC2CC, 0xF0F0F2,
	},
}

var Light = Palette{
	Name:     "light",
	Surface0: Hex(0xFFFFFF), Surface1: Hex(0xFAFAFA), Surface2: Hex(0xF4F4F5), Surface3: Hex(0xE4E4E7),
	Sidebar: Hex(0xF4F4F5), SidebarHover: Hex(0xE4E4E7), SidebarSelected: Hex(0xD4D4D8),
	Fg: Hex(0x1A1A1E), Muted: Hex(0x71717A), ExtraMuted: Hex(0xA1A1AA),
	Border: Hex(0xE4E4E7), BorderAccent: Hex(0xD4D4D8),
	Accent: Hex(0x20744A), AccentBright: Hex(0x239956), Highlight: Hex(0xE4E4E7),
	Primary: Hex(0x18181B), PrimaryFg: Hex(0xFAFAFA),
	Destructive: Hex(0xB04138), Ring: Hex(0x18181B),
	DotRunning: Hex(0x299F51), DotExited: Hex(0xA1A1AA), DotDown: Hex(0xF12E2F),
	DotWaiting: Hex(0xD97706), DotDone: Hex(0x2563EB),
	Selection: Hex(0xC8E6D2),
	DiffAdded: Hex(0x16A34A), DiffRemoved: Hex(0xDC2626), DiffModified: Hex(0xCA8A04),
	DiffAddedBg: Hex(0xE6F4EA), DiffRemovedBg: Hex(0xFBE9E9),
	ANSI: [16]uint32{
		0x1A1A1E, 0xDC2626, 0x16A34A, 0xCA8A04, 0x2563EB, 0x9333EA, 0x0891B2, 0xD4D4D8,
		0x3F3F46, 0xEF4444, 0x22C55E, 0xF59E0B, 0x3B82F6, 0xA855F7, 0x06B6D4, 0xFAFAFA,
	},
}

func Hex(v uint32) color.RGBA {
	return color.RGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 0xff}
}

// Colours are given to widgets as theme tokens, which resolve when the
// widget is laid out, so switching the theme recolours the window without
// rebuilding it. Paseo's extra steps ride on the theme as variables.
//
// The workspace's and the sidebar's fills are variables of their own, which
// a background image clears (gui's backdrop.go); SolidBg is the workspace's
// colour that stays, for what must cover what is under it.
var (
	Surface0     = uitheme.Var("surface-0")
	Surface1     = uitheme.Card
	Surface2     = uitheme.Muted
	Surface3     = uitheme.Var("surface-3")
	Sidebar      = uitheme.Var("sidebar-fill")
	SolidBg      = uitheme.Bg
	SidebarSel   = uitheme.Var("sidebar-selected")
	Fg           = uitheme.Fg
	Muted        = uitheme.MutedFg
	ExtraMuted   = uitheme.Var("extra-muted")
	Border       = uitheme.Border
	BorderAccent = uitheme.InputBorder
	AccentBright = uitheme.Var("accent-bright")
	DotRunning   = uitheme.Var("dot-running")
	DotExited    = uitheme.Var("dot-exited")
	DotDown      = uitheme.Var("dot-down")
	DotWaiting   = uitheme.Var("dot-waiting")
	DotDone      = uitheme.Var("dot-done")

	DiffAdded     = uitheme.Var("diff-added")
	DiffRemoved   = uitheme.Var("diff-removed")
	DiffModified  = uitheme.Var("diff-modified")
	DiffAddedBg   = uitheme.Var("diff-added-bg")
	DiffRemovedBg = uitheme.Var("diff-removed-bg")
)

// Key carries the palette on the theme, for the terminal, which
// paints with concrete colours.
var Key = ggui.NewEnvKey[Palette]("mako palette")

// Theme maps a palette onto ggui's semantic tokens, so the stock
// controls (buttons, selects, dialogs, fields) match the custom chrome.
func Theme(p Palette) uitheme.Theme {
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
	// Accent is what ggui lights the item under the pointer with, in menus,
	// selects and lists; it must stand off the popover they open on.
	t.Accent, t.AccentFg = p.Highlight, p.Fg
	t.Destructive, t.DestructiveFg = p.Destructive, Hex(0xFFFFFF)
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
	set("surface-0", p.Surface0)
	set("sidebar-fill", p.Sidebar)
	set("surface-3", p.Surface3)
	set("sidebar-selected", p.SidebarSelected)
	set("extra-muted", p.ExtraMuted)
	set("accent-bright", p.AccentBright)
	set("dot-running", p.DotRunning)
	set("dot-exited", p.DotExited)
	set("dot-down", p.DotDown)
	set("dot-waiting", p.DotWaiting)
	set("dot-done", p.DotDone)
	set("diff-added", p.DiffAdded)
	set("diff-removed", p.DiffRemoved)
	set("diff-modified", p.DiffModified)
	set("diff-added-bg", p.DiffAddedBg)
	set("diff-removed-bg", p.DiffRemovedBg)
	return t.Set(Key, p)
}

// Pick picks the palette a theme preference names: one of the
// user's themes, light, dark, or the one that follows the desktop's
// appearance for "system" and a theme that is gone.
func Pick(pref string, systemDark bool, custom map[string]Palette) Palette {
	switch pref {
	case "light":
		return Light
	case "dark":
		return Dark
	}
	if p, ok := custom[pref]; ok {
		return p
	}
	if systemDark {
		return Dark
	}
	return Light
}

// ApplyTerminal gives a terminal the window's colours: its defaults and the
// sixteen ANSI entries. The 256-colour cube and grays stay standard.
func ApplyTerminal(vt *gostty.Terminal, p Palette) {
	for i, c := range p.ANSI {
		_ = vt.SetDefaultPaletteColor(uint8(i), gostty.RGBFromUint32(c))
	}
	vt.ResetPalette()
	vt.SetDefaultBackgroundColor(RGB(p.Surface0))
	vt.SetDefaultForegroundColor(RGB(p.Fg))
}

func RGB(c color.RGBA) gostty.RGB { return gostty.NewRGB(c.R, c.G, c.B) }
