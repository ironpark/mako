package agentspec

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSupportsHomeIsolation(t *testing.T) {
	if !SupportsHomeIsolation("claude") {
		t.Fatal("claude should support home isolation")
	}
	if !SupportsHomeIsolation("codex") {
		t.Fatal("codex should support home isolation")
	}
	if SupportsHomeIsolation("agy") != (runtime.GOOS != "windows") {
		t.Fatalf("agy isolation mismatch for OS %s", runtime.GOOS)
	}
	if SupportsHomeIsolation("shell") {
		t.Fatal("shell should not support home isolation")
	}
}

func TestResolveWithIsolateHome(t *testing.T) {
	realHome := t.TempDir()
	makoHome := t.TempDir()
	t.Setenv("HOME", realHome)
	t.Setenv("MAKO_HOME", makoHome)

	if err := os.WriteFile(filepath.Join(realHome, ".gitconfig"), []byte("[user]\nname = Test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(realHome, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(realHome, ".gemini", "antigravity-cli"), 0o755); err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	for _, bin := range []string{"claude", "codex", "agy"} {
		p := filepath.Join(binDir, bin)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	agents := map[string]Agent{
		"claude": {},
		"codex":  {},
		"agy":    {},
	}

	lc, err := Resolve("claude", "", nil, nil, agents)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyIsolatedHome("claude", &lc); err != nil {
		t.Fatal(err)
	}
	wantClaude := filepath.Join(makoHome, "home", "claude")
	if lc.Env["CLAUDE_CONFIG_DIR"] != wantClaude {
		t.Fatalf("claude CLAUDE_CONFIG_DIR = %q, want %q", lc.Env["CLAUDE_CONFIG_DIR"], wantClaude)
	}

	lx, err := Resolve("codex", "", nil, nil, agents)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyIsolatedHome("codex", &lx); err != nil {
		t.Fatal(err)
	}
	wantCodex := filepath.Join(makoHome, "home", "codex")
	if lx.Env["CODEX_HOME"] != wantCodex {
		t.Fatalf("codex CODEX_HOME = %q, want %q", lx.Env["CODEX_HOME"], wantCodex)
	}

	if runtime.GOOS != "windows" {
		la, err := Resolve("agy", "", nil, nil, agents)
		if err != nil {
			t.Fatal(err)
		}
		if err := ApplyIsolatedHome("agy", &la); err != nil {
			t.Fatal(err)
		}
		wantAgy := filepath.Join(makoHome, "home", "agy")
		if la.Env["HOME"] != wantAgy {
			t.Fatalf("agy HOME = %q, want %q", la.Env["HOME"], wantAgy)
		}
		// .gitconfig and .ssh should be symlinked; .gemini must be a real isolated directory.
		gitLink, err := os.Readlink(filepath.Join(wantAgy, ".gitconfig"))
		if err != nil || gitLink != filepath.Join(realHome, ".gitconfig") {
			t.Fatalf("expected symlink for .gitconfig, got %q (err=%v)", gitLink, err)
		}
		geminiInfo, err := os.Lstat(filepath.Join(wantAgy, ".gemini"))
		if err != nil || geminiInfo.Mode()&os.ModeSymlink != 0 || !geminiInfo.IsDir() {
			t.Fatalf("expected isolated .gemini dir, got mode=%v err=%v", geminiInfo, err)
		}
		// Dangling symlink cleanup check: remove .gitconfig from realHome and re-ensure.
		_ = os.Remove(filepath.Join(realHome, ".gitconfig"))
		if err := EnsureAgyIsolatedHome(wantAgy); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(filepath.Join(wantAgy, ".gitconfig")); !os.IsNotExist(err) {
			t.Fatalf("expected dangling symlink to be cleaned up, err=%v", err)
		}
	}

	// Explicit env override takes precedence over IsolateHome.
	agents["claude"] = Agent{Env: map[string]string{"CLAUDE_CONFIG_DIR": "/custom/claude"}}
	override, err := Resolve("claude", "", nil, nil, agents)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyIsolatedHome("claude", &override); err != nil {
		t.Fatal(err)
	}
	if override.Env["CLAUDE_CONFIG_DIR"] != "/custom/claude" {
		t.Fatalf("explicit env was overwritten: %q", override.Env["CLAUDE_CONFIG_DIR"])
	}
}
