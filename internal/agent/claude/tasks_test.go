package claude

import (
	"testing"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/protocol"
)

// A background command is told by its call, the task its answer names and
// the notification of its end, with its exit code; a stop ends one too.
func TestBackgroundCommandsAreFollowed(t *testing.T) {
	lines := []string{
		`{"type":"assistant","timestamp":"2026-10-29T02:00:00Z","message":{"content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"go test ./...\nmore","description":"Run the tests","run_in_background":true}}]}}`,
		`{"type":"user","timestamp":"2026-10-29T02:00:01Z","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"Command running in background with ID: b1. Output is being written to: /tmp/x/tasks/b1.output. You will be notified"}]},"toolUseResult":{"backgroundTaskId":"b1"}}`,
		`{"type":"user","timestamp":"2026-10-29T02:05:00Z","message":{"content":"<task-notification>\n<task-id>b1</task-id>\n<tool-use-id>toolu_1</tool-use-id>\n<status>completed</status>\n<summary>Background command \"Run the tests\" completed (exit code 2)</summary>\n</task-notification>"}}`,
		`{"type":"assistant","timestamp":"2026-10-29T02:06:00Z","message":{"content":[{"type":"tool_use","id":"toolu_2","name":"TaskStop","input":{"task_id":"b2"}}]}}`,
		`{"type":"queue-operation","operation":"enqueue","timestamp":"2026-10-29T02:07:00Z","content":"<task-notification>\n<task-id>b3</task-id>\n<status>failed</status>\n</task-notification>"}`,
		`{"type":"queue-operation","operation":"remove","timestamp":"2026-10-29T02:07:01Z","content":"<task-notification>\n<task-id>b3</task-id>\n<status>failed</status>\n</task-notification>"}`,
		`{"type":"assistant","timestamp":"2026-10-29T02:06:00Z","message":{"content":[{"type":"tool_use","id":"toolu_3","name":"Bash","input":{"command":"ls"}}]}}`,
	}
	var got []agent.TaskEvent
	for _, l := range lines {
		got = append(got, (&Agent{}).TaskEvents([]byte(l))...)
	}
	if len(got) != 5 {
		t.Fatalf("events %+v", got)
	}
	if c := got[0]; c.Kind != agent.TaskCalled || c.CallID != "toolu_1" || c.Command != "go test ./...\nmore" || c.Description != "Run the tests" {
		t.Fatalf("call %+v", c)
	}
	if s := got[1]; s.Kind != agent.TaskStarted || s.ID != "b1" || s.CallID != "toolu_1" || s.Output != "/tmp/x/tasks/b1.output" {
		t.Fatalf("start %+v", s)
	}
	if e := got[2]; e.Kind != agent.TaskEnded || e.ID != "b1" || e.Status != protocol.TaskCompleted || e.ExitCode == nil || *e.ExitCode != 2 {
		t.Fatalf("end %+v", e)
	}
	if k := got[3]; k.Kind != agent.TaskEnded || k.ID != "b2" || k.Status != protocol.TaskKilled {
		t.Fatalf("stop %+v", k)
	}
	if q := got[4]; q.Kind != agent.TaskEnded || q.ID != "b3" || q.Status != protocol.TaskFailed {
		t.Fatalf("queued %+v", q)
	}
}
