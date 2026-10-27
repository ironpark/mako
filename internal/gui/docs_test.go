package gui

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ironpark/ggui"

	"github.com/ironpark/mako/internal/config"
)

// docApp is a window on the app's own daemon, with a file open in a tab.
func docApp(t *testing.T, body string) (*model, *ggui.Probe, string, string) {
	t.Helper()
	return docAppNamed(t, "notes.txt", body)
}

// docAppNamed is docApp with a file of that name.
func docAppNamed(t *testing.T, name, body string) (*model, *ggui.Probe, string, string) {
	t.Helper()
	t.Setenv("MAKO_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Listen = "127.0.0.1:0"
	cfg.Discovery = config.Discovery{}
	if err := savePrefs(prefs{DaemonMode: "embedded", Language: "en"}.normalized()); err != nil {
		t.Fatal(err)
	}
	m := newModel(cfg, "")
	ctx, cancel := context.WithCancel(context.Background())
	p := ggui.ProbeBuilder(func() ggui.Widget { return build(m) }, ggui.Sz(1200, 800))
	p.Setup(func() {
		setup(m)
		m.start(ctx, p.Post)
	})
	t.Cleanup(func() {
		cancel()
		m.shutdown()
		p.Close()
	})
	probeUntil(t, p, "the app's daemon", func() bool { return ggui.Peek(m.Conn).OK })
	self := ggui.Peek(m.Conn).Self.GetId()
	m.openFile(fileItem{Key: dirKey(self, path), Node: self, Path: path, Name: name})
	id := docTabID(self, path)
	probeUntil(t, p, "the file", func() bool { d, ok := m.doc(id); return ok && !d.Loading })
	return m, p, id, path
}

func TestSaveRefusesAFileChangedOnDisk(t *testing.T) {
	m, p, id, path := docApp(t, "one\n")
	// Changed by something else, a second later as file systems count.
	if err := os.WriteFile(path, []byte("theirs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	m.docs[id].text.Set("mine\n")
	m.docEdited(id, "mine\n")
	m.saveDoc(id, false)
	probeUntil(t, p, "the save to fail", func() bool { d, _ := m.doc(id); return !d.Saving })
	if d, _ := m.doc(id); !d.Changed || !d.Dirty {
		t.Fatalf("after a stale save: %+v", d)
	}
	if got, _ := os.ReadFile(path); string(got) != "theirs\n" {
		t.Fatalf("the stale save wrote %q", got)
	}
	if _, ok := p.Find(T("Overwrite")); !ok {
		t.Fatal("no Overwrite in the banner")
	}
	p.Tap(T("Overwrite"))
	probeUntil(t, p, "the overwrite", func() bool { d, _ := m.doc(id); return !d.Saving && !d.Dirty })
	if got, _ := os.ReadFile(path); string(got) != "mine\n" {
		t.Fatalf("overwrote with %q", got)
	}
}

func TestClosingAnEditedFileAsksFirst(t *testing.T) {
	m, p, id, _ := docApp(t, "one\n")
	m.docs[id].text.Set("two\n")
	m.docEdited(id, "two\n")
	p.Frame()
	m.askKill(id) // ⌘W
	p.Frame()
	if !ggui.Peek(m.DiscardOpen) || len(ggui.Peek(m.Tabs)) != 1 {
		t.Fatalf("closed an edited file unasked: dialog %v, tabs %v", ggui.Peek(m.DiscardOpen), ggui.Peek(m.Tabs))
	}
	m.confirmedDiscard()
	p.Frame()
	if _, ok := m.doc(id); ok || len(ggui.Peek(m.Tabs)) != 0 || m.docs[id] != nil {
		t.Fatalf("still open: tabs %v", ggui.Peek(m.Tabs))
	}

	// One saved closes at once.
	m2, p2, id2, _ := docApp(t, "x")
	m2.askKill(id2)
	p2.Frame()
	if ggui.Peek(m2.DiscardOpen) || len(ggui.Peek(m2.Tabs)) != 0 {
		t.Fatal("a saved file asked before closing")
	}
}
