package gui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/protocol"
)

// pluginsPreview renders a plugin's way through the window, as something
// else drives the daemon and the plugin's folder: the window asking to
// allow it, its block with what it read, its command in the palette and
// run, its settings, then its folder broken and mended. The window takes
// layout.json in as its document, which places the block. Between steps it
// leaves a file in sig, named for what it waits for next, for the driver
// to do it.
func pluginsPreview(m *model, id string, semantics func() string, sig string, do func(func()) previewStep,
	frames func(int) previewStep, shot func(string) previewStep) []previewStep {
	waitFor := func(what string, cond func() bool) previewStep {
		var since time.Time
		return func() (bool, error) {
			if since.IsZero() {
				since = time.Now()
			}
			if cond() {
				return true, nil
			}
			if time.Since(since) > 2*time.Minute {
				return false, fmt.Errorf("preview: timed out waiting for %s", what)
			}
			return false, nil
		}
	}
	signal := func(name string) previewStep {
		return do(func() { _ = os.WriteFile(filepath.Join(sig, name), nil, 0o644) })
	}
	plugin := func() *protocol.PluginInfo { return m.pluginOf(id) }
	state := func(s string) func() bool { return func() bool { return plugin().GetState() == s } }
	shows := func(text string) func() bool { return func() bool { return strings.Contains(semantics(), text) } }
	theme := func(t string) func() { return func() { m.Prefs.Update(func(p prefs) prefs { p.Theme = t; return p }) } }
	return []previewStep{
		do(theme("dark")),
		waitFor("the daemon", func() bool { return ggui.Peek(m.Conn).OK }),
		waitFor("the plugin to ask", state(protocol.PluginWaiting)),
		frames(30), shot("80-plugin-asks"),
		signal("asked"),
		waitFor("the plugin to run", state(protocol.PluginRunning)),
		waitFor("its runs", shows("lint")),
		frames(30), shot("81-plugin-block"),
		do(m.openPalette), do(func() { m.pickers.PaletteQuery.Set("re-run") }), frames(10), shot("82-plugin-palette"),
		do(func() {
			m.pickers.PaletteOpen.Set(false)
			m.actOnPlugin(&protocol.PluginAction{PluginId: id, Op: protocol.PluginRun, Command: "rerun"})
		}),
		waitFor("the command's note", shows("was started again")),
		frames(20), shot("83-plugin-command"),
		do(func() { m.openSettings(pluginsSection.id) }), frames(20), shot("84-plugin-settings"),
		do(func() { m.Page.Set("workspace") }), frames(10),
		signal("break"),
		waitFor("the broken version", func() bool { return plugin().GetStale() }),
		frames(30), shot("85-plugin-broken"),
		signal("mend"),
		waitFor("the mended version", func() bool {
			p := plugin()
			return !p.GetStale() && p.GetError() == "" && p.GetState() == protocol.PluginRunning
		}),
		waitFor("its runs again", shows("lint")),
		frames(30), shot("86-plugin-mended"),
		do(theme("light")), frames(20), shot("87-plugin-light"),
		do(theme("dark")),
		signal("runaway"),
		waitFor("the runaway stopped", state(protocol.PluginFailed)),
		frames(30), shot("88-plugin-stopped"),
		signal("done"),
	}
}
