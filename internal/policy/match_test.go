package policy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

func rule(id string, action Action, tool, pattern string) Rule {
	return Rule{ID: id, Enabled: true, Action: action, Tool: tool, Pattern: pattern}
}

// expect checks that rules decide call as want, "" for not at all.
func expect(t *testing.T, rules []Rule, call Call, want Action) {
	t.Helper()
	d := EvaluateRules(rules, call)
	if got := Action(""); d.Matched {
		got = d.Action
		if got != want {
			t.Errorf("%s %v: %s by %s %q, want %q", call.Tool, call.Input, got, d.Rule.Tool, d.Rule.Pattern, want)
		}
	} else if want != "" {
		t.Errorf("%s %v: not decided, want %s by %s %q", call.Tool, call.Input, want, rules[0].Tool, rules[0].Pattern)
	}
}

// A search is read by its path, the project without one, and what it
// picks there: a search of the whole project is not one of src, and a
// glob for .env files is read by a rule for .env.
func TestSearchesAreReadByWhatTheyPick(t *testing.T) {
	src := []Rule{rule("src", ActionAllow, "Read", "src/*")}
	env := []Rule{rule("env", ActionDeny, "Read", "*.env*")}
	grep := func(in map[string]any) Call {
		return Call{Agent: "claude", Tool: "Grep", Kind: protocol.ToolSearch, Input: in}
	}
	glob := func(in map[string]any) Call {
		return Call{Agent: "claude", Tool: "Glob", Kind: protocol.ToolSearch, Input: in}
	}
	expect(t, src, grep(map[string]any{"pattern": "src/foo"}), "")
	expect(t, src, grep(map[string]any{}), "")
	expect(t, src, grep(map[string]any{"pattern": "TODO", "path": "src/x"}), ActionAllow)
	expect(t, src, grep(map[string]any{"pattern": "TODO", "path": "src/x", "glob": "../../*"}), "")
	expect(t, src, glob(map[string]any{"pattern": "src/**/../../etc/*"}), "")
	expect(t, env, glob(map[string]any{"pattern": "**/.env", "path": "."}), ActionDeny)
	expect(t, env, grep(map[string]any{"pattern": "KEY", "glob": ".env*"}), ActionDeny)
	expect(t, env, grep(map[string]any{"pattern": "KEY", "path": "src"}), "")
}

// Paths are read cleaned, and relative to the session's project when they
// are in it, so ../ and ./ neither get past an allow rule nor dodge a deny
// rule, and an absolute path in the project reads as its relative one.
func TestPathsAreReadCleaned(t *testing.T) {
	dir := t.TempDir()
	edit := func(path string) Call {
		return Call{Agent: "claude", Tool: "Edit", Kind: protocol.ToolEdit, Input: map[string]any{"path": path, "old": "a", "new": "b"}, Dir: dir}
	}
	src := []Rule{rule("src", ActionAllow, "Edit", "src/*")}
	env := []Rule{rule("env", ActionDeny, "Edit", ".env")}
	expect(t, src, edit("src/../.env"), "")
	expect(t, src, edit("src/a/../../../etc/passwd"), "")
	expect(t, src, edit("src/a.go"), ActionAllow)
	expect(t, src, edit("./src/a.go"), ActionAllow)
	expect(t, src, edit(filepath.Join(dir, "src", "a.go")), ActionAllow)
	expect(t, src, edit(filepath.Join(dir, "..", "src", "a.go")), "")
	expect(t, env, edit("./.env"), ActionDeny)
	expect(t, env, edit("src/../.env"), ActionDeny)
	expect(t, env, edit(filepath.Join(dir, ".env")), ActionDeny)

	patch := func(p string) Call {
		return Call{Agent: "codex", Tool: "apply_patch", Kind: protocol.ToolPatch, Input: map[string]any{"patch": p}, Dir: dir}
	}
	expect(t, src, patch("*** Begin Patch\n*** Update File: src/../.env\n@@\n-a\n+b\n*** End Patch"), "")
	expect(t, env, patch("*** Begin Patch\n*** Update File: ./.env\n@@\n-a\n+b\n*** End Patch"), ActionDeny)
}

// A link in the project to a place out of it is read as the place it
// leads to, even when what it leads to is not there yet.
func TestPathsAreReadThroughLinks(t *testing.T) {
	dir, out := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(out, filepath.Join(dir, "src", "link")); err != nil {
		t.Skip("no symlinks here:", err)
	}
	if err := os.Symlink(filepath.Join(out, "secret", "new"), filepath.Join(dir, "src", "dangling")); err != nil {
		t.Fatal(err)
	}
	write := func(path string) Call {
		return Call{Agent: "claude", Tool: "Write", Kind: protocol.ToolWrite, Input: map[string]any{"path": path}, Dir: dir}
	}
	src := []Rule{rule("src", ActionAllow, "Write", "src/*")}
	expect(t, src, write("src/link/authorized_keys"), "")
	expect(t, src, write("src/dangling"), "")
	expect(t, src, write("src/a.go"), ActionAllow)
	secret := []Rule{rule("secret", ActionDeny, "Write", "*/secret/*")}
	expect(t, secret, write("src/dangling"), ActionDeny)
}

// A command run in another directory than the session's project is not
// the one an allow rule vouched for there.
func TestShellCommandsAreReadWithTheirDirectory(t *testing.T) {
	dir := t.TempDir()
	run := func(cwd string) Call {
		return Call{Agent: "codex", Tool: "exec_command", Kind: protocol.ToolShell, Input: map[string]any{"command": "rm -rf build", "cwd": cwd}, Dir: dir}
	}
	allow := []Rule{rule("build", ActionAllow, "Bash", "rm -rf build")}
	expect(t, allow, run(""), ActionAllow)
	expect(t, allow, run(dir), ActionAllow)
	expect(t, allow, run("."), ActionAllow)
	expect(t, allow, run("/"), "")
	expect(t, allow, run("sub"), "")
	expect(t, allow, run(filepath.Join(dir, "..")), "")
	deny := []Rule{rule("root", ActionDeny, "Bash", "/etc*")}
	expect(t, deny, run("/etc"), ActionDeny)
}

// Only a patch's headers name its files: a line of a hunk that reads like
// one is the file's text, in Codex's format and in a unified diff, and a
// unified diff's headers are read as well as Codex's.
func TestPatchesAreReadByTheirHeaders(t *testing.T) {
	env := []Rule{rule("env", ActionDeny, "Edit", "*.env*")}
	src := []Rule{rule("src", ActionAllow, "Edit", "src/*")}
	patch := func(p string) Call {
		return Call{Agent: "opencode", Tool: "apply_patch", Kind: protocol.ToolPatch, Input: map[string]any{"patch": p}}
	}

	// A context line in an updated file's chunk.
	context := patch("*** Begin Patch\n*** Update File: src/a.go\n@@\n *** Update File: .env\n-a\n+b\n*** End Patch")
	expect(t, env, context, "")
	// An allow rule reads it as a header too, should a patch tool do so.
	expect(t, src, context, "")
	expect(t, src, patch("*** Begin Patch\n*** Update File: src/a.go\n@@\n-a\n+b\n*** End Patch"), ActionAllow)
	// After an added file's text, Codex reads a header trimmed.
	expect(t, env, patch("*** Begin Patch\n*** Add File: src/b.go\n+x\n  *** Update File: .env\n@@\n-a\n+b\n*** End Patch"), ActionDeny)

	unified := "diff --git a/src/a.go b/src/a.go\n--- a/src/a.go\n+++ b/src/a.go\n@@ -1,3 +1,3 @@\n--- .env\n+++ .env\n context\n-x\n+y\n"
	expect(t, env, patch(unified), "")
	expect(t, src, patch(unified), "")
	expect(t, src, patch("--- a/src/a.go\n+++ b/src/a.go\n@@ -1 +1 @@\n-x\n+y\n"), ActionAllow)
	expect(t, env, patch("--- a/src/a.go\n+++ b/src/a.go\n@@ -1 +1 @@\n-x\n+y\n--- a/.env\n+++ b/.env\n@@ -1 +1 @@\n-x\n+y\n"), ActionDeny)
	expect(t, env, patch("diff --git a/x b/.env\nsimilarity index 100%\nrename from x\nrename to .env\n"), ActionDeny)
	expect(t, src, patch("--- /dev/null\n+++ b/src/new.go\n@@ -0,0 +1 @@\n+x\n"), ActionAllow)

	// A patch with no header names no file an allow rule could vouch
	// for; a deny rule reads it whole.
	expect(t, src, patch("src/a.go"), "")
	expect(t, env, patch("something about .env"), ActionDeny)
}

// A call of a known kind is read by its kind's fields alone: a string in
// another argument is not what it acts on.
func TestKnownKindsAreReadByTheirFields(t *testing.T) {
	read := Call{Agent: "claude", Tool: "Read", Kind: protocol.ToolRead, Input: map[string]any{"path": "secrets/key", "note": "src/a.go"}}
	expect(t, []Rule{rule("src", ActionAllow, "Read", "src/*")}, read, "")
	expect(t, []Rule{rule("src", ActionAllow, "Read", "src/*")}, Call{Agent: "claude", Tool: "Read", Kind: protocol.ToolRead, Input: map[string]any{"note": "src/a.go"}}, "")
}

// A star in a pattern is a wildcard even where the text holds a star.
func TestGlobStarsMatchStarsInText(t *testing.T) {
	for _, c := range []struct{ pattern, text string }{
		{"*.env*", "**/.env"},
		{"git *", "git log -- '*.go'"},
		{"*", "*"},
		{"a*b", "a*b"},
	} {
		if !matchGlobFold(c.pattern, c.text) {
			t.Errorf("%q did not match %q", c.pattern, c.text)
		}
	}
	if matchGlobFold("*.go", "*.goo") {
		t.Error(`"*.go" matched "*.goo"`)
	}
}
