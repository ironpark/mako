package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

// A program the PATH leaves out is found where installers put it, the
// agent's own folders first.
func TestFindLooksPastThePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("files run by extension there")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("NVM_DIR", "")
	write := func(rel string) string {
		p := filepath.Join(home, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755)
		return p
	}
	if _, err := Find("agent-x"); err == nil {
		t.Fatal("found what is nowhere")
	}
	shared := write(filepath.Join(".local", "bin", "agent-x"))
	if got, err := Find("agent-x"); err != nil || got != shared {
		t.Fatalf("got %q %v", got, err)
	}
	own := write(filepath.Join(".agent-x", "bin", "agent-x"))
	if got, _ := Find("agent-x", filepath.Join(".agent-x", "bin")); got != own {
		t.Fatalf("own folder not first: %q", got)
	}
	// Not one it may run.
	_ = os.Chmod(own, 0o644)
	_ = os.Chmod(shared, 0o644)
	if _, err := Find("agent-x", filepath.Join(".agent-x", "bin")); err == nil {
		t.Fatal("found a file it may not run")
	}
}

// Each OS has its installers' folders, and nvm's newest Node.js comes
// first of its versions.
func TestInstallDirsByOS(t *testing.T) {
	home := t.TempDir()
	for _, v := range []string{"v9.11.2", "v22.1.0", "v18.20.4"} {
		_ = os.MkdirAll(filepath.Join(home, ".nvm", "versions", "node", v, "bin"), 0o755)
	}
	env := func(key string) string {
		return map[string]string{"APPDATA": `C:\U\AppData\Roaming`, "LOCALAPPDATA": `C:\U\AppData\Local`}[key]
	}
	mac := installDirs("darwin", home, env, []string{".own"})
	if mac[0] != filepath.Join(home, ".own") || !slices.Contains(mac, "/opt/homebrew/bin") {
		t.Errorf("darwin %q", mac)
	}
	nvm := slices.IndexFunc(mac, func(d string) bool { return filepath.Base(filepath.Dir(d)) == "v22.1.0" })
	old := slices.IndexFunc(mac, func(d string) bool { return filepath.Base(filepath.Dir(d)) == "v9.11.2" })
	if nvm < 0 || old < nvm {
		t.Errorf("nvm order %q", mac)
	}
	if linux := installDirs("linux", home, env, nil); slices.Contains(linux, "/opt/homebrew/bin") || !slices.Contains(linux, "/home/linuxbrew/.linuxbrew/bin") {
		t.Errorf("linux %q", linux)
	}
	if win := installDirs("windows", home, env, nil); !slices.Contains(win, filepath.Join(`C:\U\AppData\Roaming`, "npm")) || slices.Contains(win, "/usr/local/bin") {
		t.Errorf("windows %q", win)
	}
}

// On Windows a program is looked for by each extension that runs it, and
// its name is without it.
func TestProgramNames(t *testing.T) {
	got := programFiles("windows", "claude", func(string) string { return ".EXE;.CMD" })
	if !slices.Equal(got, []string{"claude.exe", "claude.cmd"}) {
		t.Errorf("files %q", got)
	}
	if got := programFiles("linux", "claude", func(string) string { return "" }); !slices.Equal(got, []string{"claude"}) {
		t.Errorf("files %q", got)
	}
	for in, want := range map[string]string{"/usr/bin/codex": "codex", "claude.cmd": "claude", "agy.EXE": "agy", "my.tool": "my.tool"} {
		if got := ProgramName(in); got != want {
			t.Errorf("%s: %q", in, got)
		}
	}
}
