package gui

import (
	"slices"
	"testing"

	"github.com/ironpark/ggui"
)

func TestBackdropPlacesAsACSSBackgroundDoes(t *testing.T) {
	window := ggui.Rct(ggui.Pt(0, 0), ggui.Sz(400, 200))
	image := ggui.Sz(100, 100)
	at := func(x, y, w, h float64) ggui.Rect { return ggui.Rct(ggui.Pt(x, y), ggui.Sz(w, h)) }
	for _, tc := range []struct {
		name string
		b    backdrop
		want ggui.Rect
	}{
		{"cover fills the room, centred", backdrop{}, at(0, -100, 400, 400)},
		{"cover from the top", backdrop{Position: "top"}, at(0, 0, 400, 400)},
		{"contain fits inside, to the right", backdrop{Fit: "contain", Position: "right"}, at(200, 0, 200, 200)},
		{"fill stretches", backdrop{Fit: "fill"}, window},
		{"repeat starts at the natural size", backdrop{Fit: "repeat", Position: "top-left"}, at(0, 0, 100, 100)},
		{"a width alone keeps the shape", backdrop{Width: 50, Position: "bottom-right"}, at(350, 150, 50, 50)},
		{"margins shrink the room", backdrop{Fit: "fill", MarginX: 10, MarginY: 20}, at(10, 20, 380, 160)},
		{"margins move a corner image in", backdrop{Width: 40, Height: 20, Position: "bottom-left", MarginX: 16, MarginY: 8}, at(16, 172, 40, 20)},
	} {
		b := tc.b.normalized()
		if got := b.place(image, b.room(window)); got != tc.want {
			t.Errorf("%s: placed at %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestBackdropTilesCoverTheRoomFromTheirPosition(t *testing.T) {
	room := ggui.Rct(ggui.Pt(0, 0), ggui.Sz(250, 100))
	// The tile in the centre: the grid runs out from it both ways.
	got := slices.Collect(tiles(ggui.Rct(ggui.Pt(75, 0), ggui.Sz(100, 100)), room))
	if len(got) != 3 || got[0].Origin.X != -25 || got[2].Origin.X != 175 {
		t.Fatalf("tiles = %v, want three from x=-25", got)
	}
	// A tiny tile over a large room is drawn larger rather than thousands of times.
	if n := len(slices.Collect(tiles(ggui.Rct(ggui.Pt(0, 0), ggui.Sz(1, 1)), ggui.Rct(ggui.Pt(0, 0), ggui.Sz(3000, 2000))))); n > maxTiles*2 {
		t.Fatalf("%d tiles, want about %d at most", n, maxTiles)
	}
}

func TestBackdropSettingsDefaultAndBound(t *testing.T) {
	b := backdrop{Fit: "zoom", Position: "middle", Width: -5, MarginX: 1e9}.normalized()
	if b.Fit != "cover" || b.Position != "center" || b.Width != 0 || b.MarginX != maxBackdropLength {
		t.Fatalf("normalized = %+v", b)
	}
	for _, p := range backdropPositions {
		if backdropPositionLabels[p] == "" {
			t.Errorf("position %q has no label", p)
		}
	}
	for _, f := range backdropFits {
		if backdropFitLabels[f] == "" {
			t.Errorf("fit %q has no label", f)
		}
	}
}
