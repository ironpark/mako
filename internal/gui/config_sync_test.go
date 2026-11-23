package gui

import (
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
)

func TestSyncTogglesAndReloadSyncedConfig(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	cfg := config.Default()
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	m := newModel(cfg, "")

	if !ggui.Peek(m.WorkspaceSync) || !ggui.Peek(m.PluginSync) || !ggui.Peek(m.AgentSync) {
		t.Fatalf("expected all sync toggles on by default")
	}

	m.setPluginSync(false)
	m.setAgentSync(false)
	loaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Workspace.PluginsEnabled() || loaded.Workspace.AgentsEnabled() {
		t.Fatalf("expected disabled sync toggles persisted in config.json: %+v", loaded.Workspace)
	}

	// Simulate daemon writing a synced agent profile to config.json and notifying GUI.
	loaded.Agents["claude"] = agentspec.Agent{
		Command: []string{"claude"},
		Yolo:    true,
		Profiles: []agentspec.Profile{
			{Name: "synced", Model: "haiku"},
		},
	}
	if err := config.Save(loaded); err != nil {
		t.Fatal(err)
	}
	m.reloadSyncedConfig()

	if !m.cfg.Agents["claude"].Yolo {
		t.Fatalf("expected reloadSyncedConfig to refresh m.cfg.Agents")
	}
	foundProfile := false
	for _, d := range ggui.Peek(m.AgentDraft) {
		if d.Agent == "claude" && d.IsProfile && d.Profile == "synced" && d.Model == "haiku" {
			foundProfile = true
		}
	}
	if !foundProfile {
		t.Fatalf("expected reloadSyncedConfig to refresh AgentDraft, got %+v", ggui.Peek(m.AgentDraft))
	}

	// Unsaved local edits in AgentDraft are preserved across reloadSyncedConfig.
	if err := m.addAgent("unsaved-local"); err != nil {
		t.Fatal(err)
	}
	m.reloadSyncedConfig()
	foundUnsaved := false
	for _, d := range ggui.Peek(m.AgentDraft) {
		if !d.IsProfile && d.Agent == "unsaved-local" {
			foundUnsaved = true
		}
	}
	if !foundUnsaved {
		t.Fatalf("expected unsaved local AgentDraft edits preserved across reloadSyncedConfig")
	}
}
