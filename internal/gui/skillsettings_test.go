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

	// Both built-in skills should be listed.
	if _, ok := p.Find(Tf("%s skill", "mako-plugin")); !ok {
		t.Fatalf("missing mako-plugin skill item:\n%s", p.Semantics().String())
	}
	if _, ok := p.Find(Tf("%s skill", "mako-template")); !ok {
		t.Fatalf("missing mako-template skill item:\n%s", p.Semantics().String())
	}

	// Switch to Edit mode and verify the editor appears.
	p.Tap(T("Edit"))
	if _, ok := p.Find(T("Skill content")); !ok {
		t.Fatalf("expected Skill content editor after tapping Edit:\n%s", p.Semantics().String())
	}

	// Direct page workflow: edit -> save -> verify override & agent sync -> reset.
	pg := newSkillPage()
	edited := "---\nname: mako-plugin\ndescription: Edited plugin guide\n---\nCustom body\n"
	pg.update("mako-plugin", func(d *skillDraft) { d.Content = edited })
	pg.save(m, "mako-plugin")

	d, ok := pg.draft("mako-plugin")
	if !ok || !d.Customized || d.dirty() {
		t.Fatalf("unexpected draft after save: %+v", d)
	}
	// Verify saved file in MAKO_HOME/skills/mako-plugin/SKILL.md.
	overridePath, err := skills.OverridePath("mako-plugin")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(overridePath)
	if err != nil || string(raw) != edited {
		t.Fatalf("override file = (%q, %v), want %q", string(raw), err, edited)
	}
	// Verify synced to Claude skills directory.
	syncedPath := filepath.Join(claudeDir, "skills", "mako-plugin", "SKILL.md")
	synced, err := os.ReadFile(syncedPath)
	if err != nil || !strings.Contains(string(synced), "Custom body") {
		t.Fatalf("claude synced skill = (%q, %v), want Custom body", string(synced), err)
	}

	// Reset restores built-in content both in draft and in Claude's skills dir.
	pg.reset(m, "mako-plugin")
	d, _ = pg.draft("mako-plugin")
	if d.Customized || strings.Contains(d.Content, "Custom body") {
		t.Fatalf("expected reset draft, got customized=%v", d.Customized)
	}
	restored, err := os.ReadFile(syncedPath)
	if err != nil || strings.Contains(string(restored), "Custom body") {
		t.Fatalf("claude skill was not restored to built-in: %q (%v)", string(restored), err)
	}
}
