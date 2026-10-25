package gui

import (
	"bytes"
	"context"
	"image"
	pngenc "image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/ironpark/ggui"

	"github.com/ironpark/sanbo/internal/config"
)

// The Files tab lists the session's project through its daemon, reads a
// folder as it opens, opens a text file chosen in a tab and previews an
// image.
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
	logo := image.NewRGBA(image.Rect(0, 0, 3, 2))
	var png bytes.Buffer
	if err := pngenc.Encode(&png, logo); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "logo.png"), png.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Listen = "127.0.0.1:0"
	cfg.Discovery = config.Discovery{}
	if err := savePrefs(prefs{DaemonMode: "embedded", ShowComposer: true}.normalized()); err != nil {
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
	session := ggui.Peek(m.Selected)

	found := func(label string) func() bool {
		return func() bool { _, ok := p.Find(label); return ok }
	}
	probeUntil(t, p, "the project's files", found("README.md"))
	src, _ := p.Find("src")
	p.Click(src.Center())
	probeUntil(t, p, "src to open", found("main.go"))
	f, _ := p.Find("main.go")
	p.Click(f.Center())
	// A text file opens in a tab, whole, to edit and save.
	path := filepath.Join(project, "src", "main.go")
	id := docTabID(ggui.Peek(m.Conn).Self.ID, path)
	probeUntil(t, p, "the file's tab", func() bool { d, ok := m.doc(id); return ok && !d.Loading })
	if ggui.Peek(m.Selected) != id || ggui.Peek(m.docs[id].text) != "package main\n" {
		t.Fatalf("tab %q shows %q", ggui.Peek(m.Selected), ggui.Peek(m.docs[id].text))
	}
	p.Frame()
	p.Text("x")
	p.Frame()
	if d, _ := m.doc(id); !d.Dirty || ggui.Peek(m.docs[id].text) != "xpackage main\n" {
		t.Fatalf("after typing: dirty %v, %q", d.Dirty, ggui.Peek(m.docs[id].text))
	}
	m.saveSelected()
	probeUntil(t, p, "the save", func() bool { d, _ := m.doc(id); return !d.Saving && !d.Dirty })
	if got, _ := os.ReadFile(path); string(got) != "xpackage main\n" {
		t.Fatalf("saved %q", got)
	}
	// The Files tab still shows the project the file is in.
	if _, ok := p.Find("README.md"); !ok {
		t.Fatal("the Files tab left the project for the file's tab")
	}

	m.insertPath(path)
	if got := ggui.Peek(m.Composer); got != filepath.Join("src", "main.go") {
		t.Fatalf("composer %q, want the path from the project", got)
	}

	// Dragged onto the message box, a file is inserted as its path.
	m.showTab(session)
	p.Frame()
	m.Composer.Set("")
	f, _ = p.FindRole(ggui.RoleTreeItem, "main.go")
	box, ok := p.Find(T("Message"))
	if !ok {
		t.Fatal("no message box")
	}
	p.Press(f.Center())
	p.Move(f.Center().Add(ggui.Pt(-12, 6)))
	p.Move(box.Center())
	p.Move(box.Center())
	p.Release(box.Center())
	p.Frame()
	if got := ggui.Peek(m.Composer); got != filepath.Join("src", "main.go") {
		t.Fatalf("composer %q after the drag, want the file's path", got)
	}

	// An image is shown, and enlarged.
	logoRow, _ := p.FindRole(ggui.RoleTreeItem, "logo.png")
	p.Click(logoRow.Center())
	probeUntil(t, p, "the image", func() bool { return ggui.Peek(m.Preview).Img != nil })
	if b := ggui.Peek(m.Preview).Img.Bounds(); b.Dx() != 3 || b.Dy() != 2 {
		t.Fatalf("image of %v, want 3×2", b)
	}
	p.Tap(T("Enlarge"))
	if !ggui.Peek(m.ImageOpen) {
		t.Fatal("the image did not open enlarged")
	}
	m.ImageOpen.Set(false)

	// A folder dropped from the desktop where nothing takes it opens a new
	// session in it.
	p.OnDrop(m.dropFolder)
	p.DropPaths(ggui.Pt(120, 500), filepath.Join(project, "src"))
	p.Frame()
	if !ggui.Peek(m.NewOpen) || ggui.Peek(m.Form).Project != filepath.Join(project, "src") {
		t.Fatalf("dialog open %v on %q", ggui.Peek(m.NewOpen), ggui.Peek(m.Form).Project)
	}
}

func TestShellPathsQuote(t *testing.T) {
	got := shellPaths([]string{"/tmp/a.txt", "/Users/me/My Files", "it's", "/한글/파일"})
	want := `/tmp/a.txt '/Users/me/My Files' 'it'\''s' /한글/파일 `
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
