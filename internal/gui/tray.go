package gui

import (
	"image"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui/icons"
)

// In tray mode the app lives on in the menu bar's status area when its
// window is closed: the window hides, and the app's daemon, with the
// sessions it runs, carries on. The icon's menu, or the Dock's, brings the
// window back; a quit, from the menu or with ⌘Q, quits. With HideDock the
// app also leaves the Dock while its window is hidden. It comes back with
// the window, which without the Dock has no menu bar and no place in ⌘-Tab.

// trayIconSize is the tray icon's side in pixels: 18 points at 2×.
const trayIconSize = 36

// trayMode puts the icon in the tray while the settings ask for it, and
// has the close button hide the window rather than close it. It runs under
// the app's owner, on the UI thread.
func trayMode(app *ggui.App, m *model) {
	app.OnQuitRequest(func() { m.quitAsked = true })
	app.OnReopen(m.showWindow)
	on := ggui.Map(m.Prefs, func(p prefs) bool { return p.Tray })
	ggui.Effect(func() ggui.Cleanup {
		if !on.Get() {
			return nil
		}
		m.tray = app.AddTray(ggui.TrayConfig{
			Icon:     trayIcon(),
			Template: true,
			Title:    m.badge,
			Tooltip:  "mako",
			Menu: []ggui.MenuItem{
				ggui.MenuAction(T("Show mako"), "", m.showWindow),
				ggui.MenuAction(T("New session…"), "", func() { m.showWindow(); m.openNew("", "") }),
				ggui.MenuAction(T("Settings…"), "", func() { m.showWindow(); m.openSettings("") }),
				ggui.MenuSeparator(),
				ggui.MenuAction(T("Quit mako"), "", m.quitFromTray),
			},
		})
		return func() {
			m.tray.Remove()
			m.tray = nil
			// Out of tray mode, a hidden window would have no way back;
			// as the app closes, the window goes with it.
			if !ggui.Peek(m.Prefs).Tray {
				m.showWindow()
			}
		}
	})
}

// closeToTray decides a press of the window's close button in tray mode:
// it hides the window and reports true, unless a quit asked for the close.
func (m *model) closeToTray() bool {
	quit := m.quitAsked
	m.quitAsked = false
	if quit || m.quitting || !ggui.Peek(m.Prefs).Tray {
		return false
	}
	m.hideWindow()
	return true
}

// hideWindow hides the window into the tray, and the app out of the Dock
// when the settings ask.
func (m *model) hideWindow() {
	if m.window == nil {
		return
	}
	m.window.Hide()
	if ggui.Peek(m.Prefs).HideDock {
		ggui.SetDockIcon(false)
	}
}

// showWindow brings the window back from the tray, and the app to the Dock.
func (m *model) showWindow() {
	if m.window == nil {
		return
	}
	ggui.SetDockIcon(true)
	m.window.Show()
	m.window.Focus()
}

// quitFromTray quits as ⌘Q does: files with unsaved changes ask first,
// over the window.
func (m *model) quitFromTray() {
	if len(m.dirtyDocs()) > 0 {
		m.showWindow()
		m.QuitOpen.Set(true)
		return
	}
	m.quitting = true
	if m.window != nil {
		m.window.Close()
	}
}

// setTrayTitle shows label, the count of sessions waiting for the user,
// beside the tray's icon, as the Dock's badge does.
func (m *model) setTrayTitle(label string) {
	m.badge = label
	if m.tray != nil {
		m.tray.SetTitle(label)
	}
}

// trayIcon is the tray's icon, a terminal, as a template macOS draws in
// the menu bar's color.
func trayIcon() image.Image {
	svg, err := icons.Load(iconFiles, "icons/square-terminal.svg")
	if err != nil {
		return nil
	}
	return svg.Image(trayIconSize)
}
