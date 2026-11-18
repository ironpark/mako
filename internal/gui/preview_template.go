package gui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/prompttmpl"
	"github.com/ironpark/mako/internal/protocol"
)

// templatePreview renders the built-in //template through the window, as
// an agent, driven by something else, answers it: the // list with the
// built-in; the call, with a description, as Chat sends it to the session
// of the agent the window shows; the approval of the template the agent
// writes, in Chat and over the terminal; saved as the user would; then
// the list with it, and Settings ▸ Templates. Between steps it leaves a
// file in sig, named for what it did, for the agent to go on.
func templatePreview(m *model, p func() *ggui.Probe, sig, call string, do func(func()) previewStep,
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
	var id string
	agent := func() bool {
		for _, s := range ggui.Peek(m.Sessions) {
			if s.GetAgent() == "claude" && s.GetState() == protocol.StateRunning {
				id = s.GetId()
				return true
			}
		}
		return false
	}
	asks := func() bool { return ggui.Peek(m.approvalOfSession(id)).GetKind() == protocol.ToolTemplate }
	draft := func(text string) previewStep {
		return do(func() { m.setComposerDraft(id, text); m.readTemplates(m.chatState(id), true) })
	}
	saved := func() bool {
		dir, _ := config.TemplatesDir()
		entries, _ := os.ReadDir(dir)
		return ggui.Peek(m.approvalOfSession(id)) == nil && len(entries) > 0
	}
	return []previewStep{
		do(func() { m.Prefs.Update(func(p prefs) prefs { p.Theme = "dark"; return p }) }),
		waitFor("the daemon", func() bool { return ggui.Peek(m.Conn).OK }),
		waitFor("the agent's session", agent),
		do(func() { m.selectSession(id); m.setChatMode(id, true) }),
		waitFor("the session attached", func() bool { return m.conns[id] != nil }),
		draft("//"), frames(30), shot("80-template-list"),
		draft("//" + prompttmpl.MakerName + " " + call), frames(20), shot("81-template-call"),
		do(func() { m.sendComposer(id) }),
		waitFor("the call sent", func() bool { return strings.TrimSpace(m.composerDraft(id)) == "" }),
		signal("sent"),
		waitFor("the agent's template", asks), frames(60), shot("82-template-approval"),
		do(func() { m.setChatMode(id, false) }), frames(20), shot("83-template-bar"),
		do(func() { m.setChatMode(id, true) }), frames(20),
		do(func() { p().Tap(T("Save template")) }),
		waitFor("the template saved", saved), signal("saved"),
		draft("//"), frames(30), shot("84-template-list-after"),
		draft(""),
		do(func() { m.Page.Set("settings"); m.Section.Set("templates") }), frames(30), shot("85-template-settings"),
		do(func() { p().Tap(Tf("%s template", prompttmpl.MakerName)) }), frames(30), shot("86-template-builtin"),
		signal("done"),
	}
}
