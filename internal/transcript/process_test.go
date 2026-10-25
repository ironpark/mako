package transcript

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/ironpark/sanbo/internal/protocol"
)

func TestWritablePathsAndDescendants(t *testing.T) {
	got := writablePaths("p12\nf3\nar\nn/read.jsonl\nf4\naw\nn/write.jsonl\nf5\nau\nn/update.jsonl\nf6\na \nn/not-a-writer.jsonl\n")
	if !slices.Equal(got, []string{"/write.jsonl", "/update.jsonl"}) {
		t.Fatalf("paths=%q", got)
	}
	pids := descendants(10, "1 0\n10 1\n11 10\n12 11\n20 1\n21 20\n")
	slices.Sort(pids)
	if !slices.Equal(pids, []int{10, 11, 12}) {
		t.Fatalf("pids=%v", pids)
	}
}

func TestRecoverOldSessionRequiresOneUnclaimedConversation(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC()
	info := protocol.SessionInfo{Agent: "codex", Project: "/project", StartedAt: now}
	env := map[string]string{"CODEX_HOME": root}
	add := func(id string, created time.Time) {
		line, _ := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"id": id, "cwd": "/project", "timestamp": created}})
		writeLog(t, filepath.Join(root, "sessions", "2026", "10", "26", "rollout-"+id+".jsonl"), string(line)+"\n")
	}
	add("older", now.Add(-time.Hour))
	if _, _, err := Recover(t.Context(), info, env, nil); err == nil {
		t.Fatal("old unrelated history silently starts a new conversation")
	}
	add("first", now.Add(time.Second))
	id, _, err := Recover(t.Context(), info, env, nil)
	if err != nil || id != "first" {
		t.Fatalf("migration: %q %v", id, err)
	}
	add("second", now.Add(time.Second))
	if _, _, err := Recover(t.Context(), info, env, nil); err == nil {
		t.Fatal("guessed among concurrent sessions")
	}
	id, _, err = Recover(t.Context(), info, env, map[string]bool{"second": true})
	if err != nil || id != "first" {
		t.Fatalf("claimed: %q %v", id, err)
	}
}

func TestForkDoesNotBindSourceConversation(t *testing.T) {
	if got := ArgumentID("claude", []string{"claude", "--resume", "source", "--fork-session"}); got != "" {
		t.Fatalf("fork bound source %q", got)
	}
	if got := ArgumentID("claude", []string{"claude", "--resume", "source", "--fork-session", "--session-id", "destination"}); got != "destination" {
		t.Fatalf("fork ID=%q", got)
	}
}
