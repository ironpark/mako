package daemon

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ironpark/sanbo/internal/agentspec"
	"github.com/ironpark/sanbo/internal/config"
	"github.com/ironpark/sanbo/internal/protocol"
	"github.com/ironpark/sanbo/internal/session"
)

func TestSavedSessionsResumeOnlyOnAttach(t *testing.T) {
	dir, project := t.TempDir(), t.TempDir()
	cfg := &config.Config{NodeID: "node", Agents: map[string]agentspec.Agent{
		"test": {Command: []string{"/bin/sh"}, Profiles: []agentspec.Profile{{Name: "work"}}},
	}}
	newDaemon := func() *Daemon {
		d := New(cfg, nil)
		d.mcp = nil
		if err := d.loadSessions(dir); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(d.sessions.closeAll)
		return d
	}
	first := newDaemon()
	req := protocol.OpenSession{Agent: "test", Profile: "work", Project: project, Args: []string{"-c", "echo started >> starts; exec cat"}, Env: map[string]string{"EXAMPLE": "value"}}
	s, err := first.openSession(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.renameSession(s.ID(), "API 작업"); err != nil {
		t.Fatal(err)
	}
	first.sessions.closeAll()

	restored := newDaemon()
	list := restored.listSessions()
	if len(list) != 1 || list[0].ID != s.ID() || list[0].Name != "API 작업" || list[0].Project != project || list[0].Profile != "work" || list[0].State != protocol.StateStopped {
		t.Fatalf("restored: %+v", list)
	}
	if len(restored.sessions.list()) != 0 {
		t.Fatal("restoration started a process")
	}
	if restored.saved[s.ID()].Request.Env["EXAMPLE"] != "value" {
		t.Fatal("lost launch inputs")
	}
	// Two clients viewing the same saved session share a single process.
	var wg sync.WaitGroup
	results := make(chan *session.Session, 2)
	for range 2 {
		wg.Go(func() {
			s, err := restored.attachSession(t.Context(), s.ID())
			if err != nil {
				t.Error(err)
				return
			}
			results <- s
		})
	}
	wg.Wait()
	close(results)
	var previous *session.Session
	for active := range results {
		if previous != nil && previous != active {
			t.Fatal("started twice")
		}
		previous = active
	}
	if previous == nil || previous.Info().Name != "API 작업" || previous.Info().State != protocol.StateRunning {
		t.Fatal("did not resume")
	}
	if err := restored.closeSession(s.ID()); err != nil {
		t.Fatal(err)
	}
	if got := newDaemon().listSessions(); len(got) != 0 {
		t.Fatalf("deleted session restored: %+v", got)
	}
	stat, err := os.Stat(filepath.Join(dir, "sessions.json"))
	if err != nil || stat.Mode().Perm() != 0600 {
		t.Fatalf("private session file: %v, %v", stat, err)
	}
}

func TestSavedSessionFailureRetainsEntry(t *testing.T) {
	dir := t.TempDir()
	d := New(&config.Config{NodeID: "node"}, nil)
	if err := d.loadSessions(dir); err != nil {
		t.Fatal(err)
	}
	d.saved["missing"] = savedSession{Info: protocol.SessionInfo{ID: "missing", State: protocol.StateStopped}, Request: protocol.OpenSession{Agent: "shell", Project: filepath.Join(dir, "missing")}}
	if err := d.saveSessionsLocked(); err != nil {
		t.Fatal(err)
	}
	if _, err := d.attachSession(t.Context(), "missing"); err == nil {
		t.Fatal("missing directory started")
	}
	if len(d.listSessions()) != 1 {
		t.Fatal("failed activation discarded session")
	}
	if _, err := d.renameSession("missing", "try later"); err != nil {
		t.Fatal(err)
	}
	other := New(d.cfg, nil)
	if err := other.loadSessions(dir); err != nil {
		t.Fatal(err)
	}
	if other.listSessions()[0].Name != "try later" {
		t.Fatal("stopped rename not persisted")
	}
	if err := other.closeSession("missing"); err != nil {
		t.Fatal(err)
	}
	if len(other.listSessions()) != 0 {
		t.Fatal("stopped session not deleted")
	}
}

func TestInvalidSavedSessionsAreNotOverwritten(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")
	for _, body := range []string{`{broken`, `{"node_id":"other"}`} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		d := New(&config.Config{NodeID: "node"}, nil)
		if err := d.loadSessions(dir); err == nil {
			t.Fatal("invalid file accepted")
		}
		got, _ := os.ReadFile(path)
		if string(got) != body {
			t.Fatal("invalid file overwritten")
		}
	}
}
