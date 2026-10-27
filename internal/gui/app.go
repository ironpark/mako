// Package gui is mako's desktop front-end, built with ggui: a window onto
// the daemon mesh laid out after Paseo, with every session's terminal drawn
// in place.
package gui

import (
	"context"
	"log"

	"github.com/ironpark/ggui"
	uitheme "github.com/ironpark/ggui/ui/theme"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/notify"
)

// Run opens the window and blocks until it closes. addr is the daemon to
// talk through; empty means the local one, unless the settings name another.
func Run(cfg *config.Config, addr string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m := newModel(cfg, addr)
	defer m.shutdown()

	app := ggui.New(ggui.Config{
		Title:     "mako",
		Width:     1280,
		Height:    800,
		MinWidth:  720,
		MinHeight: 480,
		Resizable: true,
	}, func() ggui.Widget { return build(m) })
	app.Setup(func() {
		setup(m)
		m.start(ctx, app.Post)
		notify.Init(func(target string) { app.Post(func() { m.reveal(target) }) })
	})
	shortcuts(app, m)
	app.OnDrop(m.dropFolder)
	// Files with unsaved changes ask before the window goes.
	app.OnCloseRequest(func() bool {
		if len(m.dirtyDocs()) == 0 || m.quitting {
			return true
		}
		m.QuitOpen.Set(true)
		return false
	})
	return app.Run()
}

// setup wires the settings to what follows them: the theme, the saved file
// and the daemon link. It runs under the app's (or a probe's) owner.
func setup(m *model) {
	m.window = ggui.UseWindow()
	followAttention(m)
	ggui.Effect(func() ggui.Cleanup {
		pal := paletteFor(m.Prefs.Get().Theme, ggui.SystemDark().Get())
		uitheme.Set(themeFor(pal))
		return nil
	})
	last := ggui.Peek(m.Prefs)
	ggui.Watch(m.Prefs, func(p prefs) {
		if err := savePrefs(p); err != nil {
			log.Printf("mako: save settings: %v", err)
		}
		prev := last
		last = p
		m.approveInApp.Store(p.ApproveInApp)
		if p.Daemon != prev.Daemon {
			m.reconnect(p.Daemon)
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
			m.askKill(id)
		}
	})
	host.Shortcut("cmd+s", m.saveSelected)
	host.Shortcut("cmd+k", m.openPalette)
	host.Shortcut("cmd+shift+p", m.openPalette)
	host.Shortcut("cmd+shift+v", m.toggleSelectedPreview)
	host.Shortcut("cmd+shift+j", m.toggleChat)
	host.Shortcut("cmd+b", func() { ggui.Toggle(m.SidebarOpen) })
	host.Shortcut("cmd+shift+d", func() { ggui.Toggle(m.DetailsOpen) })
	host.Shortcut("cmd+,", func() { m.openSettings("") })
	host.Shortcut("cmd+p", m.openQuickOpen)
	host.Shortcut("cmd+shift+f", m.openSearch)
	host.Shortcut("alt+cmd+down", func() { m.stepDiff(1) })
	host.Shortcut("alt+cmd+up", func() { m.stepDiff(-1) })
}

// shortcutList is what the settings page shows under Shortcuts, written as
// the chords above are registered.
var shortcutList = []struct{ Chord, Action string }{
	{"cmd+t", "New session"},
	{"cmd+w", "Close tab and session"},
	{"cmd+s", "Save file"},
	{"cmd+k", "Command palette"},
	{"cmd+shift+v", "Toggle Markdown preview"},
	{"cmd+shift+j", "Toggle chat and terminal"},
	{"cmd+b", "Toggle sidebar"},
	{"cmd+shift+d", "Toggle details panel"},
	{"cmd+,", "Open settings"},
	{"cmd+p", "Go to file"},
	{"cmd+shift+f", "Search the project"},
	{"alt+cmd+down", "Next changed file"},
	{"alt+cmd+up", "Previous changed file"},
}
