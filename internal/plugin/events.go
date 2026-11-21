package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"sync"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ironpark/mako/internal/plugin/hostapi"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/pkg/fsutil"
)

// A plugin with sessions:read may subscribe to what happens to this
// node's sessions: one starting, ending or going to sleep, its agent's
// status changing, a message for one of its agents. The Host API has no
// streams (its WIT could not say one), so a subscription is a call that
// says which kinds, and each event is the host calling into the plugin:
// the handler of its kind in the default export's on, as a block's on is
// called for a node's message. A plugin not running is started for an
// event, as for a block shown; what it subscribed to is kept on this node
// (subscriptions.json in its data folder) so that it is told after it
// slept or the daemon started again.
//
// The daemon tells the manager what changed (SessionsChanged, from the
// notifier that tells the front-ends; AgentMessage, from the mailbox),
// and the manager works out the events by the sessions it last saw. Only
// this node's sessions are listed, so no event is of another node's.

// The kinds of event.
const (
	EventSessionStarted = "sessionStarted"
	EventSessionEnded   = "sessionEnded"
	EventSessionSlept   = "sessionSlept"
	EventSessionStatus  = "sessionStatus"
	EventAgentMessage   = "agentMessage"
)

var eventKinds = []string{EventSessionStarted, EventSessionEnded, EventSessionSlept, EventSessionStatus, EventAgentMessage}

const (
	// eventsPerSecond bounds the events a plugin is told; the rest are
	// dropped.
	eventsPerSecond = 10
	// maxEventText bounds an agent message's text in an event.
	maxEventText = 4 << 10
)

// subscriptions are the kinds a plugin subscribed to.
type subscriptions struct {
	mu     sync.Mutex
	path   string
	kinds  []string
	loaded bool
}

func (s *subscriptions) loadLocked() {
	if s.loaded {
		return
	}
	s.loaded = true
	s.kinds = nil
	if b, err := os.ReadFile(s.path); err == nil {
		var kinds []string
		_ = json.Unmarshal(b, &kinds)
		for _, k := range kinds {
			if slices.Contains(eventKinds, k) && !slices.Contains(s.kinds, k) {
				s.kinds = append(s.kinds, k)
			}
		}
	}
}

func (s *subscriptions) has(kind string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	return slices.Contains(s.kinds, kind)
}

func (s *subscriptions) any() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	return len(s.kinds) > 0
}

// change adds or removes kinds, all of them for remove with none, and
// returns the kinds after.
func (s *subscriptions) change(kinds []string, add bool) ([]string, error) {
	for _, k := range kinds {
		if !slices.Contains(eventKinds, k) {
			return nil, fmt.Errorf("%q is no kind of event: %v", k, eventKinds)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	next := slices.Clone(s.kinds)
	switch {
	case add:
		for _, k := range kinds {
			if !slices.Contains(next, k) {
				next = append(next, k)
			}
		}
	case len(kinds) == 0:
		next = nil
	default:
		next = slices.DeleteFunc(next, func(k string) bool { return slices.Contains(kinds, k) })
	}
	sort.Strings(next)
	if len(next) == 0 {
		if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	} else {
		b, _ := json.Marshal(next)
		if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
			return nil, err
		}
		if err := fsutil.Write(s.path, b); err != nil {
			return nil, err
		}
	}
	s.kinds = next
	return slices.Clone(next), nil
}

func (s *subscriptions) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loaded, s.kinds = false, nil
}

// eventFeed is the manager's side: the sessions last seen, and who
// waits to hear whether events are wanted.
type eventFeed struct {
	mu     sync.Mutex
	last   map[string]*protocol.SessionInfo // nil until the first listing
	wanted chan struct{}                    // closed when a subscription changes
}

func (m *Manager) subsChanged() {
	m.events.mu.Lock()
	if m.events.wanted != nil {
		close(m.events.wanted)
	}
	m.events.wanted = make(chan struct{})
	m.events.mu.Unlock()
}

// EventsWanted says whether a plugin subscribed to an event, and gives a
// channel closed when that may have changed: the daemon follows its
// sessions for the plugins only while one is.
func (m *Manager) EventsWanted() (bool, <-chan struct{}) {
	m.events.mu.Lock()
	if m.events.wanted == nil {
		m.events.wanted = make(chan struct{})
	}
	ch := m.events.wanted
	m.events.mu.Unlock()
	for _, p := range m.all() {
		if p.subs.any() {
			return true, ch
		}
	}
	m.events.mu.Lock()
	m.events.last = nil // seen anew when wanted again
	m.events.mu.Unlock()
	return false, ch
}

// SessionsChanged takes the node's sessions as they stand, and tells the
// plugins subscribed what happened since the last: the first listing is
// taken as how things stood, and tells nothing.
func (m *Manager) SessionsChanged(list []*protocol.SessionInfo) {
	now := make(map[string]*protocol.SessionInfo, len(list))
	for _, s := range list {
		now[s.GetId()] = s
	}
	m.events.mu.Lock()
	last := m.events.last
	m.events.last = now
	m.events.mu.Unlock()
	if last == nil {
		return
	}
	var out []*hostapi.SessionEvent
	ev := func(kind string, s *protocol.SessionInfo, prev string) {
		out = append(out, &hostapi.SessionEvent{Kind: kind, Session: session(s), PreviousStatus: prev})
	}
	ids := make([]string, 0, len(now)+len(last))
	for id := range now {
		ids = append(ids, id)
	}
	for id := range last {
		if _, ok := now[id]; !ok {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		s, was := now[id], last[id]
		switch {
		case was == nil:
			ev(EventSessionStarted, s, "")
		case s == nil:
			if was.GetState() != protocol.StateExited {
				ev(EventSessionEnded, was, "")
			}
		default:
			if s.GetState() != was.GetState() {
				switch s.GetState() {
				case protocol.StateExited:
					ev(EventSessionEnded, s, "")
				case protocol.StateStopped:
					ev(EventSessionSlept, s, "")
				case protocol.StateRunning, protocol.StateStarting:
					if was.GetState() == protocol.StateStopped || was.GetState() == protocol.StateExited {
						ev(EventSessionStarted, s, "")
					}
				}
			}
			if s.GetStatus() != was.GetStatus() && s.GetState() != protocol.StateExited {
				ev(EventSessionStatus, s, was.GetStatus())
			}
		}
	}
	for _, e := range out {
		m.publish(e)
	}
}

// AgentMessage tells the plugins subscribed of a message for an agent of
// this node's.
func (m *Manager) AgentMessage(id uint64, from, to, text string, note bool) {
	text, _ = cut(text, maxEventText)
	m.publish(&hostapi.SessionEvent{Kind: EventAgentMessage,
		Session: m.sessionOf(to),
		Message: &hostapi.AgentMessage{Id: strconv.FormatUint(id, 10), FromSessionId: from, ToSessionId: to, Text: text, Note: note}})
}

func (m *Manager) sessionOf(id string) *hostapi.Session {
	for _, s := range m.cfg.Sessions() {
		if s.GetId() == id {
			return session(s)
		}
	}
	return nil
}

// publish hands the event to each plugin subscribed to its kind that may
// read sessions, within its rate: on its actor, started if need be.
func (m *Manager) publish(ev *hostapi.SessionEvent) {
	for _, p := range m.all() {
		if !p.subs.has(ev.GetKind()) || !p.grants().Has(hostapi.PermSessionsRead) {
			continue
		}
		if !p.subRate.take() {
			p.dropped()
			continue
		}
		if !p.post(func() { p.deliver(ev, hostapi.PermSessionsRead) }) {
			p.dropped()
		}
	}
}

// dropped notes an event the plugin was not told, once a minute in the
// log.
func (p *plugin) dropped() {
	if p.dropLog.take() {
		p.m.cfg.Log.Warn("plugin missed events: more came than it may be told", "plugin", p.id)
	}
}

// deliver tells the plugin of an event, if it holds perm, starting it if
// it may run.
func (p *plugin) deliver(ev *hostapi.SessionEvent, perm string) {
	if !p.grants().Has(perm) || p.ensure() != nil {
		return
	}
	b, err := protojson.MarshalOptions{EmitUnpopulated: true}.Marshal(ev)
	if err != nil {
		return
	}
	if _, err := p.invoke("sessionEvent", false, string(b)); err != nil {
		p.m.cfg.Log.Debug("plugin's event handler failed", "plugin", p.id, "kind", ev.GetKind(), "err", err)
	}
	p.afterCall("")
}

func (h *host) SessionsSubscribe(_ context.Context, req *hostapi.SubscribeRequest) (*hostapi.SubscribeResponse, error) {
	kinds, err := h.p.subs.change(req.GetEvents(), true)
	if err != nil {
		return nil, err
	}
	h.p.m.subsChanged()
	return &hostapi.SubscribeResponse{Events: kinds}, nil
}

func (h *host) SessionsUnsubscribe(_ context.Context, req *hostapi.UnsubscribeRequest) (*hostapi.UnsubscribeResponse, error) {
	kinds, err := h.p.subs.change(req.GetEvents(), false)
	if err != nil {
		return nil, err
	}
	h.p.m.subsChanged()
	return &hostapi.UnsubscribeResponse{Events: kinds}, nil
}
