package transcript

import (
	"fmt"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/agent"
)

// A task starts from its call, ends once, and the list keeps those running
// with the last few that ended.
func TestBackgroundTasksKeepTheRunningAndTheLatest(t *testing.T) {
	var b backgroundTasks
	at := time.Unix(1000, 0)
	for i := range keptTasks + 3 {
		call, id := fmt.Sprint("c", i), fmt.Sprint("b", i)
		b.take(agent.TaskEvent{Kind: agent.TaskCalled, CallID: call, Command: "cmd " + id})
		b.take(agent.TaskEvent{Kind: agent.TaskStarted, CallID: call, ID: id, At: at})
		b.take(agent.TaskEvent{Kind: agent.TaskStarted, CallID: call, ID: id, At: at}) // written again
		if i > 0 {
			b.take(agent.TaskEvent{Kind: agent.TaskEnded, ID: id, Status: "completed", At: at.Add(time.Minute)})
		}
	}
	b.take(agent.TaskEvent{Kind: agent.TaskEnded, ID: "a-subagent", Status: "completed"})
	got := b.list()
	if len(got) != 1+keptTasks || got[0].GetId() != "b0" || !got[0].GetRunning() || got[0].GetCommand() != "cmd b0" {
		t.Fatalf("list %v", got)
	}
	if last := got[len(got)-1]; last.GetId() != fmt.Sprint("b", keptTasks+2) || last.GetRunning() || last.GetEnded() != 1060 {
		t.Fatalf("last %v", last)
	}
}
