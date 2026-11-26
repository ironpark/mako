package skills

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestBuiltinSkillsHaveFrontmatterFoldersAndMarker(t *testing.T) {
	list := Builtin()
	wantNames := []string{"mako"}
	if got := Names(); !slices.Equal(got, wantNames) {
		t.Fatalf("Names() = %v, want %v", got, wantNames)
	}
	sk := list[0]
	text := string(sk.Content)
	if !strings.HasPrefix(text, "---\nname: "+sk.Name+"\n") {
		t.Errorf("%s missing expected YAML frontmatter name", sk.Name)
	}
	if sk.Description == "" || strings.HasPrefix(sk.Description, ">") {
		t.Errorf("%s frontmatter folded description not parsed: %q", sk.Name, sk.Description)
	}
	if !strings.Contains(text, ManagedMarker) {
		t.Errorf("%s missing ManagedMarker %q", sk.Name, ManagedMarker)
	}
	wantFiles := []string{
		"SKILL.md",
		"cli/projects-and-agents.md",
		"plugins/authoring.md",
		"plugins/ui-components.md",
		"templates/scripting.md",
		"templates/syntax.md",
	}
	var gotFiles []string
	for _, f := range sk.Files {
		gotFiles = append(gotFiles, f.Path)
		if !strings.Contains(string(f.Content), ManagedMarker) {
			t.Errorf("%s/%s missing ManagedMarker", sk.Name, f.Path)
		}
	}
	if !slices.Equal(gotFiles, wantFiles) {
		t.Fatalf("mako skill files = %v, want %v", gotFiles, wantFiles)
	}
}

func TestSyncToDirInstallsUpdatesPreservesUserOverridesAndPrunesLegacy(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	root := t.TempDir()

	// Seed a legacy managed mako-plugin skill (should be pruned) and a
	// manually-authored legacy mako-template skill without ManagedMarker (must stay).
	legacyPlugin := filepath.Join(root, "mako-plugin", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(legacyPlugin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPlugin, []byte("old managed\n"+ManagedMarker+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	legacyTmpl := filepath.Join(root, "mako-template", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(legacyTmpl), 0o755); err != nil {
		t.Fatal(err)
	}
	manualLegacy := "---\nname: mako-template\ndescription: custom\n---\nUser custom legacy skill\n"
	if err := os.WriteFile(legacyTmpl, []byte(manualLegacy), 0o644); err != nil {
		t.Fatal(err)
	}

	// Initial install creates the unified multi-folder mako skill and prunes managed legacy.
	if err := SyncToDir(root); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"SKILL.md", "cli/projects-and-agents.md", "plugins/authoring.md", "plugins/ui-components.md", "templates/syntax.md", "templates/scripting.md"} {
		b, err := os.ReadFile(filepath.Join(root, "mako", filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("missing installed file mako/%s: %v", rel, err)
		}
		if !strings.Contains(string(b), ManagedMarker) {
			t.Fatalf("installed file mako/%s missing marker", rel)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "mako-plugin")); !os.IsNotExist(err) {
		t.Fatalf("managed legacy mako-plugin should have been pruned, stat err=%v", err)
	}
	gotManual, err := os.ReadFile(legacyTmpl)
	if err != nil || string(gotManual) != manualLegacy {
		t.Fatalf("manually placed legacy mako-template was altered: (%q, %v)", string(gotManual), err)
	}

	// An older managed file inside mako/ is refreshed on sync, while a user-edited
	// file without ManagedMarker is preserved untouched.
	managedSub := filepath.Join(root, "mako", "plugins", "authoring.md")
	if err := os.WriteFile(managedSub, []byte("old managed\n"+ManagedMarker+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	userSub := filepath.Join(root, "mako", "cli", "projects-and-agents.md")
	customCLI := "# My custom CLI notes\n"
	if err := os.WriteFile(userSub, []byte(customCLI), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SyncToDir(root); err != nil {
		t.Fatal(err)
	}
	gotManaged, _ := os.ReadFile(managedSub)
	if !strings.Contains(string(gotManaged), "Revision 46") {
		t.Fatalf("managed plugins/authoring.md was not refreshed: %s", string(gotManaged))
	}
	gotUser, _ := os.ReadFile(userSub)
	if string(gotUser) != customCLI {
		t.Fatalf("user-customized cli/projects-and-agents.md was overwritten:\n%s", string(gotUser))
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
		filepath.Join(agyHome, ".gemini", "config", "skills", "mako", "SKILL.md"),
		filepath.Join(agyHome, ".gemini", "config", "skills", "mako", "cli", "projects-and-agents.md"),
		filepath.Join(claudeDir, "skills", "mako", "SKILL.md"),
		filepath.Join(claudeDir, "skills", "mako", "plugins", "authoring.md"),
		filepath.Join(codexDir, "skills", "mako", "SKILL.md"),
		filepath.Join(codexDir, "skills", "mako", "templates", "syntax.md"),
		filepath.Join(piDir, "skills", "mako", "SKILL.md"),
		filepath.Join(piHome, ".pi", "agent", "skills", "mako", "SKILL.md"),
		filepath.Join(ompDir, "skills", "mako", "SKILL.md"),
		filepath.Join(ompHome, ".omp", "agent", "skills", "mako", "SKILL.md"),
		filepath.Join(omoDir, "skills", "mako", "SKILL.md"),
		filepath.Join(omoHome, ".omo", "agent", "skills", "mako", "SKILL.md"),
		filepath.Join(openCodeHome, ".config", "opencode", "skills", "mako", "SKILL.md"),
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
	sk, ok := EffectiveByName("mako")
	if !ok || sk.Customized {
		t.Fatalf("initial EffectiveByName = (%+v, %v), want not customized", sk, ok)
	}

	// Saving an override on SKILL.md and a subfolder file updates Effective() and syncs.
	custom := "---\nname: mako\ndescription: Custom workflow\n---\nEdited body\n"
	if err := SaveOverride("mako", []byte(custom)); err != nil {
		t.Fatalf("SaveOverride: %v", err)
	}
	customCLI := "# Custom CLI guide\n"
	if err := SaveOverrideFile("mako", "cli/projects-and-agents.md", []byte(customCLI)); err != nil {
		t.Fatalf("SaveOverrideFile: %v", err)
	}
	sk, ok = EffectiveByName("mako")
	if !ok || !sk.Customized || sk.Description != "Custom workflow" || string(sk.Content) != custom {
		t.Fatalf("after SaveOverride: %+v", sk)
	}
	cliFile, ok := sk.File("cli/projects-and-agents.md")
	if !ok || !cliFile.Customized || string(cliFile.Content) != customCLI {
		t.Fatalf("after SaveOverrideFile: %+v", cliFile)
	}
	if err := SyncToDir(agentDir); err != nil {
		t.Fatal(err)
	}
	synced, err := os.ReadFile(filepath.Join(agentDir, "mako", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(synced), "Edited body") || !strings.Contains(string(synced), ManagedMarker) {
		t.Fatalf("synced agent skill missing edited body or marker:\n%s", string(synced))
	}
	syncedCLI, err := os.ReadFile(filepath.Join(agentDir, "mako", "cli", "projects-and-agents.md"))
	if err != nil || !strings.Contains(string(syncedCLI), "Custom CLI guide") {
		t.Fatalf("synced subfile missing custom body: (%q, %v)", string(syncedCLI), err)
	}

	// Resetting removes all overrides and restores embedded defaults on next sync.
	if err := ResetOverride("mako"); err != nil {
		t.Fatalf("ResetOverride: %v", err)
	}
	sk, ok = EffectiveByName("mako")
	if !ok || sk.Customized {
		t.Fatalf("after ResetOverride: %+v, want not customized", sk)
	}
	if err := SyncToDir(agentDir); err != nil {
		t.Fatal(err)
	}
	restored, _ := os.ReadFile(filepath.Join(agentDir, "mako", "SKILL.md"))
	if strings.Contains(string(restored), "Edited body") {
		t.Fatalf("expected built-in content restored, got:\n%s", string(restored))
	}
	restoredCLI, _ := os.ReadFile(filepath.Join(agentDir, "mako", "cli", "projects-and-agents.md"))
	if strings.Contains(string(restoredCLI), "Custom CLI guide") {
		t.Fatalf("expected built-in subfile restored, got:\n%s", string(restoredCLI))
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
			content := []byte("---\nname: mako\ndescription: concurrent\n---\nBody " + string(rune('A'+i)) + "\n")
			errs <- SaveOverride("mako", content)
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
	if err := SyncToDir(agentDir); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(makoHome, "skills", "mako"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "SKILL.md" {
			t.Fatalf("unexpected leftover temp file %q", e.Name())
		}
	}
}
