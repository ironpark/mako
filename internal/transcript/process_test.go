package transcript

import (
	"encoding/json"
	"maps"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/protocol"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestWritablePathsAndDescendants(t *testing.T) {
	got := writablePaths("p12\nf3\nar\nn/read.jsonl\nf4\naw\nn/write.jsonl\np13\nf5\nau\nn/update.jsonl\nf6\na \nn/not-a-writer.jsonl\n")
	if !maps.EqualFunc(got, map[int][]string{12: {"/write.jsonl"}, 13: {"/update.jsonl"}}, slices.Equal) {
		t.Fatalf("paths=%v", got)
	}
	tree := descendants([]int{10, 20}, "1 0\n10 1\n11 10\n12 11\n20 1\n21 20\n30 1\n")
	if !maps.Equal(tree, map[int]int{10: 10, 11: 10, 12: 10, 20: 20, 21: 20}) {
		t.Fatalf("tree=%v", tree)
	}
}

func TestRecoverOldSessionRequiresOneUnclaimedConversation(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC()
	info := &protocol.SessionInfo{Agent: "codex", Project: "/project", StartedAt: timestamppb.New(now)}
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
