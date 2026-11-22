package gui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/prompttmpl"
	"github.com/ironpark/mako/internal/protocol"
)

const agentTemplate = "---\ndescription: Lint a file\nargs: [{name: file, required: true}]\n---\nLint {{file}}.\n"

// templateApproval is an approval of the kind save_template asks.
func templateApproval(name, content string, replace bool) *protocol.Approval {
	input, _ := json.Marshal(map[string]any{"name": name, "content": content, "replace": replace})
	return &protocol.Approval{Id: "a-" + name, Tool: "save_template", Kind: protocol.ToolTemplate, Detail: "//" + name, Input: string(input)}
}

// templateModel is a window with a session of Claude, Chat's composer
// state for it, and no daemon.
func templateModel(t *testing.T) (*model, *chatState, string) {
	t.Helper()
	t.Setenv("MAKO_HOME", t.TempDir())
	dir, _ := config.TemplatesDir()
	m := newModel(config.Default(), "")
	m.post = func(func()) {} // no daemon to hear from
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "s", Agent: "claude", Project: "/tmp/p"}})
	return m, m.chatState("s"), dir
}

// templatesListed are the names the composer lists for a draft of two
// slashes.
func templatesListed(m *model, state *chatState) string {
	state.draft.Set("//")
	m.readTemplates(state, false)
	return ggui.Untrack(func() string {
		var names []string
		for _, it := range m.menuItems("s", state) {
			names = append(names, it.name)
		}
		return strings.Join(names, " ")
	})
}

// Allowed, the window saves the template in its own folder, answers it
// as saved, and lists it in // at once; the file is the agent's, as it
// wrote it.
func TestAWindowSavesAnAgentsTemplate(t *testing.T) {
	m, state, dir := templateModel(t)
	if got := templatesListed(m, state); got != "//"+prompttmpl.PluginMakerName+" //"+prompttmpl.MakerName {
		t.Fatalf("listed %q before", got)
	}
	m.Sessions.Set([]*protocol.SessionInfo{{Id: "s", Agent: "claude", Approval: templateApproval("lint", agentTemplate, false)}})
	a := ggui.Peek(m.approvalOfSession("s"))
	m.saveTemplateApproval("s", a)
	data, err := os.ReadFile(filepath.Join(dir, "lint.md"))
	if err != nil || string(data) != agentTemplate {
		t.Fatalf("saved %q, %v", data, err)
	}
	if got := templatesListed(m, state); got != "//lint //"+prompttmpl.PluginMakerName+" //"+prompttmpl.MakerName {
		t.Fatalf("listed %q after", got)
	}
	if ans := m.answerTemplate(templateApproval("lint", agentTemplate, true)); ans.GetDecision() != protocol.DecisionAllow || ans.GetOption() != protocol.TemplateSaved {
		t.Fatalf("a replace allowed is answered %v", ans)
	}
}

// What the window will not save it answers declined, saying why, and
// leaves the folder as it was: a name it has a template of, or a
// built-in's, without replace; a name that would leave the folder; a
// template that does not read.
func TestAWindowDeclinesWhatItWillNotSave(t *testing.T) {
	m, _, dir := templateModel(t)
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, "lint.md"), []byte("Mine"), 0o600)
	for _, c := range []struct {
		a    *protocol.Approval
		want string
	}{
		{templateApproval("lint", agentTemplate, false), errTemplateTaken.Error()},
		{templateApproval(prompttmpl.MakerName, agentTemplate, false), errTemplateTaken.Error()},
		{templateApproval("../escape", agentTemplate, true), "is not a template's name"},
		{templateApproval("a/b", agentTemplate, true), "is not a template's name"},
		{templateApproval(".hidden", agentTemplate, true), "is not a template's name"},
		{templateApproval("bad", "{{nope}}", false), "nope"},
		{&protocol.Approval{Id: "x", Kind: protocol.ToolTemplate, Input: "{"}, "does not read"},
	} {
		ans := m.answerTemplate(c.a)
		if ans.GetDecision() != protocol.DecisionDeny || !strings.Contains(ans.GetMessage(), c.want) {
			t.Errorf("%s: %v, want %q", c.a.GetInput(), ans, c.want)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "lint.md" {
		t.Fatalf("the folder holds %v", entries)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "lint.md")); string(data) != "Mine" {
		t.Fatalf("the user's template became %q", data)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape.md")); !os.IsNotExist(err) {
		t.Fatal("a template left the folder")
	}
}

// Chat's card shows the template asked for and saves it; one that would
// replace the user's shows the change, and one the agent did not ask to
// replace offers no save.
func TestTheTemplateCard(t *testing.T) {
	m, _, dir := templateModel(t)
	show := func(a *protocol.Approval) (*ggui.Probe, string) {
		m.Sessions.Set([]*protocol.SessionInfo{{Id: "s", Agent: "claude", Approval: a}})
		p := ggui.ProbeBuilder(func() ggui.Widget { return approvalCard(m, "s") }, ggui.Sz(640, 900))
		t.Cleanup(p.Close)
		p.Frame()
		return p, p.Semantics().String()
	}
	p, sem := show(templateApproval("lint", agentTemplate, false))
	for _, want := range []string{Tf("Save the template //%s?", "lint"), "Lint a file", T("Save template"), T("Decline")} {
		if !strings.Contains(sem, want) {
			t.Errorf("the card has no %q:\n%s", want, sem)
		}
	}
	p.Tap(T("Save template"))
	if data, err := os.ReadFile(filepath.Join(dir, "lint.md")); err != nil || string(data) != agentTemplate {
		t.Fatalf("Save template saved %q, %v", data, err)
	}

	_, sem = show(templateApproval("lint", strings.Replace(agentTemplate, "Lint {{file}}.", "Lint {{file}} twice.", 1), true))
	if !strings.Contains(sem, Tf("Replace your template //%s?", "lint")) || !strings.Contains(sem, T("Save template")) {
		t.Fatalf("a replace's card:\n%s", sem)
	}
	_, sem = show(templateApproval("lint", agentTemplate, false))
	if !strings.Contains(sem, Tf("You have a template named //%s, and the agent did not ask to replace it.", "lint")) || strings.Contains(sem, T("Save template")) {
		t.Fatalf("a name taken's card:\n%s", sem)
	}
}
