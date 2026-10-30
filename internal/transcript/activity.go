package transcript

import (
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
	status protocol.AgentStatus
	at     time.Time
}

type ActivityState struct {
	Status protocol.AgentStatus
	At     time.Time
	Agents []*protocol.SubAgent
}

func (a *Activity) Read(info *protocol.SessionInfo, path string) (ActivityState, error) {
	logs, ok := logsOf(info.GetAgent())
	if err := verify(logs, ok, info, path); err != nil {
		return ActivityState{}, err
	}
	reset := func() {
		a.dec, a.subs = logs.NewDecoder(), newSubAgents(logs)
		a.calls = map[string]*protocol.ChatMessage{}
		a.status, a.at = "", time.Time{}
	}
	if a.path != path || a.dec == nil {
		a.path, a.tail = path, agent.Tail{}
		reset()
	}
	events, hasStatus := logs.(agent.LogStatus)
	a.tail.Read(path, reset, func(line []byte) {
		if hasStatus {
			if status, at := events.StatusEvent(line); status != "" && !at.Before(a.at) {
				a.status, a.at = status, at
			}
		}
		for _, m := range a.dec.Line(line) {
			if m.GetRole() != "tool" || m.GetId() == "" {
				continue
			}
			if m.GetOutput() {
				if call := a.calls[m.GetId()]; call != nil {
					call.Result, call.Pending = m.GetText(), false
				}
			} else if m.GetKind() == protocol.ToolTask {
				m.Pending = m.GetResult() == ""
				a.calls[m.GetId()] = m
			}
		}
	})
	calls := make([]*protocol.ChatMessage, 0, len(a.calls))
	for _, call := range a.calls {
		calls = append(calls, call)
	}
	return ActivityState{a.status, a.at, a.subs.Read(path, calls)}, nil
}
