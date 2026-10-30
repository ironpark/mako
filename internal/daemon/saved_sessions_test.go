package daemon

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
	"google.golang.org/protobuf/proto"
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
	req := &protocol.OpenSession{Agent: "test", Profile: "work", Project: project, Args: []string{"-c", "echo started >> starts; exec cat"}, Env: map[string]string{"EXAMPLE": "value"}}
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
	if len(list) != 1 || list[0].GetId() != s.ID() || list[0].GetName() != "API 작업" || list[0].GetProject() != project || list[0].GetProfile() != "work" || list[0].GetState() != protocol.StateStopped {
		t.Fatalf("restored: %+v", list)
	}
	if len(restored.sessions.list()) != 0 {
		t.Fatal("restoration started a process")
	}
	if restored.saved[s.ID()].Request.GetEnv()["EXAMPLE"] != "value" {
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
	if previous == nil || previous.Info().GetName() != "API 작업" || previous.Info().GetState() != protocol.StateRunning {
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
	d.saved["missing"] = savedSession{Info: &protocol.SessionInfo{Id: "missing", State: protocol.StateStopped}, Request: &protocol.OpenSession{Agent: "shell", Project: filepath.Join(dir, "missing")}}
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
	if other.listSessions()[0].GetName() != "try later" {
		t.Fatal("stopped rename not persisted")
	}
	if err := other.closeSession("missing"); err != nil {
		t.Fatal(err)
	}
	if len(other.listSessions()) != 0 {
		t.Fatal("stopped session not deleted")
	}
}

func TestSavedSessionsDiscardRuntimeActivity(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{NodeID: "node"}
	d := New(cfg, nil)
	if err := d.loadSessions(dir); err != nil {
		t.Fatal(err)
	}
	d.saved["s"] = savedSession{Info: &protocol.SessionInfo{
		Id: "s", State: protocol.StateRunning, Busy: true, Status: protocol.AgentWaiting,
		StatusAt: protocol.Time(time.Now()), Approval: &protocol.Approval{Id: "old"},
	}}
	if err := d.saveSessionsLocked(); err != nil {
		t.Fatal(err)
	}
	restored := New(cfg, nil)
	if err := restored.loadSessions(dir); err != nil {
		t.Fatal(err)
	}
	info := restored.saved["s"].Info
	if info.GetState() != protocol.StateStopped || info.GetBusy() || info.GetStatus() != "" || info.GetStatusAt() != nil || info.GetApproval() != nil {
		t.Fatalf("restored stale runtime activity: %+v", info)
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

// A sessions.json that encoding/json wrote, before the messages were
// protobuf, loads as it was: names, times and the request alike.
func TestSavedSessionsOfTheJSONEraLoad(t *testing.T) {
	dir := t.TempDir()
	old := `{
  "node_id": "node",
  "sessions": {
    "9274aec8b08527fe": {
      "info": {
        "id": "9274aec8b08527fe", "node_id": "node", "agent": "claude",
        "conversation_id": "b609d53c", "name": "API", "project": "/src/api",
        "command": null, "state": "running", "cols": 120, "rows": 36,
        "started_at": "2026-10-26T21:04:31.807642+09:00", "idle": true,
        "status_at": "0001-01-01T00:00:00Z", "last_output": "0001-01-01T00:00:00Z",
        "worktree": {"repo": "/src/api", "branch": "agent/x"}
      },
      "request": {"agent": "claude", "project": "/src/api", "cols": 120, "rows": 36, "env": {"A": "1"}},
      "transcript_path": "/logs/b609d53c.jsonl"
    }
  }
}`
	if err := os.WriteFile(filepath.Join(dir, "sessions.json"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	d := New(&config.Config{NodeID: "node"}, nil)
	if err := d.loadSessions(dir); err != nil {
		t.Fatal(err)
	}
	e := d.saved["9274aec8b08527fe"]
	started, _ := time.Parse(time.RFC3339Nano, "2026-10-26T21:04:31.807642+09:00")
	if e.Info.GetConversationId() != "b609d53c" || e.Info.GetName() != "API" || e.Info.GetCols() != 120 ||
		!protocol.TimeOf(e.Info.GetStartedAt()).Equal(started) || e.Info.GetWorktree().GetBranch() != "agent/x" ||
		e.Info.GetState() != protocol.StateStopped || e.TranscriptPath != "/logs/b609d53c.jsonl" {
		t.Fatalf("info %v, path %q", e.Info, e.TranscriptPath)
	}
	if e.Request.GetProject() != "/src/api" || e.Request.GetEnv()["A"] != "1" {
		t.Fatalf("request %v", e.Request)
	}
	// Saved again and read back, it is the same.
	if err := d.saveSessionsLocked(); err != nil {
		t.Fatal(err)
	}
	again := New(&config.Config{NodeID: "node"}, nil)
	if err := again.loadSessions(dir); err != nil {
		t.Fatal(err)
	}
	if a := again.saved["9274aec8b08527fe"]; !proto.Equal(a.Info, e.Info) || !proto.Equal(a.Request, e.Request) {
		t.Fatalf("round trip %v %v", a.Info, a.Request)
	}
}
