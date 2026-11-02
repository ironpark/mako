package gui

import (
	"image/color"
	"testing"
)

// A run's string outlives the buffer it was read from, as the text caches
// keep it as a key, and the same text is the same string, made once.
func TestThePaintMemoKeepsRunsApartFromTheirBuffer(t *testing.T) {
	var m paintMemo
	b := []byte("hello")
	s := m.run(b)
	b[0] = 'j'
	if s != "hello" {
		t.Fatalf("the run changed with its buffer: %q", s)
	}
	if got := m.run([]byte("jello")); got != "jello" {
		t.Fatalf("run %q", got)
	}
	if n := testing.AllocsPerRun(100, func() { m.run(b) }); n != 0 {
		t.Fatalf("a run read again took %v allocations", n)
	}
}

func TestThePaintMemoBoxesAColorAndARuneOnce(t *testing.T) {
	var m paintMemo
	red, blue := color.RGBA{R: 255, A: 255}, color.RGBA{B: 255, A: 255}
	if m.color(red) != color.Color(red) || m.color(blue) != color.Color(blue) {
		t.Fatal("a boxed color differs from the color")
	}
	if n := testing.AllocsPerRun(100, func() { m.color(red); m.color(blue); m.rune('한') }); n != 0 {
		t.Fatalf("colors and a rune boxed before took %v allocations", n)
	}
	if m.rune('한') != "한" {
		t.Fatal("rune")
	}
}
