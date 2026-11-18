package plugin

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ironpark/mako/internal/plugin/hostapi"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/uiblock"
)

// A plugin with sessions:write sends this node's agents messages, starts
// agents and stops those it started, through the daemon's own paths: the
// agents' mailbox, its message headed as the plugin's so that the agent
// cannot take it for the user's or mako's; and the sessions an agent's
// spawn_agent starts, the plugin named as their parent.
//
// Starting an agent runs a program that costs money. The permission
// alone does not start one: the first time, the user is asked in the
// window (or `mako plugins allow-agent`), as for the permissions, and
// allows that start alone or every one of the plugin's from then on, on
// this node. The call does not wait for the answer, which may come in
// minutes, nor for the start, which may take most of one: it returns at
// once, and the outcome is told to the plugin as an event of its own.

const (
	maxAgentPrompt   = 64 << 10
	maxAgentMessage  = 32 << 10
	maxPluginAgents  = 3
	agentStartsPerHr = 10
	messagesPerMin   = 30
	// agentAskFor is how long an ask waits for the user.
	agentAskFor = 10 * time.Minute
	// agentStartFor bounds a start, which resolves the agent's servers.
	agentStartFor = 2 * time.Minute
	// maxAskPrompt is what of the prompt the user is shown.
	maxAskPrompt = 2 << 10
)

// The kinds of event that tell a plugin how its start went; it need not
// subscribe to them.
const (
	EventAgentStarted = "agentStarted"
	EventAgentRefused = "agentRefused"
)

// AgentStart is an agent a plugin starts: the agent and profile, in a
// folder of a project of this node's, with a prompt and a name.
type AgentStart struct {
	Agent, Profile, Project, Root, Prompt, Name string
}

// AgentHost is the daemon's side of sessions:write.
type AgentHost interface {
	// Send leaves text, from the plugin, for the agent of a session of
	// this node's that takes messages, and returns the message's ID.
	Send(plugin, session, text string) (uint64, error)
	// Start starts an agent for the plugin and returns its session.
	Start(ctx context.Context, plugin string, req AgentStart) (string, error)
	// Stop stops a session the plugin started; refused for any other.
	Stop(plugin, session string) error
	// Running counts the plugin's sessions that run.
	Running(plugin string) int
}

// ParentOf is the parent a session a plugin started is saved with: no
// session's ID, so that no agent passes for it, nor it for an agent.
func ParentOf(plugin string) string { return "plugin:" + plugin }

// agentAsk is a start that waits for the user.
type agentAsk struct {
	id    string
	req   AgentStart
	name  string // the project's
	since time.Time
}

// asks are a plugin's starts waiting for the user: one at a time.
type asks struct {
	mu  sync.Mutex
	ask *agentAsk
}

func (a *asks) get() *agentAsk {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ask
}

// take takes the ask of the ID off, if it is the one waiting.
func (a *asks) take(id string) *agentAsk {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ask == nil || a.ask.id != id {
		return nil
	}
	ask := a.ask
	a.ask = nil
	return ask
}

func (h *host) MessagesSendMessage(_ context.Context, req *hostapi.SendMessageRequest) (*hostapi.SendMessageResponse, error) {
	if !h.p.msgRate.take() {
		return nil, fmt.Errorf("the plugin sent more than %d messages a minute", messagesPerMin)
	}
	text := req.GetText()
	if strings.TrimSpace(text) == "" || len(text) > maxAgentMessage || !utf8.ValidString(text) {
		return nil, fmt.Errorf("a message is text of 1 to %d KiB", maxAgentMessage>>10)
	}
	agents := h.p.m.cfg.Agents
	if agents == nil {
		return nil, errors.New("this node takes no messages from plugins")
	}
	id, err := agents.Send(h.p.id, req.GetSessionId(), text)
	if err != nil {
		return nil, err
	}
	return &hostapi.SendMessageResponse{Id: strconv.FormatUint(id, 10)}, nil
}

func (h *host) MessagesStartAgent(_ context.Context, req *hostapi.StartAgentRequest) (*hostapi.StartAgentResponse, error) {
	p := h.p
	agents := p.m.cfg.Agents
	if agents == nil {
		return nil, errors.New("this node starts no agents for plugins")
	}
	prompt := req.GetPrompt()
	if strings.TrimSpace(prompt) == "" || len(prompt) > maxAgentPrompt || !utf8.ValidString(prompt) {
		return nil, fmt.Errorf("a prompt is text of 1 to %d KiB", maxAgentPrompt>>10)
	}
	name := strings.Join(strings.Fields(req.GetName()), " ")
	if len(name) > 200 || strings.ContainsFunc(name, uiblock.Spoofing) {
		return nil, errors.New("a name is at most 200 bytes, without control or reordering characters")
	}
	if req.GetAgent() == "" || len(req.GetAgent()) > 64 || len(req.GetProfile()) > 64 {
		return nil, errors.New("name the agent to start, and its profile if any")
	}
	project, ok := p.m.project(req.GetProject(), req.GetRoot())
	if !ok {
		return nil, fmt.Errorf("this node knows no project %q with the folder %q: see files.listProjects", req.GetProject(), req.GetRoot())
	}
	if n := agents.Running(p.id); n >= maxPluginAgents {
		return nil, fmt.Errorf("%d agents the plugin started run: stop one first", n)
	}
	// Every try counts toward the hour's, one refused for an ask waiting
	// too, so that a plugin cannot ask in a loop.
	if !p.startRate.take() {
		return nil, fmt.Errorf("the plugin started %d agents in the last hour, its most", agentStartsPerHr)
	}
	if p.asks.get() != nil {
		return nil, errors.New("another agent the plugin asked to start waits for the user")
	}
	ask := &agentAsk{id: "a" + nonce(), name: project.Name, since: time.Now(),
		req: AgentStart{Agent: req.GetAgent(), Profile: req.GetProfile(), Project: req.GetProject(), Root: req.GetRoot(), Prompt: prompt, Name: name}}
	if p.m.grant.get(p.id).AgentsAllowed {
		go p.startAgent(ask)
		return &hostapi.StartAgentResponse{RequestId: ask.id}, nil
	}
	p.asks.mu.Lock()
	p.asks.ask = ask
	p.asks.mu.Unlock()
	p.m.cfg.Log.Info("plugin asks to start an agent", "plugin", p.id, "agent", ask.req.Agent, "root", ask.req.Root)
	p.status(func() {})
	return &hostapi.StartAgentResponse{RequestId: ask.id, Asked: true}, nil
}

func (h *host) MessagesStopAgent(_ context.Context, req *hostapi.StopAgentRequest) (*hostapi.StopAgentResponse, error) {
	agents := h.p.m.cfg.Agents
	if agents == nil {
		return nil, errors.New("this node starts no agents for plugins")
	}
	if err := agents.Stop(h.p.id, req.GetSessionId()); err != nil {
		return nil, err
	}
	return &hostapi.StopAgentResponse{}, nil
}

// project is the project of the ID if it has the folder root.
func (m *Manager) project(id, root string) (ProjectRoots, bool) {
	if m.cfg.Projects == nil {
		return ProjectRoots{}, false
	}
	for _, pr := range m.cfg.Projects() {
		if pr.ID == id {
			for _, r := range pr.Roots {
				if r == root {
					return pr, true
				}
			}
		}
	}
	return ProjectRoots{}, false
}

// startAgent starts what ask asks for, off the plugin's actor, and tells
// the plugin how it went.
func (p *plugin) startAgent(ask *agentAsk) {
	// Whatever was allowed, a plugin refused, disabled, blocked or removed
	// since it asked starts nothing.
	if state, _ := p.state(); (state != protocol.PluginReady && state != protocol.PluginRunning) || !p.grants().Has(hostapi.PermSessionsWrite) {
		p.tellAsk(ask, "", "the plugin may not run now")
		return
	}
	select {
	case <-p.done:
		return
	default:
	}
	ctx, cancel := context.WithTimeout(p.m.ctx, agentStartFor)
	defer cancel()
	sid, err := p.m.cfg.Agents.Start(ctx, p.id, ask.req)
	if err != nil {
		p.m.cfg.Log.Warn("plugin's agent did not start", "plugin", p.id, "err", err)
		p.tellAsk(ask, "", err.Error())
		return
	}
	p.m.cfg.Log.Info("plugin started an agent", "plugin", p.id, "session", sid)
	p.tellAsk(ask, sid, "")
}

// tellAsk tells the plugin its start's outcome: the session it started,
// or why none did.
func (p *plugin) tellAsk(ask *agentAsk, session, why string) {
	ev := &hostapi.SessionEvent{Kind: EventAgentStarted, RequestId: ask.id}
	if why != "" {
		ev.Kind, ev.Error = EventAgentRefused, why
	} else {
		ev.Session = p.m.sessionOf(session)
		if ev.Session == nil {
			ev.Session = &hostapi.Session{Id: session}
		}
	}
	p.post(func() { p.deliver(ev, hostapi.PermSessionsWrite) })
}

// answerAgent is the user's answer to the plugin's ask of the ID.
func (p *plugin) answerAgent(id, op, value string) error {
	ask := p.asks.get()
	if ask == nil || ask.id != id {
		return errors.New("the plugin asks to start no such agent now")
	}
	if op == protocol.PluginAllowAgent && value != protocol.AgentOnce && value != protocol.AgentAlways {
		return fmt.Errorf("allow_agent takes %q or %q", protocol.AgentOnce, protocol.AgentAlways)
	}
	if p.asks.take(id) == nil {
		return errors.New("the ask was answered already")
	}
	defer p.status(func() {})
	if op != protocol.PluginAllowAgent {
		p.m.cfg.Log.Info("plugin's agent refused by the user", "plugin", p.id)
		p.tellAsk(ask, "", "the user refused to start the agent")
		return nil
	}
	if value == protocol.AgentAlways {
		if err := p.m.grant.update(p.id, func(e *grantEntry) { e.AgentsAllowed = true }); err != nil {
			return err
		}
	}
	go p.startAgent(ask)
	return nil
}

// dropAsk refuses the ask waiting, for why: the plugin was refused,
// disabled or blocked.
func (p *plugin) dropAsk(why string) {
	if ask := p.asks.get(); ask != nil && p.asks.take(ask.id) != nil {
		p.tellAsk(ask, "", why)
	}
}

// expireAsk refuses an ask the user left too long, as the actor ticks.
func (p *plugin) expireAsk(now time.Time) {
	ask := p.asks.get()
	if ask == nil || now.Sub(ask.since) < agentAskFor {
		return
	}
	if p.asks.take(ask.id) != nil {
		p.tellAsk(ask, "", fmt.Sprintf("the user did not answer within %v", agentAskFor))
		p.status(func() {})
	}
}

// askInfo is the ask as a front-end shows it.
func (a *agentAsk) info() *protocol.PluginAgentAsk {
	if a == nil {
		return nil
	}
	prompt, _ := cut(a.req.Prompt, maxAskPrompt)
	return &protocol.PluginAgentAsk{Id: a.id, Agent: a.req.Agent, Profile: a.req.Profile, Project: a.name,
		Root: a.req.Root, Name: a.req.Name, Prompt: protocol.Printable(prompt), Since: a.since.UnixMilli()}
}
