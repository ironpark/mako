package plugin

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ironpark/mako/internal/plugin/hostapi"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/uiblock"
)

// The kinds of event that tell a plugin how an agent question went; it need
// not subscribe to them.
const (
	EventAgentAskStarted = "agentAskStarted"
	EventAgentAnswered   = "agentAnswered"
	EventAgentAskFailed  = "agentAskFailed"

	maxAskPromptBytes = 96 << 10
	maxAskSchema      = 16 << 10
	maxAskOutput      = 64 << 10
	maxAskTagBytes    = 512
	maxAskTitleBytes  = 200
	maxPluginAsks     = 2
	asksPerHour       = 30
	askDefaultFor     = 120 * time.Second
	askMaxFor         = 300 * time.Second
)

// askMinFor is the shortest ask timeout (a var so unit tests can exercise timeout quickly).
var askMinFor = 15 * time.Second

// AgentChoice is an agent configuration a plugin may list.
type AgentChoice struct {
	Agent, Profile, Description, Model, Effort string
	Installed, Askable                         bool
	Reason                                     string
}

// AgentQuestion is a one-shot ask carried out for a plugin.
type AgentQuestion struct {
	Agent, Profile, Root, Prompt, Format, JSONSchema string
	Timeout                                          time.Duration
}

// AskHost is the daemon's side of agents:ask.
type AskHost interface {
	Choices() []AgentChoice
	// Ask runs the agent once in root and returns its answer. The host adds
	// the plugin's header, turns YOLO off and bounds the run by ctx.
	Ask(ctx context.Context, plugin string, q AgentQuestion) (string, error)
}

func askKey(agent, profile string) string { return agent + "/" + profile }

func agentLabel(agent, profile string) string {
	if profile == "" {
		return agent
	}
	return agent + " · " + profile
}

func (h *host) AgentsListAgents(_ context.Context, _ *hostapi.ListAgentsRequest) (*hostapi.ListAgentsResponse, error) {
	asksHost := h.p.m.cfg.Asks
	if asksHost == nil {
		return &hostapi.ListAgentsResponse{}, nil
	}
	g := h.p.m.grant.get(h.p.id)
	choices := asksHost.Choices()
	out := make([]*hostapi.AgentProfile, 0, len(choices))
	for _, c := range choices {
		out = append(out, &hostapi.AgentProfile{
			Agent:         c.Agent,
			Profile:       c.Profile,
			Label:         agentLabel(c.Agent, c.Profile),
			Description:   c.Description,
			Model:         c.Model,
			Effort:        c.Effort,
			Installed:     c.Installed,
			Askable:       c.Askable,
			Reason:        c.Reason,
			AlwaysAllowed: slices.Contains(g.AsksAllowed, askKey(c.Agent, c.Profile)),
		})
	}
	return &hostapi.ListAgentsResponse{Agents: out}, nil
}

func (h *host) AgentsAskAgent(_ context.Context, req *hostapi.AskAgentRequest) (*hostapi.AskAgentResponse, error) {
	p := h.p
	asksHost := p.m.cfg.Asks
	if asksHost == nil {
		return nil, errors.New("this node asks no agents for plugins")
	}
	agentName, profile := req.GetAgent(), req.GetProfile()
	if agentName == "" || len(agentName) > 64 || len(profile) > 64 {
		return nil, errors.New("name the agent to ask, and its profile if any")
	}
	prompt := req.GetPrompt()
	if strings.TrimSpace(prompt) == "" || len(prompt) > maxAskPromptBytes || !utf8.ValidString(prompt) {
		return nil, fmt.Errorf("a prompt is text of 1 to %d KiB", maxAskPromptBytes>>10)
	}
	format := strings.ToLower(strings.TrimSpace(req.GetFormat()))
	if format == "" {
		format = "text"
	}
	if format != "text" && format != "json" {
		return nil, fmt.Errorf("format %q is not text or json", format)
	}
	schema := req.GetJsonSchema()
	if schema != "" {
		if len(schema) > maxAskSchema || !utf8.ValidString(schema) || !json.Valid([]byte(schema)) {
			return nil, fmt.Errorf("json_schema must be valid JSON of at most %d KiB", maxAskSchema>>10)
		}
	}
	title := strings.Join(strings.Fields(req.GetTitle()), " ")
	if len(title) > maxAskTitleBytes || strings.ContainsFunc(title, uiblock.Spoofing) {
		return nil, fmt.Errorf("a title is at most %d bytes, without control or reordering characters", maxAskTitleBytes)
	}
	tag := req.GetTag()
	if len(tag) > maxAskTagBytes || !utf8.ValidString(tag) {
		return nil, fmt.Errorf("a tag is UTF-8 of at most %d bytes", maxAskTagBytes)
	}
	timeout := askDefaultFor
	if ms := req.GetTimeoutMs(); ms > 0 {
		timeout = time.Duration(ms) * time.Millisecond
		if timeout < askMinFor {
			timeout = askMinFor
		}
		if timeout > askMaxFor {
			timeout = askMaxFor
		}
	}

	proj, resolvedRoot, ok := p.m.projectRoot(req.GetProject(), req.GetRoot())
	if !ok {
		return nil, fmt.Errorf("this node knows no project %q with the folder %q", req.GetProject(), req.GetRoot())
	}

	var matched *AgentChoice
	for _, c := range asksHost.Choices() {
		if c.Agent == agentName && c.Profile == profile {
			cc := c
			matched = &cc
			break
		}
	}
	if matched == nil {
		return nil, fmt.Errorf("agent %s is not available on this node", agentLabel(agentName, profile))
	}
	if !matched.Askable {
		return nil, fmt.Errorf("agent %s cannot be asked: %s", agentLabel(agentName, profile), cmp.Or(matched.Reason, "not askable"))
	}

	// Every attempt counts toward the hourly limit, including ones waiting or refused.
	if !p.askRate.take() {
		return nil, fmt.Errorf("the plugin asked agents %d times in the last hour, its most", asksPerHour)
	}

	ask := &agentAsk{
		id:      "q" + nonce(),
		kind:    "ask",
		name:    proj.Name,
		project: req.GetProject(),
		title:   title,
		tag:     tag,
		since:   time.Now(),
		q: AgentQuestion{
			Agent:      agentName,
			Profile:    profile,
			Root:       resolvedRoot,
			Prompt:     prompt,
			Format:     format,
			JSONSchema: schema,
			Timeout:    timeout,
		},
	}

	always := slices.Contains(p.m.grant.get(p.id).AsksAllowed, askKey(agentName, profile))
	if always {
		if err := p.startAskJob(ask, false); err != nil {
			return nil, err
		}
		return &hostapi.AskAgentResponse{RequestId: ask.id}, nil
	}

	p.pendingMu.Lock()
	running := len(p.askJobs)
	p.asks.mu.Lock()
	if p.asks.ask != nil {
		p.asks.mu.Unlock()
		p.pendingMu.Unlock()
		return nil, errors.New("another agent the plugin asked waits for the user")
	}
	if running >= maxPluginAsks {
		p.asks.mu.Unlock()
		p.pendingMu.Unlock()
		return nil, fmt.Errorf("at most %d agent questions may run or wait at once", maxPluginAsks)
	}
	p.asks.ask = ask
	p.asks.mu.Unlock()
	p.pendingMu.Unlock()

	p.m.cfg.Log.Info("plugin asks to question an agent", "plugin", p.id, "agent", ask.q.Agent, "profile", ask.q.Profile, "root", ask.q.Root)
	p.status(func() {})
	return &hostapi.AskAgentResponse{RequestId: ask.id, Asked: true}, nil
}

func (h *host) AgentsCancelAsk(_ context.Context, req *hostapi.CancelAskRequest) (*hostapi.CancelAskResponse, error) {
	p := h.p
	id := req.GetRequestId()
	if id == "" {
		return &hostapi.CancelAskResponse{}, nil
	}
	if ask := p.asks.takeKind(id, "ask"); ask != nil {
		p.tellAskFailed(ask, "canceled", "the ask was canceled", 0)
		p.status(func() {})
		return &hostapi.CancelAskResponse{Found: true}, nil
	}
	p.pendingMu.Lock()
	cancel, ok := p.askJobs[id]
	p.pendingMu.Unlock()
	if ok {
		cancel()
		return &hostapi.CancelAskResponse{Found: true}, nil
	}
	return &hostapi.CancelAskResponse{}, nil
}

// projectRoot returns the project and resolved folder path; when root is "",
// the project's first folder is used.
func (m *Manager) projectRoot(id, root string) (ProjectRoots, string, bool) {
	if m.cfg.Projects == nil {
		return ProjectRoots{}, "", false
	}
	for _, pr := range m.cfg.Projects() {
		if pr.ID != id {
			continue
		}
		if root == "" {
			if len(pr.Roots) > 0 {
				return pr, pr.Roots[0], true
			}
			return ProjectRoots{}, "", false
		}
		if slices.Contains(pr.Roots, root) {
			return pr, root, true
		}
	}
	return ProjectRoots{}, "", false
}

// startAskJob reserves a running ask slot and launches runAsk in a goroutine.
// When notifyStarted is true (a waiting ask was approved by the user), an
// agentAskStarted event is queued strictly before runAsk can post its outcome.
func (p *plugin) startAskJob(ask *agentAsk, notifyStarted bool) error {
	p.pendingMu.Lock()
	waiting := 0
	if cur := p.asks.get(); cur != nil && cur.kind == "ask" {
		waiting = 1
	}
	if len(p.askJobs)+waiting >= maxPluginAsks {
		p.pendingMu.Unlock()
		return fmt.Errorf("at most %d agent questions may run or wait at once", maxPluginAsks)
	}
	jobCtx, cancel := context.WithTimeout(p.m.ctx, ask.q.Timeout)
	p.askJobs[ask.id] = cancel
	p.pendingMu.Unlock()

	if notifyStarted {
		p.tellAskStarted(ask)
	}
	go p.runAsk(ask, jobCtx, cancel)
	return nil
}

func (p *plugin) tellAskStarted(ask *agentAsk) {
	ev := &hostapi.SessionEvent{
		Kind:      EventAgentAskStarted,
		RequestId: ask.id,
		Tag:       ask.tag,
		Agent:     ask.q.Agent,
		Profile:   ask.q.Profile,
	}
	p.post(func() { p.deliver(ev, hostapi.PermAgentsAsk) })
}

func (p *plugin) delAskJob(id string) {
	p.pendingMu.Lock()
	delete(p.askJobs, id)
	p.pendingMu.Unlock()
}

func (p *plugin) runAsk(ask *agentAsk, ctx context.Context, cancel context.CancelFunc) {
	defer func() {
		cancel()
		p.delAskJob(ask.id)
	}()
	if state, _ := p.state(); (state != protocol.PluginReady && state != protocol.PluginRunning) || !p.hasGrant(hostapi.PermAgentsAsk) {
		p.tellAskFailed(ask, "canceled", "the plugin may not run now", 0)
		return
	}
	select {
	case <-p.done:
		return
	default:
	}
	start := time.Now()
	out, err := p.m.cfg.Asks.Ask(ctx, p.id, ask.q)
	elapsed := elapsedMillis(start)
	if err != nil {
		code := "failed"
		switch {
		case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
			code = "timeout"
		case errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled):
			code = "canceled"
		}
		p.m.cfg.Log.Warn("plugin's agent ask failed", "plugin", p.id, "agent", ask.q.Agent, "profile", ask.q.Profile, "code", code, "err", err)
		p.tellAskFailed(ask, code, err.Error(), elapsed)
		return
	}
	trimmed, truncated := cut(out, maxAskOutput)
	p.m.cfg.Log.Info("plugin asked an agent", "plugin", p.id, "agent", ask.q.Agent, "profile", ask.q.Profile, "root", ask.q.Root, "ms", elapsed)
	ev := &hostapi.SessionEvent{
		Kind:      EventAgentAnswered,
		RequestId: ask.id,
		Tag:       ask.tag,
		Output:    trimmed,
		Truncated: truncated,
		ElapsedMs: elapsed,
	}
	p.post(func() { p.deliver(ev, hostapi.PermAgentsAsk) })
}

func (p *plugin) tellAskFailed(ask *agentAsk, code, why string, elapsed uint32) {
	ev := &hostapi.SessionEvent{
		Kind:      EventAgentAskFailed,
		RequestId: ask.id,
		Tag:       ask.tag,
		Code:      code,
		Error:     why,
		ElapsedMs: elapsed,
	}
	p.post(func() { p.deliver(ev, hostapi.PermAgentsAsk) })
}

// cancelPending cancels all in-flight asynchronous decision, agent ask, and
// git run jobs and drops any waiting agent approval ask.
func (p *plugin) cancelPending(why string) {
	p.dropAsk(why)
	p.pendingMu.Lock()
	cancels := make([]context.CancelFunc, 0, len(p.askJobs)+len(p.decJobs)+len(p.gitJobs))
	for _, c := range p.askJobs {
		cancels = append(cancels, c)
	}
	for _, c := range p.decJobs {
		cancels = append(cancels, c)
	}
	for _, c := range p.gitJobs {
		cancels = append(cancels, c)
	}
	p.pendingMu.Unlock()
	for _, c := range cancels {
		c()
	}
}
