package gui

import (
	"testing"

	"github.com/ironpark/mako/internal/config"
)

func TestTheCloseButtonHidesToTheTrayAndAQuitQuits(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	if m.closeToTray() {
		t.Fatal("out of tray mode, the close button kept the window")
	}
	m.Prefs.Update(func(p prefs) prefs { p.Tray = true; return p })
	if !m.closeToTray() {
		t.Fatal("in tray mode, the close button closed the window")
	}
	m.quitAsked = true
	if m.closeToTray() {
		t.Fatal("in tray mode, a quit kept the window")
	}
	if !m.closeToTray() {
		t.Fatal("after a quit was asked and refused, the close button closed the window")
	}
}
