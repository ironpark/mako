package gui

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
)

func TestParentPath(t *testing.T) {
	for p, want := range map[string]string{
		"/w/api/main.go": "/w/api", "/w/api/": "/w", "/main.go": "/", `C:\w\api.go`: `C:\w`, "x": "",
	} {
		if got := parentPath(p); got != want {
			t.Errorf("parentPath(%q) = %q, want %q", p, got, want)
		}
	}
}

// A change a node tells of reads again the folders listed it changed in,
// and no others.
func TestAChangeRereadsItsFolder(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	m.post = func(func()) {} // no daemon to answer
	m.files.Dirs.Set(map[string]dirListing{
		dirKey("n", "/w"): {}, dirKey("n", "/w/src"): {}, dirKey("n", "/w/docs"): {}, dirKey("other", "/w/src"): {},
	})
	m.filesChanged("n", &protocol.FilesChanged{Paths: []string{"/w/src/new.go"}})
	loading := map[string]bool{}
	for key, l := range ggui.Peek(m.files.Dirs) {
		loading[key] = l.Loading
	}
	if !loading[dirKey("n", "/w/src")] || loading[dirKey("n", "/w")] || loading[dirKey("n", "/w/docs")] || loading[dirKey("other", "/w/src")] {
		t.Fatalf("read again: %v", loading)
	}
}

// A folder listed shows a file an agent makes in it, as its node tells,
// without a refresh.
func TestAListedFolderFollowsItsNode(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	project := t.TempDir()
	cfg := config.Default()
	cfg.P2P.Listen = "127.0.0.1:0"
	cfg.Discovery = config.Discovery{}
	if err := savePrefs(prefs{DaemonMode: "embedded", Language: "en"}.normalized()); err != nil {
		t.Fatal(err)
	}
	m := newModel(cfg, "")
	ctx, cancel := context.WithCancel(t.Context())
	p := ggui.ProbeBuilder(func() ggui.Widget { return build(m) }, ggui.Sz(800, 600))
	p.Setup(func() { setup(m); m.start(ctx, p.Post) })
	t.Cleanup(func() { cancel(); m.shutdown(); p.Close() })
	probeUntil(t, p, "daemon", func() bool { return ggui.Peek(m.Conn).OK })
	node := ggui.Peek(m.Conn).Self.GetId()
	m.loadDir(node, project, false)
	key := dirKey(node, project)
	probeUntil(t, p, "the folder listed and followed", func() bool {
		l, ok := ggui.Peek(m.files.Dirs)[key]
		return ok && !l.Loading && m.fileWatched(node)
	})
	if err := os.WriteFile(filepath.Join(project, "made.txt"), []byte("by an agent"), 0o644); err != nil {
		t.Fatal(err)
	}
	probeUntil(t, p, "the new file listed", func() bool {
		return slices.ContainsFunc(ggui.Peek(m.files.Dirs)[key].Entries, func(e *protocol.DirEntry) bool { return e.GetName() == "made.txt" })
	})
}

// A work tree its node cannot follow is polled again; asking the node
// anew tries it again.
func TestAnUnwatchedTreeIsPolled(t *testing.T) {
	t.Setenv("MAKO_HOME", t.TempDir())
	m := newModel(config.Default(), "")
	w := &nodeWatch{cancel: func() {}, more: make(chan *protocol.WatchFiles, 1)}
	w.want.Store(&protocol.WatchFiles{Trees: []string{"/w"}})
	w.live.Store(true)
	m.files.watches["n"] = w
	root := fileRoot{Node: "n", Path: "/w"}
	if !m.treeWatched(root) {
		t.Fatal("a tree followed is polled")
	}
	m.filesChanged("n", &protocol.FilesChanged{Unwatched: []string{"/w"}})
	if m.treeWatched(root) {
		t.Fatal("a tree its node cannot follow is not polled")
	}
	m.watchFiles(map[string]*protocol.WatchFiles{"n": {Trees: []string{"/w", "/v"}}})
	if !m.treeWatched(root) {
		t.Fatal("asked anew, the tree is not tried again")
	}
}
