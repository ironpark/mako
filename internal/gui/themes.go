package gui

import (
	"encoding/json"
	"fmt"
	"image/color"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	uitheme "github.com/ironpark/ggui/ui/theme"

	"github.com/ironpark/mako/internal/config"
)

// The user's themes are files in the themes folder beside gui.json, one a
// theme, named after the file: the palette they start from, light or dark,
// and their colours by name, as "#rrggbb". A colour a file leaves out is
// its base's.
//
//	{"base": "dark", "colors": {"accent-bright": "#7ccba0", "ansi-red": "#e07070"}}
type themeFile struct {
	Base   string            `json:"base"`
	Colors map[string]string `json:"colors,omitempty"`
}

// paletteColor is one colour of a palette as a theme names it, the label
// the editor gives it, and the group it shows under.
type paletteColor struct {
	key, label, group string
	get               func(palette) color.RGBA
	set               func(*palette, color.RGBA)
}

func field(key, label, group string, f func(*palette) *color.RGBA) paletteColor {
	return paletteColor{key: key, label: label, group: group,
		get: func(p palette) color.RGBA { return *f(&p) },
		set: func(p *palette, c color.RGBA) { *f(p) = c }}
}

// ansiNames are the terminal's eight colours, whose bright halves follow.
var ansiNames = [8]string{"Black", "Red", "Green", "Yellow", "Blue", "Magenta", "Cyan", "White"}

// The editor's groups. paletteColors lists each group's colours together,
// in the order the editor shows them.
const (
	groupSurfaces = "Surfaces"
	groupText     = "Text and lines"
	groupAccents  = "Accents"
	groupStatus   = "Status"
	groupDiff     = "Changes"
	groupTerminal = "Terminal colours"
)

// paletteColors are every colour a theme sets.
var paletteColors = func() []paletteColor {
	colors := []paletteColor{
		field("surface-0", "Workspace", groupSurfaces, func(p *palette) *color.RGBA { return &p.Surface0 }),
		field("surface-1", "Raised", groupSurfaces, func(p *palette) *color.RGBA { return &p.Surface1 }),
		field("surface-2", "Selected and inputs", groupSurfaces, func(p *palette) *color.RGBA { return &p.Surface2 }),
		field("surface-3", "Scrollbars", groupSurfaces, func(p *palette) *color.RGBA { return &p.Surface3 }),
		field("sidebar", "Sidebar", groupSurfaces, func(p *palette) *color.RGBA { return &p.Sidebar }),
		field("sidebar-hover", "Sidebar, hovered", groupSurfaces, func(p *palette) *color.RGBA { return &p.SidebarHover }),
		field("sidebar-selected", "Sidebar, selected", groupSurfaces, func(p *palette) *color.RGBA { return &p.SidebarSelected }),
		field("highlight", "Menu highlight", groupSurfaces, func(p *palette) *color.RGBA { return &p.Highlight }),
		field("foreground", "Text", groupText, func(p *palette) *color.RGBA { return &p.Fg }),
		field("muted", "Muted text", groupText, func(p *palette) *color.RGBA { return &p.Muted }),
		field("extra-muted", "Faint text", groupText, func(p *palette) *color.RGBA { return &p.ExtraMuted }),
		field("border", "Borders", groupText, func(p *palette) *color.RGBA { return &p.Border }),
		field("border-accent", "Input borders", groupText, func(p *palette) *color.RGBA { return &p.BorderAccent }),
		field("selection", "Selection", groupText, func(p *palette) *color.RGBA { return &p.Selection }),
		field("accent", "Accent", groupAccents, func(p *palette) *color.RGBA { return &p.Accent }),
		field("accent-bright", "Bright accent", groupAccents, func(p *palette) *color.RGBA { return &p.AccentBright }),
		field("primary", "Primary buttons", groupAccents, func(p *palette) *color.RGBA { return &p.Primary }),
		field("primary-foreground", "Primary buttons' text", groupAccents, func(p *palette) *color.RGBA { return &p.PrimaryFg }),
		field("destructive", "Destructive", groupAccents, func(p *palette) *color.RGBA { return &p.Destructive }),
		field("ring", "Focus ring", groupAccents, func(p *palette) *color.RGBA { return &p.Ring }),
		field("running", "Running", groupStatus, func(p *palette) *color.RGBA { return &p.DotRunning }),
		field("exited", "Exited", groupStatus, func(p *palette) *color.RGBA { return &p.DotExited }),
		field("down", "Down", groupStatus, func(p *palette) *color.RGBA { return &p.DotDown }),
		field("waiting", "Waiting", groupStatus, func(p *palette) *color.RGBA { return &p.DotWaiting }),
		field("done", "Done", groupStatus, func(p *palette) *color.RGBA { return &p.DotDone }),
		field("diff-added", "Added", groupDiff, func(p *palette) *color.RGBA { return &p.DiffAdded }),
		field("diff-removed", "Removed", groupDiff, func(p *palette) *color.RGBA { return &p.DiffRemoved }),
		field("diff-modified", "Modified", groupDiff, func(p *palette) *color.RGBA { return &p.DiffModified }),
		field("diff-added-bg", "Added lines", groupDiff, func(p *palette) *color.RGBA { return &p.DiffAddedBg }),
		field("diff-removed-bg", "Removed lines", groupDiff, func(p *palette) *color.RGBA { return &p.DiffRemovedBg }),
	}
	for i := range 16 {
		key := "ansi-" + strings.ToLower(ansiNames[i%8])
		if i >= 8 {
			key = "ansi-bright-" + strings.ToLower(ansiNames[i%8])
		}
		colors = append(colors, paletteColor{key: key, label: ansiNames[i%8], group: groupTerminal,
			get: func(p palette) color.RGBA { return hex(p.ANSI[i]) },
			set: func(p *palette, c color.RGBA) { p.ANSI[i] = uint32(c.R)<<16 | uint32(c.G)<<8 | uint32(c.B) }})
	}
	return colors
}()

// themesDir is where the user's themes are kept.
func themesDir() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "themes"), nil
}

// loadThemes reads the user's themes, by name. A file that does not parse
// is left out.
func loadThemes() map[string]palette {
	themes := map[string]palette{}
	dir, err := themesDir()
	if err != nil {
		return themes
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var f themeFile
		if json.Unmarshal(body, &f) != nil {
			continue
		}
		name := strings.TrimSuffix(filepath.Base(path), ".json")
		themes[name] = f.palette(name)
	}
	return themes
}

// palette is the theme: its base with its colours over it.
func (f themeFile) palette(name string) palette {
	p := lightPalette
	if f.Base == "dark" {
		p = darkPalette
	}
	p.Name = name
	for _, c := range paletteColors {
		if s, ok := f.Colors[c.key]; ok {
			if v, err := uitheme.ParseColor(s); err == nil {
				c.set(&p, color.RGBAModel.Convert(v).(color.RGBA))
			}
		}
	}
	return p
}

// saveTheme writes p as the theme name, every colour spelled out so the
// file shows what there is to change.
func saveTheme(name string, p palette) error {
	dir, err := themesDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f := themeFile{Base: "light", Colors: map[string]string{}}
	if p.Dark {
		f.Base = "dark"
	}
	for _, c := range paletteColors {
		f.Colors[c.key] = hexString(c.get(p))
	}
	body, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name+".json"), append(body, '\n'), 0o600)
}

func removeTheme(name string) error {
	dir, err := themesDir()
	if err != nil {
		return err
	}
	return os.Remove(filepath.Join(dir, name+".json"))
}

// builtinThemes are the app's own themes, the first of the preference's
// choices.
var builtinThemes = []string{"system", "light", "dark"}

func isBuiltinTheme(name string) bool { return slices.Contains(builtinThemes, name) }

// newThemeName is a name no theme has yet: "Custom", then "Custom 2".
func newThemeName(themes map[string]palette) string {
	name := "Custom"
	for i := 2; ; i++ {
		if _, ok := themes[name]; !ok && !isBuiltinTheme(name) {
			return name
		}
		name = "Custom " + strconv.Itoa(i)
	}
}

// themeNames are the theme preference's choices: the app's own, then the
// user's, by name.
func themeNames(themes map[string]palette) []string {
	return append(slices.Clone(builtinThemes), slices.Sorted(maps.Keys(themes))...)
}

// parseHex reads "#rrggbb", the # optional, and nothing shorter: the
// editor saves a colour once its hex is whole, not on the way there. A
// theme's file takes any CSS colour.
func parseHex(s string) (color.RGBA, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(s) != 6 {
		return color.RGBA{}, false
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return color.RGBA{}, false
	}
	return hex(uint32(v)), true
}

func hexString(c color.RGBA) string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }
