package gui

import (
	"encoding/json"
	"image/color"
	"os"
	"path/filepath"
	"strings"

	uitheme "github.com/ironpark/ggui/ui/theme"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/gui/skin"
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
func loadThemes() map[string]skin.Palette {
	themes := map[string]skin.Palette{}
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
func (f themeFile) palette(name string) skin.Palette {
	p := skin.Light
	if f.Base == "dark" {
		p = skin.Dark
	}
	p.Name = name
	for _, c := range skin.Colors {
		if s, ok := f.Colors[c.Key]; ok {
			if v, err := uitheme.ParseColor(s); err == nil {
				c.Set(&p, color.RGBAModel.Convert(v).(color.RGBA))
			}
		}
	}
	return p
}

// saveTheme writes p as the theme name, every colour spelled out so the
// file shows what there is to change.
func saveTheme(name string, p skin.Palette) error {
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
	for _, c := range skin.Colors {
		f.Colors[c.Key] = skin.HexString(c.Get(p))
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
