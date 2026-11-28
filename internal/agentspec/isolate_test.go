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

func TestResolveSandboxAndItsHome(t *testing.T) {
	realHome := t.TempDir()
	makoHome := t.TempDir()
	t.Setenv("HOME", realHome)
	t.Setenv("MAKO_HOME", makoHome)
	binDir := t.TempDir()
	for _, bin := range []string{"claude", "codex"} {
		if err := os.WriteFile(filepath.Join(binDir, bin), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	agents := map[string]Agent{
		"claude": {Sandbox: SandboxWorkspace, Env: map[string]string{"CLAUDE_CONFIG_DIR": "~/.claude-work"}, Profiles: []Profile{
			{Name: "inherits"},
			{Name: "open", Sandbox: SandboxOff},
			{Name: "typo", Sandbox: "worksapce"},
		}},
		"codex": {Profiles: []Profile{{Name: "boxed", Sandbox: SandboxWorkspace}}},
	}
	for _, c := range []struct{ agent, profile, want string }{
		{"claude", "", SandboxWorkspace},
		{"claude", "inherits", SandboxWorkspace},
		{"claude", "open", ""},
		{"codex", "", ""},
		{"codex", "boxed", SandboxWorkspace},
	} {
		l, err := Resolve(c.agent, c.profile, nil, nil, agents)
		if err != nil {
			t.Fatal(err)
		}
		if l.Sandbox != c.want {
			t.Errorf("%s: sandbox = %q, want %q", Target(c.agent, c.profile), l.Sandbox, c.want)
		}
	}
	if _, err := Resolve("claude", "typo", nil, nil, agents); err == nil {
		t.Fatal("an unknown sandbox resolved, which would run the session without one")
	}

	// A sandboxed session's agent keeps its settings in a home of its own,
	// even where the profile names another.
	l, err := Resolve("claude", "", nil, nil, agents)
	if err != nil {
		t.Fatal(err)
	}
	home, err := ApplySandboxHome("claude", &l)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(makoHome, "sandbox", "claude"); home != want || l.Env["CLAUDE_CONFIG_DIR"] != want {
		t.Fatalf("sandbox home = %q, CLAUDE_CONFIG_DIR = %q, want %q", home, l.Env["CLAUDE_CONFIG_DIR"], want)
	}
	lx, err := Resolve("codex", "boxed", nil, nil, agents)
	if err != nil {
		t.Fatal(err)
	}
	if home, err := ApplySandboxHome("codex", &lx); err != nil || lx.Env["CODEX_HOME"] != home || home != filepath.Join(makoHome, "sandbox", "codex") {
		t.Fatalf("codex sandbox home = %q, CODEX_HOME = %q, err %v", home, lx.Env["CODEX_HOME"], err)
	}
	if home, err := ApplySandboxHome("custom-agent", &lx); err != nil || home != "" {
		t.Fatalf("an agent Mako cannot move got sandbox home %q, %v", home, err)
	}
}
