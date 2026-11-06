package gui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ironpark/mako/internal/gui/skin"
)

func TestThemesRoundTripAndFillFromTheirBase(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	p := skin.Dark
	p.AccentBright = skin.Hex(0x123456)
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
	if !mine.Dark || mine.AccentBright != skin.Hex(0x123456) || mine.ANSI[9] != 0xABCDEF || mine.Surface0 != skin.Dark.Surface0 || mine.Name != "Mine" {
		t.Fatalf("Mine read back as %+v", mine)
	}
	few := themes["Few"]
	if few.Dark || few.Fg != skin.Hex(0x010203) || few.ANSI[1] != 0xFF0000 || few.Accent != skin.Light.Accent || few.Surface0 != skin.Light.Surface0 {
		t.Fatalf("Few read as %+v", few)
	}
	if _, ok := themes["Broken"]; ok || len(themes) != 2 {
		t.Fatalf("themes = %v", skin.Names(themes))
	}
	if got := skin.Pick("Few", true, themes); got.Name != "Few" {
		t.Fatalf("skin.Pick picked %q", got.Name)
	}
	if got := skin.Pick("Gone", true, themes); got.Name != "dark" {
		t.Fatalf("a theme that is gone gave %q, not the system's", got.Name)
	}
	if name := skin.NewName(map[string]skin.Palette{"Custom": {}, "Custom 2": {}}); name != "Custom 3" {
		t.Fatalf("skin.NewName = %q", name)
	}
	if err := removeTheme("Mine"); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadThemes()["Mine"]; ok {
		t.Fatal("a removed theme is still read")
	}
}
