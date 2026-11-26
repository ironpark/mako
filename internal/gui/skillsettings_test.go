package gui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/skills"
)

func TestSkillSettingsViewEditSaveAndReset(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MAKO_HOME", home)
	// Simulate an initialized Claude skills parent dir so sync propagates too.
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)

	m := newModel(&config.Config{NodeID: "n1"}, "")
	m.post = func(func()) {} // syntax highlighting wakes via m.post
	m.Page.Set("settings")
	m.Section.Set("skills")

	p := ggui.ProbeBuilder(func() ggui.Widget { return skillSettings(m) }, ggui.Sz(1100, 760))
	defer p.Close()

	// Unified built-in skill and its subfolder files should be listed.
	if _, ok := p.Find(Tf("%s skill", "mako")); !ok {
		t.Fatalf("missing mako skill item:\n%s", p.Semantics().String())
	}
	if _, ok := p.Find("cli/projects-and-agents.md"); !ok {
		t.Fatalf("missing cli/projects-and-agents.md file selector:\n%s", p.Semantics().String())
	}

	// Switch to Edit mode and verify the editor appears.
	p.Tap(T("Edit"))
	if _, ok := p.Find(T("Skill content")); !ok {
		t.Fatalf("expected Skill content editor after tapping Edit:\n%s", p.Semantics().String())
	}

	// Direct page workflow: edit SKILL.md -> save -> verify override & agent sync -> reset.
	pg := newSkillPage()
	edited := "---\nname: mako\ndescription: Edited unified guide\n---\nCustom body\n"
	pg.update("mako", func(d *skillDraft) { d.Content = edited })
	pg.save(m, "mako")

	d, ok := pg.draft("mako")
	if !ok || !d.Customized || d.dirty() {
		t.Fatalf("unexpected draft after save: %+v", d)
	}
	overridePath, err := skills.OverridePath("mako")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(overridePath)
	if err != nil || string(raw) != edited {
		t.Fatalf("override file = (%q, %v), want %q", string(raw), err, edited)
	}
	syncedPath := filepath.Join(claudeDir, "skills", "mako", "SKILL.md")
	synced, err := os.ReadFile(syncedPath)
	if err != nil || !strings.Contains(string(synced), "Custom body") {
		t.Fatalf("claude synced skill = (%q, %v), want Custom body", string(synced), err)
	}

	// Also edit a subfolder file (cli/projects-and-agents.md), save, verify sync, and reset.
	pg.selectFile("mako", "cli/projects-and-agents.md")
	editedSub := "# Custom CLI Reference\n"
	pg.update("mako", func(d *skillDraft) { d.Content = editedSub })
	pg.save(m, "mako")

	syncedSubPath := filepath.Join(claudeDir, "skills", "mako", "cli", "projects-and-agents.md")
	syncedSub, err := os.ReadFile(syncedSubPath)
	if err != nil || !strings.Contains(string(syncedSub), "Custom CLI Reference") {
		t.Fatalf("claude synced subfile = (%q, %v), want Custom CLI Reference", string(syncedSub), err)
	}

	// Reset subfile then reset SKILL.md.
	pg.reset(m, "mako")
	pg.selectFile("mako", "SKILL.md")
	pg.reset(m, "mako")

	d, _ = pg.draft("mako")
	if d.Customized || strings.Contains(d.Content, "Custom body") {
		t.Fatalf("expected reset draft, got customized=%v", d.Customized)
	}
	restored, err := os.ReadFile(syncedPath)
	if err != nil || strings.Contains(string(restored), "Custom body") {
		t.Fatalf("claude skill was not restored to built-in: %q (%v)", string(restored), err)
	}
}
