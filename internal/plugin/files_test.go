package plugin

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/ironpark/mako/internal/protocol"
)

// project makes a project's folder for the files tests, with a file
// outside it that no call may reach: text, binary, a folder, links in and
// out, a FIFO, and a .git folder.
func project(t *testing.T) (root, outside string) {
	t.Helper()
	base := t.TempDir()
	// The folder as the node knows it: its links followed, as a temporary
	// folder's are not on macOS.
	base, _ = filepath.EvalSymlinks(base)
	root = filepath.Join(base, "proj")
	outside = filepath.Join(base, "outside.txt")
	bin := make([]byte, 256)
	for i := range bin {
		bin[i] = byte(i)
	}
	files := map[string][]byte{
		"a.txt":      []byte("hello"),
		"bin.dat":    bin,
		"sub/b.txt":  []byte("bee"),
		".git/HEAD":  []byte("ref: refs/heads/main\n"),
		"secret.txt": []byte("x"),
	}
	for name, data := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(outside, []byte("OUTSIDE"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(base, "elsewhere"), 0o755); err != nil {
		t.Fatal(err)
	}
	links := map[string]string{
		"link-in":  "a.txt",
		"link-out": "../outside.txt",
		"link-abs": outside,
		"link-dir": "../elsewhere",
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	if runtime.GOOS != "windows" {
		if err := syscall.Mkfifo(filepath.Join(root, "fifo"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root, outside
}

func filesRig(t *testing.T, roots ...ProjectRoots) *rig {
	return startRig(t, Config{Limits: testLimits, Projects: func() []ProjectRoots { return roots }})
}

// filePlugin is a plugin of the permissions whose load runs code, and
// shows what it came to or the error.
func filePlugin(r *rig, id string, perms []string, load string) *protocol.UiNode {
	r.t.Helper()
	r.write(id, map[string]string{ManifestName: manifest(id, perms, ""), "main.js": block(load, `ui.text(s.msg)`)})
	r.state(id, protocol.PluginWaiting)
	r.grant(id)
	return r.watch("plugin:"+id+"/ui").until("its load", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool {
		return len(texts(n)) > 0 && texts(n)[0] != ""
	})
}

func TestPluginReadsItsProjectsFiles(t *testing.T) {
	root, _ := project(t)
	r := filesRig(t, ProjectRoots{ID: "p1", Name: "proj", Roots: []string{root}})
	at := `{ project: "p1", root: ` + quote(root) + `, `
	tree := filePlugin(r, "reader", []string{"fs:project"}, `
		const { projects } = await mako.files.listProjects({});
		const a = await mako.files.readFile(`+at+`path: "a.txt" });
		const piece = await mako.files.readFile(`+at+`path: "a.txt", offset: 1, length: 2 });
		const bin = await mako.files.readFile(`+at+`path: "bin.dat" });
		const sub = await mako.files.readFile(`+at+`path: "sub/b.txt" });
		const link = await mako.files.readFile(`+at+`path: "link-in" });
		const dir = await mako.files.listDir(`+at+`path: "" });
		s.msg = [projects[0].id, projects[0].roots.length, a.text, a.size, a.eof, piece.text, piece.eof,
			bin.binary, bin.text === "", bin.data, sub.text, link.text,
			dir.entries.map((e) => e.name + (e.dir ? "/" : "") + (e.link ? "@" : "")).join(" ")].join("|");`)
	bin := make([]byte, 256)
	for i := range bin {
		bin[i] = byte(i)
	}
	entries := ".git/ a.txt bin.dat fifo link-abs@ link-dir@ link-in@ link-out@ secret.txt sub/"
	if runtime.GOOS == "windows" {
		entries = strings.Replace(entries, "fifo ", "", 1)
	}
	want := strings.Join([]string{"p1", "1", "hello", "5", "true", "el", "false", "true", "true", base64.StdEncoding.EncodeToString(bin), "bee", "hello", entries}, "|")
	if got := texts(tree)[0]; got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func quote(s string) string { return `"` + strings.ReplaceAll(s, `\`, `\\`) + `"` }

func TestFileReadsOutOfTheProjectAreRefused(t *testing.T) {
	root, outside := project(t)
	other := t.TempDir()
	r := filesRig(t, ProjectRoots{ID: "p1", Roots: []string{root}}, ProjectRoots{ID: "p2", Roots: []string{other}})
	at := `{ project: "p1", root: ` + quote(root) + `, `
	cases := map[string]struct{ call, want string }{
		"dots":       {`mako.files.readFile(` + at + `path: "../outside.txt" })`, "not a path relative"},
		"unclean":    {`mako.files.readFile(` + at + `path: "sub/../../outside.txt" })`, "not a path relative"},
		"absolute":   {`mako.files.readFile(` + at + `path: ` + quote(outside) + ` })`, "not a path relative"},
		"backslash":  {`mako.files.readFile(` + at + `path: "sub\\b.txt" })`, "not a path relative"},
		"link-out":   {`mako.files.readFile(` + at + `path: "link-out" })`, "link-out"},
		"link-abs":   {`mako.files.readFile(` + at + `path: "link-abs" })`, "link-abs"},
		"link-dir":   {`mako.files.listDir(` + at + `path: "link-dir" })`, "link-dir"},
		"folder":     {`mako.files.readFile(` + at + `path: "sub" })`, "not a regular file"},
		"root-other": {`mako.files.readFile({ project: "p1", root: ` + quote(other) + `, path: "x" })`, "knows no project"},
		"root-up":    {`mako.files.readFile({ project: "p1", root: ` + quote(filepath.Dir(root)) + `, path: "outside.txt" })`, "knows no project"},
		"unknown":    {`mako.files.readFile({ project: "nope", root: ` + quote(root) + `, path: "a.txt" })`, "knows no project"},
		"offset":     {`mako.files.readFile(` + at + `path: "a.txt", offset: -1 })`, "offset"},
		"flood":      {`(async () => { for (let i = 0; i < 130; i++) await mako.files.readFile(` + at + `path: "a.txt" }) })()`, "times a minute"},
	}
	if runtime.GOOS != "windows" {
		// A FIFO is refused at once, rather than holding the daemon
		// waiting for a writer.
		cases["fifo"] = struct{ call, want string }{`mako.files.readFile(` + at + `path: "fifo" })`, "not a regular file"}
	}
	for id, c := range cases {
		tree := filePlugin(r, "r-"+id, []string{"fs:project"}, `await `+c.call+`; s.msg = "got through"`)
		if !hasText(tree, "error:") || !hasText(tree, c.want) || hasText(tree, "OUTSIDE") {
			t.Errorf("%s: %v", id, texts(tree))
		}
	}
	healthy(t, r)
}

func TestPluginWritesItsProjectsFiles(t *testing.T) {
	root, outside := project(t)
	r := filesRig(t, ProjectRoots{ID: "p1", Roots: []string{root}})
	at := `{ project: "p1", root: ` + quote(root) + `, `
	// fs:project alone does not write: refused before the request is read.
	tree := filePlugin(r, "readonly", []string{"fs:project"}, `await mako.files.writeFile(`+at+`path: "a.txt", text: "x" }); s.msg = "got through"`)
	if !hasText(tree, "needs the permission fs:project:write") {
		t.Fatalf("a write without fs:project:write: %v", texts(tree))
	}
	tree = filePlugin(r, "writer", []string{"fs:project", "fs:project:write"}, `
		await mako.files.writeFile(`+at+`path: "a.txt", text: "new text" });
		await mako.files.writeFile(`+at+`path: "made/deep/c.txt", text: "c", makeDirs: true });
		const back = await mako.files.readFile(`+at+`path: "a.txt" });
		s.msg = "wrote " + back.text;`)
	if !hasText(tree, "wrote new text") {
		t.Fatalf("%v", texts(tree))
	}
	if b, _ := os.ReadFile(filepath.Join(root, "made", "deep", "c.txt")); string(b) != "c" {
		t.Fatalf("made/deep/c.txt is %q", b)
	}
	if info, err := os.Stat(filepath.Join(root, "a.txt")); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("a.txt after a write: %v, %v (its mode is kept)", info.Mode(), err)
	}
	cases := map[string]struct{ call, want string }{
		"dots":     {`path: "../outside.txt", text: "pwned" }`, "not a path relative"},
		"git":      {`path: ".git/hooks/pre-commit", text: "#!/bin/sh", makeDirs: true }`, ".git"},
		"git-case": {`path: "sub/.GIT/config", text: "x", makeDirs: true }`, ".git"},
		"link":     {`path: "link-in", text: "pwned" }`, "not a regular file"},
		"link-out": {`path: "link-out", text: "pwned" }`, "not a regular file"},
		"via-dir":  {`path: "link-dir/x.txt", text: "pwned" }`, "link-dir"},
		"mkdir":    {`path: "link-dir/new/x.txt", text: "pwned", makeDirs: true }`, "link-dir"},
		"folder":   {`path: "sub", text: "x" }`, "not a regular file"},
		"no-dir":   {`path: "nowhere/x.txt", text: "x" }`, "nowhere"},
		"big":      {`path: "big.txt", text: "x".repeat((1 << 20) + 1) }`, "over 1048576 bytes"},
		"both":     {`path: "x.txt", text: "x", data: "eA==" }`, "not both"},
	}
	for id, c := range cases {
		tree := filePlugin(r, "w-"+id, []string{"fs:project", "fs:project:write"}, `await mako.files.writeFile(`+at+c.call+`); s.msg = "got through"`)
		if !hasText(tree, "error:") || !hasText(tree, c.want) {
			t.Errorf("%s: %v", id, texts(tree))
		}
	}
	tree = filePlugin(r, "w-flood", []string{"fs:project", "fs:project:write"},
		`for (let i = 0; i < 40; i++) await mako.files.writeFile(`+at+`path: "flood.txt", text: String(i) }); s.msg = "got through"`)
	if !hasText(tree, "times a minute") {
		t.Errorf("flood: %v", texts(tree))
	}
	if b, _ := os.ReadFile(outside); string(b) != "OUTSIDE" {
		t.Fatalf("the file outside became %q", b)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "elsewhere", "x.txt")); err == nil {
		t.Fatal("a write went through a link out of the project")
	}
	// Nothing half written is left behind.
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && strings.HasPrefix(d.Name(), ".mako-plugin-") {
			t.Errorf("a write left %s", p)
		}
		return nil
	})
	healthy(t, r)
}
