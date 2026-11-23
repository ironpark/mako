// Package gui is mako's desktop front-end, built with ggui: a window onto
// the daemon mesh laid out after Paseo, with every session's terminal drawn
// in place.
package gui

import (
	"context"
	"log"
	"maps"

	"github.com/ironpark/ggui"
	uitheme "github.com/ironpark/ggui/ui/theme"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/gui/skin"
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
		// On macOS the headers stand for the title bar; see titlebar.go.
		TitlebarHidden: titlebarHidden,
		TrafficLights:  trafficLights(),
	}, func() ggui.Widget { return build(m) })
	app.Setup(func() {
		go findEditors()
		// setup's effects already fetch, and a call that fails at once
		// hands its result back before start would have set post.
		m.post = app.Post
		setup(m)
		trayMode(app, m)
		m.start(ctx, app.Post)
	})
	shortcuts(app, m)
	app.OnDrop(m.dropFolder)
	// In tray mode the window hides; otherwise files with unsaved changes
	// ask before it goes.
	app.OnCloseRequest(func() bool {
		if m.closeToTray() {
			return false
		}
		if len(m.dirtyDocs()) == 0 || m.quitting {
			return true
		}
		m.askQuit()
		return false
	})
	return app.Run()
}

// setup wires the settings to what follows them: the theme, the saved file
// and the daemon link. It runs under the app's (or a probe's) owner.
func setup(m *model) {
	m.window = ggui.UseWindow()
	m.host = ggui.UseHost()
	// A click on a notification opens what it was about.
	m.host.OnNotification(func(r ggui.NotificationResponse) {
		if r.Action == "" {
			m.reveal(r.Data)
		}
	})
	followAttention(m)
	followFiles(m)
	followDiffChanges(m)
	followWindowFocus(m)
	setupWorkspace(m)
	ggui.Effect(func() ggui.Cleanup {
		pal := m.themePalette()
		uitheme.Set(skin.Theme(pal))
		return nil
	})
	last := ggui.Peek(m.Prefs)
	ggui.Watch(m.Prefs, func(p prefs) {
		if path, body, err := prefsFile(p); err != nil {
			log.Printf("mako: save settings: %v", err)
		} else {
			m.prefsWriter.write(path, body)
		}
		prev := last
		last = p
		m.link.setApproveInApp(p.ApproveInApp)
		m.recorder.on.Store(p.RecordStreams)
		if p.Daemon != prev.Daemon {
			m.reconnect(p.Daemon)
		}
		if p.DaemonMode != prev.DaemonMode || (p.Daemon != prev.Daemon && p.DaemonMode != "embedded") {
			m.applyDaemonMode()
		}
		if !maps.Equal(p.Shortcuts, prev.Shortcuts) {
			m.syncShortcuts()
		}
	})
	ggui.Watch(m.Page, func(page string) {
		if page != "settings" {
			m.stopRecordingShortcut()
		}
	})
	ggui.Watch(m.Section, func(section string) {
		if section != "shortcuts" {
			m.stopRecordingShortcut()
		}
	})
	ggui.Effect(func() ggui.Cleanup {
		return func() { m.cleanupShortcuts() }
	})
}

func shortcuts(host ggui.Host, m *model) {
	bindShortcuts(host, m)
}

// notify tells the desktop of what target is about, as the platform shows
// notifications, with its sound; a later one about the same target takes
// its place, and a click on it reveals target. It may be called from any
// goroutine, and returns at once.
func (m *model) notify(title, body, target string) {
	if m.host == nil {
		return
	}
	if err := m.host.Notify(ggui.Notification{ID: target, Title: title, Body: body, Sound: true, Data: target}); err != nil {
		m.notifyFailed.Do(func() { log.Printf("mako: notifications: %v", err) })
	}
}
