package skills

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
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

	for _, p := range []string{
		filepath.Join(agyHome, ".gemini", "config", "skills", "mako-plugin", "SKILL.md"),
		filepath.Join(agyHome, ".gemini", "config", "skills", "mako-template", "SKILL.md"),
		filepath.Join(claudeDir, "skills", "mako-plugin", "SKILL.md"),
		filepath.Join(claudeDir, "skills", "mako-template", "SKILL.md"),
		filepath.Join(codexDir, "skills", "mako-plugin", "SKILL.md"),
		filepath.Join(codexDir, "skills", "mako-template", "SKILL.md"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected skill at %s: %v", p, err)
		}
	}
}
