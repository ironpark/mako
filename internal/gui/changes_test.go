package gui

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

func TestGitTreeAddsUpFolders(t *testing.T) {
	tree := gitTree([]*protocol.GitFile{
		{Path: "internal/gui/agents.go", Added: 35, Removed: 34, Status: protocol.GitModified},
		{Path: "internal/gui/icons/copy.svg", Added: 16, Status: protocol.GitAdded},
		{Path: "internal/client/client.go", Added: 24, Status: protocol.GitModified},
		{Path: "README.md", Added: 1, Removed: 1, Status: protocol.GitModified},
	})
	if len(tree) != 2 || tree[0].Name != "internal" || tree[1].Name != "README.md" {
		t.Fatalf("top level %+v", tree)
	}
	internal := tree[0]
	if internal.Added != 75 || internal.Removed != 34 {
		t.Fatalf("internal adds up to +%d -%d", internal.Added, internal.Removed)
	}
	gui := internal.Children[1]
	if gui.Name != "gui" || gui.Children[0].Name != "icons" || gui.Children[1].Name != "agents.go" {
		t.Fatalf("gui %+v: want folders first", gui)
	}
	// A folder holding one folder alone shares its row.
	deep := gitTree([]*protocol.GitFile{
		{Path: "app/android/app/src/gen/java/dev/v2/A.java", Added: 2},
		{Path: "app/android/app/src/gen/java/dev/v2/B.java", Added: 3},
		{Path: "app/README.md", Added: 1},
	})
	if len(deep) != 1 || deep[0].Name != "app" || len(deep[0].Children) != 2 {
		t.Fatalf("app %+v", deep)
	}
	gen := deep[0].Children[0]
	if gen.Name != "android/app/src/gen/java/dev/v2" || gen.Key != "app/android/app/src/gen/java/dev/v2" || gen.Added != 5 || len(gen.Children) != 2 {
		t.Fatalf("the chain of folders: %+v", gen)
	}
	if lineCount(2712) != "2.7k" || lineCount(2000) != "2k" || lineCount(320) != "320" {
		t.Fatal("counts are not written as the tree shows them")
	}
}

// The Changes tab reads the project's work tree through its daemon and
// shows the diff of a file chosen.
func TestChangesTabShowsTheWorkTree(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	project := t.TempDir()
	r, err := git.PlainInit(project, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wt, _ := r.Worktree()
	_, _ = wt.Add("main.go")
	if _, err := wt.Commit("init", &git.CommitOptions{Author: &object.Signature{Name: "t", Email: "t@t", When: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.P2P.Listen = "127.0.0.1:0"
	cfg.Discovery = config.Discovery{}
	if err := savePrefs(prefs{DaemonMode: "embedded", Language: "en"}.normalized()); err != nil {
		t.Fatal(err)
	}
	m := newModel(cfg, "")
	m.DetailTab.Set(1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := ggui.ProbeBuilder(func() ggui.Widget { return build(m) }, ggui.Sz(1200, 800))
	p.Setup(func() {
		setup(m)
		m.start(ctx, p.Post)
	})
	defer p.Close()
	defer m.shutdown()

	probeUntil(t, p, "the app's daemon", func() bool { return ggui.Peek(m.Conn).OK })
	m.launch("shell", "", project)
	probeUntil(t, p, "the changes", func() bool { _, ok := p.Find("main.go"); return ok })
	if f := ggui.Peek(m.Git).Changes.GetFiles(); len(f) != 1 || f[0].GetAdded() != 2 || f[0].GetRemoved() != 0 {
		t.Fatalf("changes %+v", f)
	}
	row, _ := p.Find("main.go")
	p.Click(row.Center())
	// The diff opens in a tab of its own.
	probeUntil(t, p, "the diff", func() bool {
		d := ggui.Peek(m.Diffs)[ggui.Peek(m.Selected)]
		return !d.Loading && len(d.Diff.GetLines()) > 0
	})
	if l := ggui.Peek(m.Diffs)[ggui.Peek(m.Selected)].Diff.GetLines()[0]; l.GetOp() != "@" {
		t.Fatalf("diff starts %+v, want its hunk's header", l)
	}
}
