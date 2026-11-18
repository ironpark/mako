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

// storePreview renders a plugin's way from a registry into the window,
// as something else drives the registry: its block missing with an offer
// to install it, the store, the install asking, the block, an update
// that asks for more, one signed by another key refused, and the plugin
// blocked. The window acts as the user would, with the calls its buttons
// make; between steps it leaves a file in sig, named for what it waits
// for next, for the driver to do it.
func storePreview(m *model, id string, semantics func() string, sig string, do func(func()) previewStep,
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
	settings := do(func() { m.openSettings(pluginsSection.id) })
	workspace := do(func() { m.Page.Set("workspace") })
	listed := func() bool {
		for _, l := range ggui.Peek(m.store.Results).Listings {
			if l.GetId() == id {
				return true
			}
		}
		return false
	}
	return []previewStep{
		do(theme("dark")),
		waitFor("the daemon", func() bool { return ggui.Peek(m.Conn).OK && m.storeOpen() }),
		waitFor("the offer to install", func() bool { return len(ggui.Peek(m.store.Known)[id]) > 0 }),
		frames(30), shot("89-store-missing-offer"),
		settings, waitFor("the listing", listed), frames(30), shot("90-store"),
		do(theme("light")), frames(20), shot("90-store-light"), do(theme("dark")),
		workspace,
		do(func() { m.installPlugin(protocol.InstallOp, id, "") }),
		waitFor("the plugin to ask", state(protocol.PluginWaiting)),
		signal("installed"),
		frames(30), shot("91-store-install-asks"),
		do(func() { m.actOnPlugin(&protocol.PluginAction{PluginId: id, Op: protocol.PluginGrant}) }),
		waitFor("the plugin to run", state(protocol.PluginRunning)),
		waitFor("its runs", shows("lint")),
		frames(30), shot("92-store-block"),
		signal("publish-v2"),
		waitFor("the update", func() bool { return plugin().GetUpdate() != "" }),
		settings, frames(30), shot("93-store-update-available"),
		do(func() { m.installPlugin(protocol.UpdateOp, id, "") }),
		waitFor("the update to ask", func() bool { return plugin().GetState() == protocol.PluginWaiting && plugin().GetVersion() == "0.2.0" }),
		workspace, frames(30), shot("94-store-update-asks"),
		settings, frames(20), shot("95-store-update-asks-settings"),
		do(func() { m.actOnPlugin(&protocol.PluginAction{PluginId: id, Op: protocol.PluginGrant}) }),
		waitFor("the update to run", state(protocol.PluginRunning)),
		signal("publish-v3"),
		waitFor("v0.3 listed", func() bool {
			for _, l := range ggui.Peek(m.store.Results).Listings {
				if l.GetId() == id && l.GetVersion() == "0.3.0" {
					return true
				}
			}
			if r := ggui.Peek(m.store.Results); !r.Loading {
				m.searchStore("") // the driver says when it published; ask again until it has
			}
			return false
		}),
		do(func() { m.installPlugin(protocol.UpdateOp, id, "") }),
		waitFor("the refusal", func() bool { return strings.Contains(ggui.Peek(m.store.Err), "same key") }),
		frames(30), shot("96-store-other-key-refused"),
		signal("block"),
		waitFor("the plugin blocked", state(protocol.PluginBlocked)),
		frames(30), shot("97-store-blocked-settings"),
		workspace, frames(30), shot("98-store-blocked-block"),
		signal("done"),
	}
}
