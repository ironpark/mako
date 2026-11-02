package transcript

import (
	"slices"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/protocol"
)

// keptTasks is how many tasks that ended a conversation's list keeps, past
// those running.
const keptTasks = 5

// backgroundTasks follows the commands a conversation's agent runs in the
// background, as its log's lines tell of them (agent.BackgroundTasks).
type backgroundTasks struct {
	calls map[string]agent.TaskEvent          // the calls asking for one, by call ID, until answered
	tasks []*protocol.BackgroundTask          // in the order they started
	byID  map[string]*protocol.BackgroundTask // the same, by the task's ID
}

// take follows what ev says.
func (b *backgroundTasks) take(ev agent.TaskEvent) {
	if b.calls == nil {
		b.calls, b.byID = map[string]agent.TaskEvent{}, map[string]*protocol.BackgroundTask{}
	}
	switch ev.Kind {
	case agent.TaskCalled:
		b.calls[ev.CallID] = ev
	case agent.TaskStarted:
		call, ok := b.calls[ev.CallID]
		if !ok || ev.ID == "" || b.byID[ev.ID] != nil {
			return // a call out of the log, or a record written again
		}
		delete(b.calls, ev.CallID)
		t := &protocol.BackgroundTask{Id: ev.ID, CallId: ev.CallID, Command: call.Command, Description: call.Description,
			Running: true, Started: ev.At.Unix(), Output: ev.Output}
		b.tasks = append(b.tasks, t)
		b.byID[ev.ID] = t
	case agent.TaskEnded:
		t := b.byID[ev.ID]
		if t == nil || !t.GetRunning() {
			return // another sort of task's, as a subagent's, or ended already
		}
		t.Running, t.Status, t.ExitCode, t.Ended = false, ev.Status, ev.ExitCode, ev.At.Unix()
	}
}

// list is the tasks running and the last that ended, oldest first, each a
// copy of its own.
func (b *backgroundTasks) list() []*protocol.BackgroundTask {
	ended := 0
	var out []*protocol.BackgroundTask
	for i := len(b.tasks) - 1; i >= 0; i-- {
		t := b.tasks[i]
		if !t.GetRunning() {
			if ended++; ended > keptTasks {
				continue
			}
		}
		out = append(out, proto.CloneOf(t))
	}
	slices.Reverse(out)
	return out
}
