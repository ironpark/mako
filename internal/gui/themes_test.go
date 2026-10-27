package gui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestThemesRoundTripAndFillFromTheirBase(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	p := darkPalette
	p.AccentBright = hex(0x123456)
	p.ANSI[9] = 0xABCDEF
	if err := saveTheme("Mine", p); err != nil {
		t.Fatal(err)
	}
	// A file that names a few colours takes the rest from its base.
	dir, _ := themesDir()
	if err := os.WriteFile(filepath.Join(dir, "Few.json"), []byte(`{"base":"light","colors":{"foreground":"#010203","ansi-red":"#ff0000","accent":"nope"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Broken.json"), []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	themes := loadThemes()
	mine := themes["Mine"]
	if !mine.Dark || mine.AccentBright != hex(0x123456) || mine.ANSI[9] != 0xABCDEF || mine.Surface0 != darkPalette.Surface0 || mine.Name != "Mine" {
		t.Fatalf("Mine read back as %+v", mine)
	}
	few := themes["Few"]
	if few.Dark || few.Fg != hex(0x010203) || few.ANSI[1] != 0xFF0000 || few.Accent != lightPalette.Accent || few.Surface0 != lightPalette.Surface0 {
		t.Fatalf("Few read as %+v", few)
	}
	if _, ok := themes["Broken"]; ok || len(themes) != 2 {
		t.Fatalf("themes = %v", themeNames(themes))
	}
	if got := paletteFor("Few", true, themes); got.Name != "Few" {
		t.Fatalf("paletteFor picked %q", got.Name)
	}
	if got := paletteFor("Gone", true, themes); got.Name != "dark" {
		t.Fatalf("a theme that is gone gave %q, not the system's", got.Name)
	}
	if name := newThemeName(map[string]palette{"Custom": {}, "Custom 2": {}}); name != "Custom 3" {
		t.Fatalf("newThemeName = %q", name)
	}
	if err := removeTheme("Mine"); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadThemes()["Mine"]; ok {
		t.Fatal("a removed theme is still read")
	}
}

func TestParseHex(t *testing.T) {
	for in, ok := range map[string]bool{"#7ccba0": true, "7CCBA0": true, " #000000 ": true, "#7cc": false, "#gggggg": false, "": false} {
		if _, got := parseHex(in); got != ok {
			t.Errorf("parseHex(%q) ok = %v", in, got)
		}
	}
	if c, _ := parseHex("#7ccba0"); hexString(c) != "#7ccba0" {
		t.Errorf("hexString(parseHex) = %s", hexString(c))
	}
}
