package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeClaudeJSONL(t *testing.T, dir, name string, records []map[string]any) string {
	t.Helper()
	var b strings.Builder
	for _, r := range records {
		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(raw)
		b.WriteByte('\n')
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDecoderPreservesUserUUID(t *testing.T) {
	dec := New().NewDecoder()
	raw, _ := json.Marshal(map[string]any{
		"type":      "user",
		"uuid":      "u-uuid-123",
		"sessionId": "sess-1",
		"cwd":       "/tmp/proj",
		"message":   map[string]any{"role": "user", "content": "hello world"},
	})
	msgs := dec.Line(raw)
	if len(msgs) != 1 || msgs[0].GetId() != "u-uuid-123" || msgs[0].GetText() != "hello world" {
		t.Fatalf("decoded = %+v", msgs)
	}
}

func TestForkTranscriptBeforeByUUIDAndIndex(t *testing.T) {
	dir := t.TempDir()
	src := writeClaudeJSONL(t, dir, "orig-sess.jsonl", []map[string]any{
		{"type": "user", "uuid": "u1", "sessionId": "orig-sess", "cwd": "/tmp/proj", "message": map[string]any{"role": "user", "content": "first prompt"}},
		{"type": "assistant", "uuid": "a1", "parentUuid": "u1", "sessionId": "orig-sess", "cwd": "/tmp/proj", "message": map[string]any{"role": "assistant", "content": "first reply"}},
		{"type": "user", "uuid": "u2", "parentUuid": "a1", "sessionId": "orig-sess", "cwd": "/tmp/proj", "message": map[string]any{"role": "user", "content": "second prompt"}},
		{"type": "assistant", "uuid": "a2", "parentUuid": "u2", "sessionId": "orig-sess", "cwd": "/tmp/proj", "message": map[string]any{"role": "assistant", "content": "second reply"}},
	})

	// 1. Fork strictly before u2 by UUID.
	newID, newPath, zero, err := ForkTranscriptBefore(src, "u2", 1)
	if err != nil {
		t.Fatalf("ForkTranscriptBefore(u2): %v", err)
	}
	if zero || newID == "" || newID == "orig-sess" {
		t.Fatalf("unexpected result: newID=%q zero=%v", newID, zero)
	}
	if filepath.Dir(newPath) != dir || filepath.Base(newPath) != newID+".jsonl" {
		t.Fatalf("unexpected forked path: %s", newPath)
	}
	data, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 kept records before u2, got %d:\n%s", len(lines), string(data))
	}
	for i, l := range lines {
		var obj map[string]any
		if err := json.Unmarshal([]byte(l), &obj); err != nil {
			t.Fatalf("line %d invalid json: %v", i, err)
		}
		if obj["sessionId"] != newID {
			t.Fatalf("line %d sessionId = %v, want %q", i, obj["sessionId"], newID)
		}
	}

	// 2. Fallback by userTurnIndex when message UUID is empty.
	newID2, newPath2, zero2, err := ForkTranscriptBefore(src, "", 1)
	if err != nil || zero2 || newID2 == "" {
		t.Fatalf("ForkTranscriptBefore(index=1): id=%q zero=%v err=%v", newID2, zero2, err)
	}
	data2, _ := os.ReadFile(newPath2)
	if len(strings.Split(strings.TrimSpace(string(data2)), "\n")) != 2 {
		t.Fatalf("index fallback kept wrong lines:\n%s", string(data2))
	}

	// 3. Rewinding before turn 0 reports zeroTurns=true without creating a file.
	id0, path0, zero0, err := ForkTranscriptBefore(src, "u1", 0)
	if err != nil || !zero0 || id0 != "" || path0 != "" {
		t.Fatalf("turn 0 rewind = (%q, %q, %v, %v), want zeroTurns=true", id0, path0, zero0, err)
	}
}

func TestForkTranscriptBeforeSteeredQueuedCommand(t *testing.T) {
	dir := t.TempDir()
	src := writeClaudeJSONL(t, dir, "steered.jsonl", []map[string]any{
		{"type": "user", "uuid": "u1", "sessionId": "steered", "cwd": "/tmp/proj", "message": map[string]any{"role": "user", "content": "start task"}},
		{"type": "assistant", "uuid": "a1", "parentUuid": "u1", "sessionId": "steered", "cwd": "/tmp/proj", "message": map[string]any{"role": "assistant", "content": "working"}},
		{"type": "attachment", "uuid": "att1", "parentUuid": "a1", "sessionId": "steered", "attachment": map[string]any{"type": "queued_command", "source_uuid": "steer-msg-99"}},
		{"type": "assistant", "uuid": "a2", "parentUuid": "att1", "sessionId": "steered", "cwd": "/tmp/proj", "message": map[string]any{"role": "assistant", "content": "steered reply"}},
	})

	newID, newPath, zero, err := ForkTranscriptBefore(src, "steer-msg-99", -1)
	if err != nil || zero || newID == "" {
		t.Fatalf("steered rewind = (%q, %q, %v, %v)", newID, newPath, zero, err)
	}
	data, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 records (u1 + a1), got %d:\n%s", len(lines), string(data))
	}
	if strings.Contains(string(data), "queued_command") || strings.Contains(string(data), "steered reply") {
		t.Fatalf("forked file should exclude steered attachment and later turns:\n%s", string(data))
	}
}
