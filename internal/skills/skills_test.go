package skills

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestBuiltinSkillsHaveFrontmatterAndMarker(t *testing.T) {
	list := Builtin()
	wantNames := []string{"mako-plugin", "mako-template"}
	if got := Names(); !slices.Equal(got, wantNames) {
		t.Fatalf("Names() = %v, want %v", got, wantNames)
	}
	for _, sk := range list {
		text := string(sk.Content)
		if !strings.HasPrefix(text, "---\nname: "+sk.Name+"\n") {
			t.Errorf("%s missing expected YAML frontmatter name", sk.Name)
		}
		if !strings.Contains(text, "description:") {
			t.Errorf("%s missing frontmatter description", sk.Name)
		}
		if !strings.Contains(text, ManagedMarker) {
			t.Errorf("%s missing ManagedMarker %q", sk.Name, ManagedMarker)
		}
	}
}

func TestSyncToDirInstallsUpdatesAndPreservesUserOverrides(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	root := t.TempDir()

	// Initial install creates both managed skills.
	if err := SyncToDir(root); err != nil {
		t.Fatal(err)
	}
	for _, name := range Names() {
		b, err := os.ReadFile(filepath.Join(root, name, "SKILL.md"))
		if err != nil {
			t.Fatalf("missing installed skill %s: %v", name, err)
		}
		if !strings.Contains(string(b), ManagedMarker) {
			t.Fatalf("installed skill %s missing marker", name)
		}
	}

	// An older managed file is refreshed on sync.
	pluginPath := filepath.Join(root, "mako-plugin", "SKILL.md")
	if err := os.WriteFile(pluginPath, []byte("old managed\n"+ManagedMarker+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A user-customized file without ManagedMarker is preserved untouched.
	tmplPath := filepath.Join(root, "mako-template", "SKILL.md")
	custom := "---\nname: mako-template\ndescription: custom\n---\nUser custom skill\n"
	if err := os.WriteFile(tmplPath, []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SyncToDir(root); err != nil {
		t.Fatal(err)
	}
	gotPlugin, _ := os.ReadFile(pluginPath)
	if !strings.Contains(string(gotPlugin), "Revision 44") {
		t.Fatalf("managed mako-plugin skill was not refreshed: %s", string(gotPlugin))
	}
	gotTmpl, _ := os.ReadFile(tmplPath)
	if string(gotTmpl) != custom {
		t.Fatalf("user-customized mako-template skill was overwritten:\n%s", string(gotTmpl))
	}
}

func TestEnsureAgentSkillsResolvesPerAgentEnv(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	agyHome := t.TempDir()
	claudeDir := t.TempDir()
	codexDir := t.TempDir()

	if err := EnsureAgentSkills("agy", map[string]string{"HOME": agyHome}); err != nil {
		t.Fatal(err)
	}
	if err := EnsureAgentSkills("claude", map[string]string{"CLAUDE_CONFIG_DIR": claudeDir}); err != nil {
		t.Fatal(err)
	}
	if err := EnsureAgentSkills("codex", map[string]string{"CODEX_HOME": codexDir}); err != nil {
		t.Fatal(err)
	}
	piDir := t.TempDir()
	piHome := t.TempDir()
	ompDir := t.TempDir()
	ompHome := t.TempDir()
	omoDir := t.TempDir()
	omoHome := t.TempDir()
	openCodeHome := t.TempDir()

	for _, name := range []string{"omp", "omo"} {
		if !slices.Contains(SupportedAgents, name) {
			t.Fatalf("SupportedAgents missing %q: %v", name, SupportedAgents)
		}
	}
	if err := EnsureAgentSkills("pi", map[string]string{"PI_CODING_AGENT_DIR": piDir}); err != nil {
		t.Fatal(err)
	}
	if err := EnsureAgentSkills("pi", map[string]string{"HOME": piHome, "PI_CODING_AGENT_DIR": ""}); err != nil {
		t.Fatal(err)
	}
	if err := EnsureAgentSkills("omp", map[string]string{"PI_CODING_AGENT_DIR": ompDir}); err != nil {
		t.Fatal(err)
	}
	if err := EnsureAgentSkills("omp", map[string]string{"HOME": ompHome, "PI_CODING_AGENT_DIR": ""}); err != nil {
		t.Fatal(err)
	}
	if err := EnsureAgentSkills("omo", map[string]string{"OMO_CODING_AGENT_DIR": omoDir}); err != nil {
		t.Fatal(err)
	}
	if err := EnsureAgentSkills("omo", map[string]string{"HOME": omoHome, "OMO_CODING_AGENT_DIR": ""}); err != nil {
		t.Fatal(err)
	}
	if err := EnsureAgentSkills("opencode", map[string]string{"HOME": openCodeHome}); err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{
		filepath.Join(agyHome, ".gemini", "config", "skills", "mako-plugin", "SKILL.md"),
		filepath.Join(agyHome, ".gemini", "config", "skills", "mako-template", "SKILL.md"),
		filepath.Join(claudeDir, "skills", "mako-plugin", "SKILL.md"),
		filepath.Join(claudeDir, "skills", "mako-template", "SKILL.md"),
		filepath.Join(codexDir, "skills", "mako-plugin", "SKILL.md"),
		filepath.Join(codexDir, "skills", "mako-template", "SKILL.md"),
		filepath.Join(piDir, "skills", "mako-template", "SKILL.md"),
		filepath.Join(piHome, ".pi", "agent", "skills", "mako-template", "SKILL.md"),
		filepath.Join(ompDir, "skills", "mako-template", "SKILL.md"),
		filepath.Join(ompHome, ".omp", "agent", "skills", "mako-template", "SKILL.md"),
		filepath.Join(omoDir, "skills", "mako-template", "SKILL.md"),
		filepath.Join(omoHome, ".omo", "agent", "skills", "mako-template", "SKILL.md"),
		filepath.Join(openCodeHome, ".config", "opencode", "skills", "mako-template", "SKILL.md"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected skill at %s: %v", p, err)
		}
	}
}

func TestSaveAndResetOverrideSyncsToAgents(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	agentDir := t.TempDir()

	// Initial sync writes the embedded default.
	if err := SyncToDir(agentDir); err != nil {
		t.Fatal(err)
	}
	sk, ok := EffectiveByName("mako-template")
	if !ok || sk.Customized {
		t.Fatalf("initial EffectiveByName = (%+v, %v), want not customized", sk, ok)
	}

	// Saving an override updates Effective() and propagates to managed agent files.
	custom := "---\nname: mako-template\ndescription: Custom workflow\n---\nEdited body\n"
	if err := SaveOverride("mako-template", []byte(custom)); err != nil {
		t.Fatalf("SaveOverride: %v", err)
	}
	sk, ok = EffectiveByName("mako-template")
	if !ok || !sk.Customized || sk.Description != "Custom workflow" || string(sk.Content) != custom {
		t.Fatalf("after SaveOverride: %+v", sk)
	}
	if err := SyncToDir(agentDir); err != nil {
		t.Fatal(err)
	}
	synced, err := os.ReadFile(filepath.Join(agentDir, "mako-template", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(synced), "Edited body") || !strings.Contains(string(synced), ManagedMarker) {
		t.Fatalf("synced agent skill missing edited body or marker:\n%s", string(synced))
	}

	// Resetting removes the override and restores the embedded default on next sync.
	if err := ResetOverride("mako-template"); err != nil {
		t.Fatalf("ResetOverride: %v", err)
	}
	sk, ok = EffectiveByName("mako-template")
	if !ok || sk.Customized {
		t.Fatalf("after ResetOverride: %+v, want not customized", sk)
	}
	if err := SyncToDir(agentDir); err != nil {
		t.Fatal(err)
	}
	restored, _ := os.ReadFile(filepath.Join(agentDir, "mako-template", "SKILL.md"))
	if strings.Contains(string(restored), "Edited body") {
		t.Fatalf("expected built-in content restored, got:\n%s", string(restored))
	}
}

func TestSyncAndSaveOverrideConcurrent(t *testing.T) {
	makoHome := t.TempDir()
	t.Setenv("MAKO_HOME", makoHome)
	agentDir := t.TempDir()

	const workers = 8
	errs := make(chan error, workers*2)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			content := []byte("---\nname: mako-template\ndescription: concurrent\n---\nBody " + string(rune('A'+i)) + "\n")
			errs <- SaveOverride("mako-template", content)
		}(i)
		go func() {
			defer wg.Done()
			errs <- SyncToDir(agentDir)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent skill operation failed: %v", err)
		}
	}
	// Final sync picks up latest override cleanly.
	if err := SyncToDir(agentDir); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{
		filepath.Join(makoHome, "skills", "mako-template"),
		filepath.Join(agentDir, "mako-template"),
	} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.Name() != "SKILL.md" {
				t.Fatalf("unexpected leftover temp file %q in %s", e.Name(), dir)
			}
		}
	}
}
