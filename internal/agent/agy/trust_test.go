package agy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/agent"
)

func TestTrustDirsCreatesAndPreservesSettings(t *testing.T) {
	home := t.TempDir()
	projMain := t.TempDir()
	projSub1 := t.TempDir()
	projSub2 := t.TempDir()

	realMain := agent.CanonicalDirs([]string{projMain})[0]
	realSub1 := agent.CanonicalDirs([]string{projSub1})[0]
	realSub2 := agent.CanonicalDirs([]string{projSub2})[0]

	path := filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	seed := "\xef\xbb\xbf" + `{"model":"gemini-4-neon-xhigh","trustedWorkspaces":["/already/trusted",` + mustJSON(t, realSub1) + `]}`
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	drv := New()
	l := &agent.Launch{Command: []string{"agy"}, Env: map[string]string{"HOME": home}}
	if err := drv.TrustDirs(l, []string{projMain, projSub1, projSub2}); err != nil {
		t.Fatalf("TrustDirs: %v", err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Model             string   `json:"model"`
		TrustedWorkspaces []string `json:"trustedWorkspaces"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal settings.json: %v", err)
	}
	if got.Model != "gemini-4-neon-xhigh" {
		t.Fatalf("lost model setting: %q", got.Model)
	}
	want := []string{"/already/trusted", realSub1, realMain, realSub2}
	if !slices.Equal(got.TrustedWorkspaces, want) {
		t.Fatalf("trustedWorkspaces = %q, want %q", got.TrustedWorkspaces, want)
	}

	// Second call when all dirs are already trusted must not rewrite the file.
	past := time.Now().Add(-10 * time.Second).Truncate(time.Second)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatal(err)
	}
	if err := drv.TrustDirs(l, []string{projMain, projSub1, projSub2}); err != nil {
		t.Fatalf("idempotent TrustDirs: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.ModTime().Equal(past) {
		t.Fatalf("expected no rewrite when already trusted; mtime changed from %v to %v", past, fi.ModTime())
	}
}

func TestTrustDirsFreshSymlinkCorruptAndConcurrent(t *testing.T) {
	home := t.TempDir()
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
	l := &agent.Launch{Command: []string{"agy"}, Env: map[string]string{"HOME": home}}
	if err := drv.TrustDirs(l, []string{linkDir}); err != nil {
		t.Fatalf("TrustDirs symlink: %v", err)
	}
	path := filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		TrustedWorkspaces []string `json:"trustedWorkspaces"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	wantDir := agent.CanonicalDirs([]string{realDir})[0]
	if !slices.Equal(got.TrustedWorkspaces, []string{wantDir}) {
		t.Fatalf("trustedWorkspaces = %q, want %q", got.TrustedWorkspaces, []string{wantDir})
	}

	// Corrupt settings.json must error and remain untouched.
	badHome := t.TempDir()
	badPath := filepath.Join(badHome, ".gemini", "antigravity-cli", "settings.json")
	if err := os.MkdirAll(filepath.Dir(badPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(badPath, []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := drv.TrustDirs(&agent.Launch{Env: map[string]string{"HOME": badHome}}, []string{realDir}); err == nil {
		t.Fatal("expected error for corrupt settings.json")
	}
	if raw, _ := os.ReadFile(badPath); string(raw) != "{bad" {
		t.Fatalf("corrupt file modified: %q", raw)
	}

	// Concurrent updates.
	concHome := t.TempDir()
	dirs := make([]string, 8)
	for i := range dirs {
		dirs[i] = t.TempDir()
	}
	cl := &agent.Launch{Env: map[string]string{"HOME": concHome}}
	var wg sync.WaitGroup
	for _, d := range dirs {
		wg.Go(func() {
			if err := drv.TrustDirs(cl, []string{d}); err != nil {
				t.Errorf("TrustDirs(%q): %v", d, err)
			}
		})
	}
	wg.Wait()

	body, err = os.ReadFile(filepath.Join(concHome, ".gemini", "antigravity-cli", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	got.TrustedWorkspaces = nil
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	for _, d := range agent.CanonicalDirs(dirs) {
		if !slices.Contains(got.TrustedWorkspaces, d) {
			t.Fatalf("missing concurrent dir %q in %q", d, got.TrustedWorkspaces)
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
