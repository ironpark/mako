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

// agentUIPreview renders an agent's UI through the window, as an agent,
// driven by something else, shows it: a form, filled in and sent here as
// the user would, through the window's own route; the agent's thanks; the
// form closed; a block it asks to place elsewhere, allowed in the bar;
// then a plugin it wrote, allowed, and its block placed, allowed. Between
// steps it leaves a file in sig, named for what it saw, for the agent to
// go on.
func agentUIPreview(m *model, p func() *ggui.Probe, sig string, do func(func()) previewStep,
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
	shows := func(text string) func() bool {
		return func() bool { return strings.Contains(p().Semantics().String(), text) }
	}
	click := func(label string) previewStep {
		return do(func() {
			if f, ok := p().Find(label); ok {
				p().Click(f.Center())
			}
		})
	}
	staged := func(tool string) func() bool {
		return func() bool {
			st := ggui.Peek(m.LayoutStage)
			return st.GetPhase() == protocol.LayoutApprove && st.GetTool() == tool
		}
	}
	return []previewStep{
		do(func() { m.Prefs.Update(func(p prefs) prefs { p.Theme = "dark"; return p }) }),
		waitFor("the daemon", func() bool { return ggui.Peek(m.Conn).OK }),
		waitFor("the agent's form", shows("Which size should I order?")),
		frames(30), shot("90-agent-form"),
		click("Your name"), frames(3), do(func() { p().Text("Ada") }), frames(3),
		click("Wrap it as a gift"), frames(10), shot("91-agent-form-filled"),
		do(func() { p().Tap("Send") }),
		waitFor("the agent's thanks", shows("Thanks, Ada")),
		frames(20), shot("92-agent-thanks"), signal("thanks.shot"),
		waitFor("the form closed", func() bool { return len(ggui.Peek(m.AgentBlocks)) == 0 }),
		frames(20), shot("93-agent-closed"), signal("closed.shot"),
		waitFor("the agent asking to place a block", staged("show_ui")),
		frames(20), shot("94-agent-place-ask"),
		do(func() { m.answerStage(ggui.Peek(m.LayoutStage), true) }),
		waitFor("the block placed", func() bool {
			bs := ggui.Peek(m.AgentBlocks)
			return len(bs) == 1 && bs[0].GetParent() == "workspace" && ggui.Peek(m.LayoutStage) == nil
		}),
		frames(30), shot("95-agent-placed"), signal("placed.shot"),
		waitFor("the agent's plugin to ask", func() bool { return m.pluginOf("notes").GetState() == protocol.PluginWaiting }),
		frames(20), shot("96-agent-plugin-asks"),
		do(func() { p().Tap(T("Allow")) }),
		waitFor("the agent's plugin allowed", func() bool { return m.pluginOf("notes").GetState() != protocol.PluginWaiting }),
		signal("plugin.granted"),
		waitFor("the agent asking to place its plugin", staged("patch_layout")),
		frames(20), shot("97-agent-plugin-place-ask"),
		do(func() { m.answerStage(ggui.Peek(m.LayoutStage), true) }),
		waitFor("the plugin's block", shows("Notes from the agent")),
		frames(30), shot("98-agent-plugin"), signal("done"),
	}
}
