// Package gui is sanbo's desktop front-end, built with ggui: a window onto
// the daemon mesh laid out after Paseo, with every session's terminal drawn
// in place.
package gui

import (
	"context"
	"log"

	"github.com/ironpark/ggui"
	uitheme "github.com/ironpark/ggui/ui/theme"

	"github.com/ironpark/sanbo/internal/config"
)

// Run opens the window and blocks until it closes. addr is the daemon to
// talk through; empty means the local one, unless the settings name another.
func Run(cfg *config.Config, addr string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m := newModel(cfg, addr)
	defer m.shutdown()

	app := ggui.New(ggui.Config{
		Title:     "sanbo",
		Width:     1280,
		Height:    800,
		MinWidth:  720,
		MinHeight: 480,
		Resizable: true,
	}, func() ggui.Widget { return build(m) })
	app.Setup(func() {
		setup(m)
		m.start(ctx, app.Post)
	})
	shortcuts(app, m)
	app.OnDrop(m.dropFolder)
	return app.Run()
}

// setup wires the settings to what follows them: the theme, the saved file
// and the daemon link. It runs under the app's (or a probe's) owner.
func setup(m *model) {
	ggui.Effect(func() ggui.Cleanup {
		pal := paletteFor(m.Prefs.Get().Theme, ggui.SystemDark().Get())
		uitheme.Set(themeFor(pal))
		return nil
	})
	last := ggui.Peek(m.Prefs)
	ggui.Watch(m.Prefs, func(p prefs) {
		if err := savePrefs(p); err != nil {
			log.Printf("sanbo: save settings: %v", err)
		}
		prev := last
		last = p
		if p.Daemon != prev.Daemon {
			m.reconnect(p.Daemon, m.key())
		}
		if p.DaemonMode != prev.DaemonMode || (p.Daemon != prev.Daemon && p.DaemonMode != "embedded") {
			m.applyDaemonMode()
		}
	})
}

func shortcuts(host ggui.Host, m *model) {
	host.Shortcut("cmd+t", func() { m.openNew("", "") })
	host.Shortcut("cmd+w", func() {
		if id := ggui.Peek(m.Selected); id != "" {
			m.closeTab(id)
		}
	})
	host.Shortcut("cmd+b", func() { ggui.Toggle(m.SidebarOpen) })
	host.Shortcut("cmd+shift+d", func() { ggui.Toggle(m.DetailsOpen) })
	host.Shortcut("cmd+,", func() { m.openSettings("") })
}

// shortcutList is what the settings page shows under Shortcuts, written as
// the chords above are registered.
var shortcutList = []struct{ Chord, Action string }{
	{"cmd+t", "New session"},
	{"cmd+w", "Close tab"},
	{"cmd+b", "Toggle sidebar"},
	{"cmd+shift+d", "Toggle details panel"},
	{"cmd+,", "Open settings"},
}
