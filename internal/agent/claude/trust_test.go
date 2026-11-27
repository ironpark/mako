package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/agent"
)

func TestTrustDirsCreatesAndPreservesState(t *testing.T) {
	cfgDir := t.TempDir()
	projMain := t.TempDir()
	projSub1 := t.TempDir()
	projSub2 := t.TempDir()

	// Seed existing .claude.json with unrelated top-level and per-project fields.
	statePath := filepath.Join(cfgDir, ".claude.json")
	realMain := agent.CanonicalDirs([]string{projMain})[0]
	realSub1 := agent.CanonicalDirs([]string{projSub1})[0]
	realSub2 := agent.CanonicalDirs([]string{projSub2})[0]
	seed := map[string]any{
		"numStartups":            42,
		"hasCompletedOnboarding": true,
		"projects": map[string]any{
			realMain: map[string]any{
				"allowedTools":           []string{"Bash(git *)"},
				"hasTrustDialogAccepted": false,
				"lastCost":               1.23,
			},
			"/existing/untouched": map[string]any{
				"hasTrustDialogAccepted": true,
			},
		},
	}
	seedBytes, err := json.MarshalIndent(seed, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, append([]byte("\xef\xbb\xbf"), seedBytes...), 0o600); err != nil {
		t.Fatal(err)
	}

	drv := New()
	l := &agent.Launch{
		Command: []string{"claude"},
		Env:     map[string]string{"CLAUDE_CONFIG_DIR": cfgDir},
	}
	if err := drv.TrustDirs(l, []string{projMain, projSub1, projSub2}); err != nil {
		t.Fatalf("TrustDirs: %v", err)
	}

	var got struct {
		NumStartups            int                       `json:"numStartups"`
		HasCompletedOnboarding bool                      `json:"hasCompletedOnboarding"`
		Projects               map[string]map[string]any `json:"projects"`
	}
	body, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal updated .claude.json: %v", err)
	}
	if got.NumStartups != 42 || !got.HasCompletedOnboarding {
		t.Fatalf("lost top-level fields: %+v", got)
	}
	for _, dir := range []string{realMain, realSub1, realSub2, "/existing/untouched"} {
		if got.Projects[dir]["hasTrustDialogAccepted"] != true {
			t.Fatalf("project %q not trusted: %+v", dir, got.Projects[dir])
		}
	}
	if got.Projects[realMain]["lastCost"] != 1.23 {
		t.Fatalf("lost existing project field: %+v", got.Projects[realMain])
	}

	// Second call when all dirs are already trusted must not rewrite the file.
	past := time.Now().Add(-10 * time.Second).Truncate(time.Second)
	if err := os.Chtimes(statePath, past, past); err != nil {
		t.Fatal(err)
	}
	if err := drv.TrustDirs(l, []string{projMain, projSub1, projSub2}); err != nil {
		t.Fatalf("idempotent TrustDirs: %v", err)
	}
	fi, err := os.Stat(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.ModTime().Equal(past) {
		t.Fatalf("expected no rewrite when already trusted; mtime changed from %v to %v", past, fi.ModTime())
	}
}

func TestTrustDirsFallsBackToHomeWhenConfigDirUnset(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(t.TempDir(), "should-not-be-used"))

	drv := New()
	// Explicitly empty CLAUDE_CONFIG_DIR in Launch.Env falls back to HOME.
	l := &agent.Launch{
		Command: []string{"claude"},
		Env:     map[string]string{"HOME": home, "CLAUDE_CONFIG_DIR": ""},
	}
	if err := drv.TrustDirs(l, []string{proj}); err != nil {
		t.Fatalf("TrustDirs: %v", err)
	}
	statePath := filepath.Join(home, ".claude.json")
	body, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("expected %s: %v", statePath, err)
	}
	var got struct {
		Projects map[string]struct {
			Accepted bool `json:"hasTrustDialogAccepted"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	realProj := agent.CanonicalDirs([]string{proj})[0]
	if !got.Projects[realProj].Accepted {
		t.Fatalf("project %q not trusted in %s: %+v", realProj, statePath, got)
	}
}

func TestTrustDirsResolvesSymlinksAndRefusesCorruptJSON(t *testing.T) {
	cfgDir := t.TempDir()
	base := t.TempDir()
	realDir := filepath.Join(base, "real")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(base, "link")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	drv := New()
	l := &agent.Launch{Command: []string{"claude"}, Env: map[string]string{"CLAUDE_CONFIG_DIR": cfgDir}}
	if err := drv.TrustDirs(l, []string{linkDir}); err != nil {
		t.Fatalf("TrustDirs symlink: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(cfgDir, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Projects map[string]map[string]any `json:"projects"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	wantKey := agent.CanonicalDirs([]string{realDir})[0]
	if got.Projects[wantKey]["hasTrustDialogAccepted"] != true {
		t.Fatalf("expected canonical key %q, got %+v", wantKey, got.Projects)
	}

	// Corrupt JSON must error and remain untouched.
	badCfg := t.TempDir()
	badPath := filepath.Join(badCfg, ".claude.json")
	if err := os.WriteFile(badPath, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := drv.TrustDirs(&agent.Launch{Env: map[string]string{"CLAUDE_CONFIG_DIR": badCfg}}, []string{realDir}); err == nil {
		t.Fatal("expected error for corrupt .claude.json")
	}
	if raw, _ := os.ReadFile(badPath); string(raw) != "{broken" {
		t.Fatalf("corrupt file was modified: %q", raw)
	}
}

func TestTrustDirsConcurrent(t *testing.T) {
	cfgDir := t.TempDir()
	dirs := make([]string, 8)
	for i := range dirs {
		dirs[i] = t.TempDir()
	}
	drv := New()
	l := &agent.Launch{Command: []string{"claude"}, Env: map[string]string{"CLAUDE_CONFIG_DIR": cfgDir}}

	var wg sync.WaitGroup
	for _, d := range dirs {
		wg.Go(func() {
			if err := drv.TrustDirs(l, []string{d}); err != nil {
				t.Errorf("TrustDirs(%q): %v", d, err)
			}
		})
	}
	wg.Wait()

	body, err := os.ReadFile(filepath.Join(cfgDir, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Projects map[string]struct {
			Accepted bool `json:"hasTrustDialogAccepted"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	for _, d := range agent.CanonicalDirs(dirs) {
		if !got.Projects[d].Accepted {
			t.Fatalf("missing concurrent dir %q", d)
		}
	}
}
