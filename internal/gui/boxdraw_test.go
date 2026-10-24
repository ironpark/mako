package gui

import (
	"image/color"
	"testing"

	"github.com/ironpark/ggui"
)

// Every box-drawing and block character is drawn as shapes, none left to a
// font that would stop short of the cell.
func TestEveryBoxAndBlockCharacterIsDrawn(t *testing.T) {
	c := &ggui.Canvas{}
	cell := ggui.Rect{Origin: ggui.Pt(0, 0), Size: ggui.Sz(8, 18)}
	for r := rune(0x2500); r <= 0x259F; r++ {
		if !drawBoxChar(c, r, cell, color.RGBA{A: 0xff}) {
			t.Errorf("%U %c is left to the font", r, r)
		}
	}
	if drawBoxChar(c, 'A', cell, color.RGBA{}) || drawBoxChar(c, '❯', cell, color.RGBA{}) {
		t.Error("an ordinary character was taken as a box character")
	}
}

func TestBoxArmsMatchTheirNames(t *testing.T) {
	for r, want := range map[rune]arms{
		'─': {0, light, 0, light}, '┃': {heavy, 0, heavy, 0}, '┌': {0, light, light, 0},
		'┢': {light, heavy, heavy, 0}, '╋': {heavy, heavy, heavy, heavy}, '╔': {0, double, double, 0},
		'╪': {light, double, light, double}, '╿': {heavy, 0, light, 0},
	} {
		if got := boxArms[r-0x2500]; got != want {
			t.Errorf("%c arms %v, want %v", r, got, want)
		}
	}
}
