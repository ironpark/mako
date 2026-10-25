package gui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/sanbo/internal/config"
)

// The Files tab lists the session's project through its daemon, reads a
// folder as it opens and previews a file chosen.
func TestFilesTabBrowsesTheProject(t *testing.T) {
	t.Setenv("SANBO_HOME", t.TempDir())
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, "src"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"README.md": "# hello", "src/main.go": "package main\n"} {
		if err := os.WriteFile(filepath.Join(project, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Default()
	cfg.Listen = "127.0.0.1:0"
	cfg.Discovery = config.Discovery{}
	if err := savePrefs(prefs{DaemonMode: "embedded"}.normalized()); err != nil {
		t.Fatal(err)
	}
	m := newModel(cfg, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := ggui.ProbeBuilder(func() ggui.Widget { return build(m) }, ggui.Sz(1200, 800))
	p.Setup(func() {
		setup(m)
		m.start(ctx, p.Post)
	})
	defer p.Close()
	defer m.shutdown()

	probeUntil(t, p, "the window to reach the app's daemon", func() bool { return ggui.Peek(m.Conn).OK })
	m.launch("shell", "", project)
	probeUntil(t, p, "the session", func() bool { return ggui.Peek(m.Selected) != "" })

	found := func(label string) func() bool {
		return func() bool { _, ok := p.Find(label); return ok }
	}
	probeUntil(t, p, "the project's files", found("README.md"))
	src, _ := p.Find("src")
	p.Click(src.Center())
	probeUntil(t, p, "src to open", found("main.go"))
	f, _ := p.Find("main.go")
	p.Click(f.Center())
	probeUntil(t, p, "the preview", func() bool {
		pv := ggui.Peek(m.Preview)
		return !pv.Loading && strings.HasPrefix(string(pv.File.Data), "package main")
	})

	m.insertPath(ggui.Peek(m.Preview).Path)
	if got := ggui.Peek(m.Composer); got != filepath.Join("src", "main.go") {
		t.Fatalf("composer %q, want the path from the project", got)
	}
}
