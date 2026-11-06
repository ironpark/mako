package skin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestThemesRoundTripAndFillFromTheirBase(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	p := Dark
	p.AccentBright = Hex(0x123456)
	p.ANSI[9] = 0xABCDEF
	if err := Save("Mine", p); err != nil {
		t.Fatal(err)
	}
	// A file that names a few colours takes the rest from its base.
	dir, _ := Dir()
	if err := os.WriteFile(filepath.Join(dir, "Few.json"), []byte(`{"base":"light","colors":{"foreground":"#010203","ansi-red":"#ff0000","accent":"nope"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Broken.json"), []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	themes := Load()
	mine := themes["Mine"]
	if !mine.Dark || mine.AccentBright != Hex(0x123456) || mine.ANSI[9] != 0xABCDEF || mine.Surface0 != Dark.Surface0 || mine.Name != "Mine" {
		t.Fatalf("Mine read back as %+v", mine)
	}
	few := themes["Few"]
	if few.Dark || few.Fg != Hex(0x010203) || few.ANSI[1] != 0xFF0000 || few.Accent != Light.Accent || few.Surface0 != Light.Surface0 {
		t.Fatalf("Few read as %+v", few)
	}
	if _, ok := themes["Broken"]; ok || len(themes) != 2 {
		t.Fatalf("themes = %v", Names(themes))
	}
	if got := Pick("Few", true, themes); got.Name != "Few" {
		t.Fatalf("Pick picked %q", got.Name)
	}
	if got := Pick("Gone", true, themes); got.Name != "dark" {
		t.Fatalf("a theme that is gone gave %q, not the system's", got.Name)
	}
	if name := NewName(map[string]Palette{"Custom": {}, "Custom 2": {}}); name != "Custom 3" {
		t.Fatalf("NewName = %q", name)
	}
	if err := Remove("Mine"); err != nil {
		t.Fatal(err)
	}
	if _, ok := Load()["Mine"]; ok {
		t.Fatal("a removed theme is still read")
	}
}

func TestParseHex(t *testing.T) {
	for in, ok := range map[string]bool{"#7ccba0": true, "7CCBA0": true, " #000000 ": true, "#7cc": false, "#gggggg": false, "": false} {
		if _, got := ParseHex(in); got != ok {
			t.Errorf("ParseHex(%q) ok = %v", in, got)
		}
	}
	if c, _ := ParseHex("#7ccba0"); HexString(c) != "#7ccba0" {
		t.Errorf("HexString(ParseHex) = %s", HexString(c))
	}
}
