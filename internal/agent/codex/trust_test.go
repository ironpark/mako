package codex

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/ironpark/mako/internal/agent"
)

func TestTrustDirsCreatesAndPreservesConfigToml(t *testing.T) {
	codexHome := t.TempDir()
	projMain := t.TempDir()
	projSub1 := t.TempDir()
	projSub2 := t.TempDir()

	realMain := agent.CanonicalDirs([]string{projMain})[0]
	realSub1 := agent.CanonicalDirs([]string{projSub1})[0]
	realSub2 := agent.CanonicalDirs([]string{projSub2})[0]

	cfgPath := filepath.Join(codexHome, "config.toml")
	initial := `# User Codex settings
model = "gpt-6.2-sol"

[tui]
screen_reader_detection_done = true

[projects.` + tomlString(realMain) + `]
# keep this comment
trust_level = "untrusted" # inline comment
sandbox_mode = "workspace-write"

[projects.` + tomlString(realMain) + `.nested]
flag = true

[projects.'` + realSub1 + `']
approval_policy = "on-request"
`
	if err := os.WriteFile(cfgPath, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	drv := New()
	l := &agent.Launch{
		Command: []string{"codex"},
		Env:     map[string]string{"CODEX_HOME": codexHome},
	}
	if err := drv.TrustDirs(l, []string{projMain, projSub1, projSub2}); err != nil {
		t.Fatalf("TrustDirs: %v", err)
	}

	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, keep := range []string{"# User Codex settings", "# keep this comment", "# inline comment", `sandbox_mode = "workspace-write"`, `approval_policy = "on-request"`, "flag = true"} {
		if !strings.Contains(text, keep) {
			t.Fatalf("expected %q to be preserved in config.toml:\n%s", keep, text)
		}
	}

	var parsed struct {
		Model    string `toml:"model"`
		Projects map[string]struct {
			TrustLevel     string         `toml:"trust_level"`
			SandboxMode    string         `toml:"sandbox_mode"`
			ApprovalPolicy string         `toml:"approval_policy"`
			Nested         map[string]any `toml:"nested"`
		} `toml:"projects"`
	}
	if _, err := toml.Decode(text, &parsed); err != nil {
		t.Fatalf("updated config.toml is invalid TOML: %v\n%s", err, text)
	}
	if parsed.Model != "gpt-6.2-sol" {
		t.Fatalf("model = %q", parsed.Model)
	}
	for _, dir := range []string{realMain, realSub1, realSub2} {
		if parsed.Projects[dir].TrustLevel != "trusted" {
			t.Fatalf("project %q trust_level = %q, want trusted\n%s", dir, parsed.Projects[dir].TrustLevel, text)
		}
	}
	if parsed.Projects[realMain].SandboxMode != "workspace-write" {
		t.Fatalf("lost sandbox_mode: %+v", parsed.Projects[realMain])
	}
	if parsed.Projects[realSub1].ApprovalPolicy != "on-request" {
		t.Fatalf("lost approval_policy: %+v", parsed.Projects[realSub1])
	}

	// Idempotent: second call must not rewrite the file.
	past := time.Now().Add(-10 * time.Second).Truncate(time.Second)
	if err := os.Chtimes(cfgPath, past, past); err != nil {
		t.Fatal(err)
	}
	if err := drv.TrustDirs(l, []string{projMain, projSub1, projSub2}); err != nil {
		t.Fatalf("idempotent TrustDirs: %v", err)
	}
	fi, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.ModTime().Equal(past) {
		t.Fatalf("expected no rewrite when already trusted; mtime changed from %v to %v", past, fi.ModTime())
	}
}

func TestTrustDirsNewConfigAndDottedKeys(t *testing.T) {
	// Brand-new config.toml under HOME/.codex when CODEX_HOME is unset.
	home := t.TempDir()
	proj := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(t.TempDir(), "ignored"))

	drv := New()
	l := &agent.Launch{
		Command: []string{"codex"},
		Env:     map[string]string{"HOME": home, "CODEX_HOME": ""},
	}
	if err := drv.TrustDirs(l, []string{proj}); err != nil {
		t.Fatalf("TrustDirs fresh: %v", err)
	}
	cfgPath := filepath.Join(home, ".codex", "config.toml")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("expected %s: %v", cfgPath, err)
	}
	var parsed trustConfig
	if _, err := toml.Decode(string(raw), &parsed); err != nil {
		t.Fatalf("decode fresh config: %v\n%s", err, raw)
	}
	realProj := agent.CanonicalDirs([]string{proj})[0]
	if parsed.Projects[realProj].TrustLevel != "trusted" {
		t.Fatalf("fresh project not trusted: %+v", parsed.Projects)
	}

	// Dotted key form at root and under [projects].
	codexHome := t.TempDir()
	d1, d2 := agent.CanonicalDirs([]string{t.TempDir()})[0], agent.CanonicalDirs([]string{t.TempDir()})[0]
	dotted := "projects." + tomlString(d1) + `.trust_level = "untrusted"` + "\n\n[projects]\n" + tomlString(d2) + `.trust_level = 'untrusted'` + "\n"
	dottedPath := filepath.Join(codexHome, "config.toml")
	if err := os.WriteFile(dottedPath, []byte(dotted), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := drv.TrustDirs(&agent.Launch{Env: map[string]string{"CODEX_HOME": codexHome}}, []string{d1, d2}); err != nil {
		t.Fatalf("TrustDirs dotted: %v", err)
	}
	raw, err = os.ReadFile(dottedPath)
	if err != nil {
		t.Fatal(err)
	}
	parsed = trustConfig{}
	if _, err := toml.Decode(string(raw), &parsed); err != nil {
		t.Fatalf("decode dotted: %v\n%s", err, raw)
	}
	if parsed.Projects[d1].TrustLevel != "trusted" || parsed.Projects[d2].TrustLevel != "trusted" {
		t.Fatalf("dotted entries not trusted: %+v\n%s", parsed.Projects, raw)
	}
}

func TestTrustDirsRefusesCorruptTomlAndHandlesConcurrency(t *testing.T) {
	badHome := t.TempDir()
	badPath := filepath.Join(badHome, "config.toml")
	if err := os.WriteFile(badPath, []byte("[broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	drv := New()
	if err := drv.TrustDirs(&agent.Launch{Env: map[string]string{"CODEX_HOME": badHome}}, []string{t.TempDir()}); err == nil {
		t.Fatal("expected error for corrupt config.toml")
	}
	if raw, _ := os.ReadFile(badPath); string(raw) != "[broken\n" {
		t.Fatalf("corrupt config.toml was modified: %q", raw)
	}

	// Concurrent updates.
	codexHome := t.TempDir()
	dirs := make([]string, 8)
	for i := range dirs {
		dirs[i] = t.TempDir()
	}
	l := &agent.Launch{Env: map[string]string{"CODEX_HOME": codexHome}}
	var wg sync.WaitGroup
	for _, d := range dirs {
		wg.Go(func() {
			if err := drv.TrustDirs(l, []string{d}); err != nil {
				t.Errorf("TrustDirs(%q): %v", d, err)
			}
		})
	}
	wg.Wait()

	raw, err := os.ReadFile(filepath.Join(codexHome, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	var parsed trustConfig
	if _, err := toml.Decode(string(raw), &parsed); err != nil {
		t.Fatalf("decode concurrent config: %v\n%s", err, raw)
	}
	for _, d := range agent.CanonicalDirs(dirs) {
		if parsed.Projects[d].TrustLevel != "trusted" {
			t.Fatalf("missing concurrent dir %q", d)
		}
	}
}
