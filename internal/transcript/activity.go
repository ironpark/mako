package transcript

import (
	"slices"
	"strings"
	"time"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/protocol"
)

// Activity follows turn boundaries and all task calls, independently of
// the chat's paged window. Children can work while their parent is quiet.
// Its caller serializes Read.
type Activity struct {
	path   string
	tail   agent.Tail
	dec    agent.Decoder
	subs   agent.SubAgentScan
	calls  map[string]*protocol.ChatMessage
	list   []*protocol.ChatMessage // calls, as the latest read handed them to subs
	status protocol.AgentStatus
	at     time.Time
	said   time.Time // when the latest prompt or reply was written
	tasks  backgroundTasks
	// commands are those the log says the agent takes
	// (agent.LoggedCommands), in the order first said.
	commands []agent.Command
}

type ActivityState struct {
	Status protocol.AgentStatus
	At     time.Time
	Said   time.Time
	Agents []*protocol.SubAgent
	Tasks  []*protocol.BackgroundTask // see backgroundTasks
	// Commands are those the log says the agent takes, beside the files
	// that say others: see agent.LoggedCommands.
	Commands []agent.Command
}

func (a *Activity) Read(info *protocol.SessionInfo, path string) (ActivityState, error) {
	logs, ok := logsOf(info.GetAgent())
	if err := verify(logs, ok, info, path); err != nil {
		return ActivityState{}, err
	}
	reset := func() {
		a.dec, a.subs = logs.NewDecoder(), newSubAgents(logs)
		a.calls = map[string]*protocol.ChatMessage{}
		a.status, a.at, a.said = "", time.Time{}, time.Time{}
		a.tasks = backgroundTasks{}
		a.commands = nil
	}
	if a.path != path || a.dec == nil {
		a.path, a.tail = path, agent.Tail{}
		reset()
	}
	events, hasStatus := logs.(agent.LogStatus)
	tasks, hasTasks := logs.(agent.BackgroundTasks)
	commands, hasCommands := logs.(agent.LoggedCommands)
	a.tail.Read(path, reset, func(line []byte) {
		if hasCommands {
			for _, c := range commands.LogCommands(line) {
				if i := slices.IndexFunc(a.commands, func(k agent.Command) bool { return k.Name == c.Name }); i >= 0 {
					a.commands[i] = c
				} else {
					a.commands = append(a.commands, c)
				}
			}
		}
		if hasTasks {
			for _, ev := range tasks.TaskEvents(line) {
				a.tasks.take(ev)
			}
		}
		if hasStatus {
			if status, at := events.StatusEvent(line); status != "" && !at.Before(a.at) {
				a.status, a.at = status, at
			}
		}
		for _, m := range a.dec.Line(line) {
			if m.GetRole() != "tool" {
				if a.tail.Updated.After(a.said) {
					a.said = a.tail.Updated
				}
				continue
			}
			if m.GetId() == "" {
				continue
			}
			if m.GetOutput() {
				if call := a.calls[m.GetId()]; call != nil {
					call.Result, call.Pending = keptResult(m.GetText()), false
				}
			} else if m.GetKind() == protocol.ToolTask {
				a.calls[m.GetId()] = &protocol.ChatMessage{
					Role: m.GetRole(), Id: m.GetId(), Kind: m.GetKind(), Text: m.GetText(),
					Result: keptResult(m.GetResult()), Pending: m.GetResult() == "",
				}
			}
		}
	})
	a.list = a.list[:0]
	for _, call := range a.calls {
		a.list = append(a.list, call)
	}
	return ActivityState{a.status, a.at, a.said, a.subs.Read(path, a.list), a.tasks.list(), slices.Clone(a.commands)}, nil
}

// resultKept is as much of a task's result as is kept for the whole
// conversation. A scan reads one only for the child it names, which the
// start of a result says; a child's closing report can run to many
// kilobytes, and every task the conversation ran has one.
const resultKept = 4 << 10

// keptResult is the start of result, copied, so that the rest is released.
func keptResult(result string) string {
	if len(result) <= resultKept {
		return result
	}
	return strings.Clone(result[:resultKept])
}
