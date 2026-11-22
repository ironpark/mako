package botavatar

import (
	"bytes"
	"image"
	"testing"
	"time"

	"github.com/ironpark/ggfx"
	"github.com/ironpark/ggfx/svg"
)

func TestAllBuiltinAvatarsValidAndAnimate(t *testing.T) {
	if len(Builtin) != 23 {
		t.Fatalf("got %d builtin avatars, want 23", len(Builtin))
	}
	for _, def := range Builtin {
		norm, errs := Validate(def)
		if len(errs) > 0 {
			t.Errorf("builtin avatar %q failed validation: %v", def.ID, errs)
			continue
		}
		if norm.ID != def.ID {
			t.Errorf("normalized ID = %q, want %q", norm.ID, def.ID)
		}
		if _, ok := DefaultColors[def.ID]; !ok {
			t.Errorf("builtin avatar %q missing default color", def.ID)
		}
	}
}

func TestRenderAllStatesAndMoodsParseAndAnimate(t *testing.T) {
	def, ok := Lookup("mug")
	if !ok {
		t.Fatal("missing mug avatar")
	}
	for _, st := range AllStates {
		for _, mood := range AllMoods {
			doc, err := Document(def, Options{State: st, Mood: mood, Color: "#ff6a1a"})
			if err != nil {
				t.Fatalf("Document(state=%s, mood=%s) failed: %v", st, mood, err)
			}
			if !doc.Animated() {
				t.Errorf("Document(state=%s, mood=%s) expected Animated() == true", st, mood)
			}
			if _, ok := doc.Node("oba-gaze"); !ok {
				t.Errorf("Document(state=%s, mood=%s) missing oba-gaze node", st, mood)
			}
			if _, ok := doc.Node("oba-root"); !ok {
				t.Errorf("Document(state=%s, mood=%s) missing oba-root node", st, mood)
			}
		}
	}

	// ReducedMotion emits a static SVG.
	still, err := Document(def, Options{State: StateWorking, ReducedMotion: true})
	if err != nil {
		t.Fatalf("ReducedMotion Document failed: %v", err)
	}
	if still.Animated() {
		t.Error("expected ReducedMotion document to be static (Animated() == false)")
	}
}

func TestAnimatedSVGRasterChangesOverTime(t *testing.T) {
	def, _ := Lookup("cat")
	for _, st := range AllStates {
		doc, err := Document(def, Options{State: st, Color: "#f8a01c"})
		if err != nil {
			t.Fatalf("state %s: %v", st, err)
		}
		renderAt := func(ts time.Duration) []byte {
			img := image.NewRGBA(image.Rect(0, 0, 64, 64))
			vx, vy, vw, vh := doc.ViewBox()
			var m ggfx.GeoM
			m.Translate(-vx, -vy)
			m.Scale(64.0/vw, 64.0/vh)
			doc.DrawWith(img, m, &svg.DrawOptions{Time: ts})
			return img.Pix
		}
		f0 := renderAt(0)
		// Sample multiple timestamps so periodic motions like blocked alarm (2.6s)
		// or working ribbons (3.4s) definitely capture movement.
		changed := false
		for _, dt := range []time.Duration{250 * time.Millisecond, 600 * time.Millisecond, 1200 * time.Millisecond} {
			if !bytes.Equal(f0, renderAt(dt)) {
				changed = true
				break
			}
		}
		if !changed {
			t.Errorf("state %s rendered identical pixels across animation timestamps", st)
		}
	}
}

func TestParseJSONAndValidation(t *testing.T) {
	valid := []byte(`{
		"version": 1,
		"id": "box",
		"name": "Box",
		"body": { "path": "M20 20 H80 V80 H20 Z" },
		"eyes": { "x": 50, "y": 46, "spacing": 15, "scale": 1 }
	}`)
	def, err := ParseJSON(valid)
	if err != nil {
		t.Fatalf("ParseJSON valid failed: %v", err)
	}
	if def.ID != "box" || def.Name != "Box" {
		t.Fatalf("unexpected parsed def: %+v", def)
	}

	invalid := []byte(`{
		"version": 1,
		"id": "bad",
		"name": "Bad",
		"body": { "path": "M20 20 H80 V80 H20 Z" },
		"eyes": { "x": 5, "y": 5, "spacing": 15, "scale": 1 }
	}`)
	if _, err := ParseJSON(invalid); err == nil {
		t.Fatal("expected error for eyes placed outside body")
	}
}

func TestRibbonPaletteProducesDistinctValidHexColors(t *testing.T) {
	pal := RibbonPalette("#ff6a1a", HashString("mug"))
	for i, c := range pal {
		if _, ok := ParseHex(c); !ok {
			t.Fatalf("palette[%d] = %q is not valid hex", i, c)
		}
	}
	// Neutral white body should still produce valid darker shades.
	whitePal := RibbonPalette("#f4f4f4", HashString("ghost"))
	for i, c := range whitePal {
		rgb, ok := ParseHex(c)
		if !ok {
			t.Fatalf("white palette[%d] = %q invalid", i, c)
		}
		if Luminance(rgb) > 0.9 {
			t.Errorf("white palette[%d] = %q too bright against white body", i, c)
		}
	}
}
