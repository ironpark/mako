package homefs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture is a sandbox home with a folder and a file outside it that the
// agent links to.
func fixture(t *testing.T) (home, outside string) {
	t.Helper()
	mako, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAKO_HOME", mako)
	home = filepath.Join(mako, "sandbox", "claude")
	outside = filepath.Join(mako, "outside")
	for _, d := range []string{home, outside} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.json"), []byte(`{"token":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return home, outside
}

func TestSandboxHomeLinksKeepTheDaemonInside(t *testing.T) {
	home, outside := fixture(t)
	// A folder of the home linked out of it.
	if err := os.Symlink(outside, filepath.Join(home, "skills")); err != nil {
		t.Fatal(err)
	}
	if err := MkdirAll(filepath.Join(home, "skills", "mako"), 0o700); err == nil {
		t.Error("MkdirAll followed a link out of the home")
	}
	if err := Write(filepath.Join(home, "skills", "SKILL.md"), []byte("x")); err == nil {
		t.Error("Write followed a link out of the home")
	}
	if err := Remove(filepath.Join(home, "skills", "secret.json")); err == nil {
		t.Error("Remove followed a link out of the home")
	}
	if _, err := ReadDir(filepath.Join(home, "skills")); err == nil {
		t.Error("ReadDir followed a link out of the home")
	}
	for _, name := range []string{"SKILL.md", "mako"} {
		if _, err := os.Stat(filepath.Join(outside, name)); err == nil {
			t.Fatalf("%s landed outside the home", name)
		}
	}
	// The file itself linked out of it: not read, and replaced, not
	// written through.
	state := filepath.Join(home, ".claude.json")
	if err := os.Symlink(filepath.Join(outside, "secret.json"), state); err != nil {
		t.Fatal(err)
	}
	if b, err := ReadFile(state); err == nil {
		t.Fatalf("ReadFile read %q through a link out of the home", b)
	}
	if f, err := Open(state); err == nil {
		f.Close()
		t.Fatal("Open followed a link out of the home")
	}
	if err := Write(state, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "secret.json")); string(b) != `{"token":"x"}` {
		t.Fatalf("the file outside the home is now %q", b)
	}
	if info, err := os.Lstat(state); err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("Write left %v, %v", info, err)
	}
	// What stays in the home works, links inside it too.
	if err := MkdirAll(filepath.Join(home, "a", "b"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a", filepath.Join(home, "inner")); err != nil {
		t.Fatal(err)
	}
	if err := Write(filepath.Join(home, "inner", "b", "f"), []byte("ok")); err != nil {
		t.Fatal(err)
	}
	if b, err := ReadFile(filepath.Join(home, "a", "b", "f")); err != nil || string(b) != "ok" {
		t.Fatalf("ReadFile = %q, %v", b, err)
	}
	if err := Symlink(outside, filepath.Join(home, "link")); err != nil {
		t.Fatal(err)
	}
	if target, err := Readlink(filepath.Join(home, "link")); err != nil || target != outside {
		t.Fatalf("Readlink = %q, %v", target, err)
	}
	if info, err := Lstat(filepath.Join(home, "link")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("Lstat = %v, %v", info, err)
	}
	entries, err := ReadDir(home)
	if err != nil || len(entries) == 0 {
		t.Fatalf("ReadDir = %v, %v", entries, err)
	}
}

func TestASandboxHomeThatIsALinkIsRefused(t *testing.T) {
	home, outside := fixture(t)
	if err := os.Remove(home); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, home); err != nil {
		t.Fatal(err)
	}
	if err := Write(filepath.Join(home, "x"), []byte("x")); !errors.Is(err, ErrLink) {
		t.Fatalf("Write = %v, want ErrLink", err)
	}
	if err := MkdirAll(home, 0o700); !errors.Is(err, ErrLink) {
		t.Fatalf("MkdirAll = %v, want ErrLink", err)
	}
}

func TestAMissingSandboxHomeIsMade(t *testing.T) {
	home, _ := fixture(t)
	codex := filepath.Join(filepath.Dir(home), "codex")
	if err := MkdirAll(codex, 0o700); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(codex); err != nil || !info.IsDir() {
		t.Fatalf("MkdirAll made %v, %v", info, err)
	}
}

func TestPathsOutsideTheSandboxHomesAreAsTheyAre(t *testing.T) {
	_, outside := fixture(t)
	link := filepath.Join(outside, "link.json")
	if err := os.Symlink(filepath.Join(outside, "secret.json"), link); err != nil {
		t.Fatal(err)
	}
	if b, err := ReadFile(link); err != nil || !strings.Contains(string(b), "token") {
		t.Fatalf("ReadFile = %q, %v", b, err)
	}
	if !Within(filepath.Join(outside, "x"), outside) || Within(outside, filepath.Join(outside, "x")) {
		t.Fatal("Within")
	}
}

func TestSandboxHomePathsInAnotherCase(t *testing.T) {
	prev := caseFolds
	caseFolds = true
	t.Cleanup(func() { caseFolds = prev })
	if rel, ok := relIn("/Users/me/.mako/sandbox", "/users/ME/.mako/Sandbox/claude/x"); !ok || rel != "claude/x" {
		t.Fatalf("relIn = %q, %v", rel, ok)
	}
	if _, ok := relIn("/Users/me/.mako/sandbox", "/Users/me/.mako/sandboxes/x"); ok {
		t.Fatal("relIn took a sibling")
	}
}
