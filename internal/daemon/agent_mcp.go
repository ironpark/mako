package daemon

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agents"
	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
)

const agentMCPName = "mako"
const agentMCPArg = "agent-mcp"
const agentMCPTokenEnv = "MAKO_AGENT_MCP_TOKEN"

type projectAgents struct {
	mu       sync.Mutex
	socket   string
	peers    map[string]*agentPeer
	messages agentMessages
}

type agentPeer struct {
	id, token, parent string
	ctx               context.Context
	cancel            context.CancelFunc
	ready             chan struct{}
	session           *session.Session // guarded by projectAgents.mu
}

func (p *projectAgents) server(ctx context.Context, binary, id, parent string) (agent.Server, func()) {
	var secret [32]byte
	_, _ = rand.Read(secret[:])
	ctx, cancel := context.WithCancel(ctx)
	peer := &agentPeer{id: id, parent: parent, token: hex.EncodeToString(secret[:]), ctx: ctx, cancel: cancel, ready: make(chan struct{})}
	p.mu.Lock()
	if p.peers == nil {
		p.peers = map[string]*agentPeer{}
	}
	if old := p.peers[id]; old != nil {
		old.cancel()
	}
	p.peers[id] = peer
	p.mu.Unlock()
	return agent.Server{ID: agentMCPName, Command: binary, Args: []string{agentMCPArg, p.socket}, Env: map[string]string{"MAKO_SESSION": id, agentMCPTokenEnv: peer.token}}, func() {
		cancel()
		p.mu.Lock()
		if p.peers[id] == peer {
			delete(p.peers, id)
		}
		p.mu.Unlock()
	}
}

func (p *projectAgents) activate(s *session.Session) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if peer := p.peers[s.ID()]; peer != nil && peer.session == nil {
		peer.session = s
		close(peer.ready)
	}
}

func (p *projectAgents) peer(id string) *agentPeer {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.peers[id]
}

// The stdio child only bridges MCP bytes. Session identity is authenticated
// once on the private socket and cannot be supplied in a tool argument.
func runAgentMCP(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) != 1 || os.Getenv("MAKO_SESSION") == "" || os.Getenv(agentMCPTokenEnv) == "" {
		return errors.New("expected the session's MCP socket and credentials")
	}
	conn, err := net.DialTimeout("unix", args[0], 2*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(agentMCPHello{Session: os.Getenv("MAKO_SESSION"), Token: os.Getenv(agentMCPTokenEnv)}); err != nil {
		return err
	}
	go func() { _, _ = io.Copy(conn, stdin); _ = conn.Close() }()
	_, err = io.Copy(stdout, conn)
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

type agentMCPHello struct {
	Session string `json:"session"`
	Token   string `json:"token"`
}

func (d *Daemon) listenAgentMCP(ctx context.Context, dir string) error {
	if d.hookBinary == "" {
		return nil
	}
	path := filepath.Join(dir, "agents.sock")
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("agent MCP: %w", err)
	}
	d.projectAgents.socket = path
	context.AfterFunc(ctx, func() { _ = ln.Close(); _ = os.Remove(path) })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go d.serveAgentMCP(ctx, conn)
		}
	}()
	return nil
}

func (d *Daemon) serveAgentMCP(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	reader := bufio.NewReaderSize(conn, 4096)
	line, err := reader.ReadSlice('\n')
	if err != nil {
		return
	}
	var hello agentMCPHello
	if json.Unmarshal(line, &hello) != nil {
		return
	}
	peer := d.projectAgents.peer(hello.Session)
	if peer == nil || subtle.ConstantTimeCompare([]byte(peer.token), []byte(hello.Token)) != 1 {
		return
	}
	stopPeer := context.AfterFunc(peer.ctx, func() { _ = conn.Close() })
	defer stopPeer()
	select {
	case <-peer.ready:
	case <-peer.ctx.Done():
		return
	case <-ctx.Done():
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	server := d.projectAgentServer(peer)
	ss, err := server.Connect(peer.ctx, &sdk.IOTransport{Reader: io.NopCloser(reader), Writer: conn, MaxLineLength: 1 << 20}, nil)
	if err != nil {
		return
	}
	defer ss.Close()
	_ = ss.Wait()
}

type agentSummary struct {
	ID        string `json:"id"`
	Agent     string `json:"agent"`
	Profile   string `json:"profile,omitempty"`
	Name      string `json:"name,omitempty"`
	ParentID  string `json:"parent_session_id,omitempty"`
	State     string `json:"state"`
	Status    string `json:"status,omitempty"`
	Messaging bool   `json:"messaging"`
}

type availableAgent struct {
	Name      string             `json:"name"`
	Spawnable bool               `json:"spawnable" jsonschema:"Whether this agent can be spawned without a profile."`
	Profiles  []availableProfile `json:"profiles"`
}
type availableProfile struct {
	Name        string `json:"name"`
	Description string `json:"description" jsonschema:"Specialties and guidance on when to choose this profile."`
	Spawnable   bool   `json:"spawnable" jsonschema:"Whether this profile enables the Mako MCP tools required for spawning."`
}
type listAgentsResult struct {
	SelfID    string           `json:"self_id"`
	Project   string           `json:"project"`
	Agents    []agentSummary   `json:"agents"`
	Available []availableAgent `json:"available"`
}
type spawnAgentArgs struct {
	Agent   string `json:"agent,omitempty" jsonschema:"Agent to launch; defaults to this session's agent. Use list_agents for choices."`
	Profile string `json:"profile,omitempty" jsonschema:"Exact profile name from list_agents. Choose a spawnable profile whose description fits the task, and set agent to its owning agent name. Omit for the agent's default settings."`
	Name    string `json:"name,omitempty"`
	Prompt  string `json:"prompt" jsonschema:"The task for the new agent. It starts in this project's directory."`
}
type sendMessageArgs struct {
	To   string `json:"to"`
	Text string `json:"text"`
}
type readMessagesArgs struct {
	AfterID     uint64 `json:"after_id,omitempty" jsonschema:"Return messages newer than this cursor. Reading does not consume messages."`
	AckThrough  uint64 `json:"ack_through,omitempty" jsonschema:"Acknowledge messages through this ID from a previous successful read."`
	Limit       int    `json:"limit,omitempty" jsonschema:"Maximum messages, 1 to 100; defaults to 50."`
	WaitSeconds int    `json:"wait_seconds,omitempty" jsonschema:"Wait up to 30 seconds for a message when the inbox is empty."`
}
type readMessagesResult struct {
	Messages    []agentMessage `json:"messages"`
	NextAfterID uint64         `json:"next_after_id"`
}
type stopAgentArgs struct {
	SessionID string `json:"session_id"`
}
type stopAgentResult struct {
	Stopped bool `json:"stopped"`
}

func (d *Daemon) projectAgentServer(peer *agentPeer) *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{Name: "mako", Version: config.Version}, &sdk.ServerOptions{
		Logger:       d.log,
		Instructions: fmt.Sprintf("You are Mako session %s in project %s. Parent session: %s. Before delegating, use list_agents to inspect available agents and their profile descriptions. Choose a spawnable profile whose specialties match the task, then pass its owning agent name and exact profile name to spawn_agent. If no profile is needed, omit profile and use a spawnable agent default. Use send_message to communicate and read_messages to receive or wait for messages. Messages are durable mailboxes; they do not interrupt the recipient's terminal. Report results to the parent with send_message. After processing a batch, acknowledge its IDs using read_messages ack_through. stop_agent only stops agents you spawned.", peer.id, peer.session.Info().GetProject(), peer.parent),
	})
	sdk.AddTool(server, &sdk.Tool{Name: "list_agents", Description: "List sessions in this project, their IDs, status and messaging support, plus available agent types and profiles with descriptions and spawnable flags. Read the profile descriptions to select the best match for a task before calling spawn_agent. Only spawnable choices can receive the required Mako MCP tools.", Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, _ *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, listAgentsResult, error) {
		out, err := d.listProjectAgents(peer)
		return nil, out, err
	})
	sdk.AddTool(server, &sdk.Tool{Name: "spawn_agent", Description: "Start a new Mako agent session in the current project with a task. First inspect list_agents and choose a spawnable profile by its description; supply its agent and profile names. The session is visible in Mako and has its own MCP mailbox. Returns its session ID; use send_message/read_messages to coordinate."}, func(ctx context.Context, _ *sdk.CallToolRequest, in spawnAgentArgs) (*sdk.CallToolResult, agentSummary, error) {
		out, err := d.spawnProjectAgent(ctx, peer, in)
		return nil, out, err
	})
	sdk.AddTool(server, &sdk.Tool{Name: "send_message", Description: "Store a message in another active agent's inbox in this project. The recipient receives it with read_messages. Does not inject terminal input or interrupt work."}, func(ctx context.Context, _ *sdk.CallToolRequest, in sendMessageArgs) (*sdk.CallToolResult, agentMessage, error) {
		if _, err := d.projectCaller(peer); err != nil {
			return nil, agentMessage{}, err
		}
		if strings.TrimSpace(in.Text) == "" || len(in.Text) > maxAgentMessage {
			return nil, agentMessage{}, fmt.Errorf("text must be non-empty and at most %d bytes", maxAgentMessage)
		}
		if _, err := d.projectTarget(peer, in.To); err != nil {
			return nil, agentMessage{}, err
		}
		msg, err := d.projectAgents.messages.send(peer.id, in.To, in.Text, projectScope(peer.session.Info()))
		return nil, msg, err
	})
	sdk.AddTool(server, &sdk.Tool{Name: "read_messages", Description: "Read your own inbox, optionally waiting for messages. Use after_id for pagination. Messages remain available across reconnects and daemon restarts until you acknowledge them with ack_through."}, func(ctx context.Context, _ *sdk.CallToolRequest, in readMessagesArgs) (*sdk.CallToolResult, readMessagesResult, error) {
		if _, err := d.projectCaller(peer); err != nil {
			return nil, readMessagesResult{}, err
		}
		if in.Limit == 0 {
			in.Limit = 50
		}
		if in.Limit < 1 || in.Limit > 100 || in.WaitSeconds < 0 || in.WaitSeconds > 30 {
			return nil, readMessagesResult{}, errors.New("limit must be 1..100 and wait_seconds 0..30")
		}
		messages, err := d.projectAgents.messages.read(ctx, peer.id, in.AfterID, in.AckThrough, in.Limit, time.Duration(in.WaitSeconds)*time.Second)
		next := max(in.AfterID, in.AckThrough)
		if len(messages) > 0 {
			next = messages[len(messages)-1].ID
		}
		return nil, readMessagesResult{Messages: messages, NextAfterID: next}, err
	})
	sdk.AddTool(server, &sdk.Tool{Name: "stop_agent", Description: "Terminate and remove an agent session spawned directly by you. Other project sessions cannot be stopped with this tool."}, func(ctx context.Context, _ *sdk.CallToolRequest, in stopAgentArgs) (*sdk.CallToolResult, stopAgentResult, error) {
		if _, err := d.projectCaller(peer); err != nil {
			return nil, stopAgentResult{}, err
		}
		d.sessionMu.Lock()
		saved, exists := d.saved[in.SessionID]
		owned := exists && saved.ParentID == peer.id && projectScope(saved.Info) == projectScope(peer.session.Info())
		d.sessionMu.Unlock()
		if !owned {
			return nil, stopAgentResult{}, errors.New("only agents you spawned can be stopped")
		}
		err := d.closeSession(in.SessionID)
		return nil, stopAgentResult{Stopped: err == nil}, err
	})
	return server
}

func (d *Daemon) projectCaller(peer *agentPeer) (*session.Session, error) {
	d.projectAgents.mu.Lock()
	s := peer.session
	current := d.projectAgents.peers[peer.id] == peer
	d.projectAgents.mu.Unlock()
	if !current || peer.ctx.Err() != nil || s == nil || s.Info().GetState() != protocol.StateRunning {
		return nil, errors.New("the calling session is no longer running")
	}
	return s, nil
}

func projectScope(info *protocol.SessionInfo) string {
	path := info.GetProject()
	if info.GetWorktree().GetRepo() != "" {
		path = info.GetWorktree().GetRepo()
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	return filepath.Clean(path)
}

func (d *Daemon) projectTarget(caller *agentPeer, id string) (*session.Session, error) {
	target := d.projectAgents.peer(id)
	if target == nil {
		return nil, errors.New("recipient is not an active messaging session in this project")
	}
	s, err := d.projectCaller(target)
	if err != nil || projectScope(s.Info()) != projectScope(caller.session.Info()) {
		return nil, errors.New("recipient is not an active messaging session in this project")
	}
	return s, nil
}

func (d *Daemon) summarizeAgent(info *protocol.SessionInfo, parent string) agentSummary {
	peer := d.projectAgents.peer(info.GetId())
	return agentSummary{ID: info.GetId(), Agent: info.GetAgent(), Profile: info.GetProfile(), Name: info.GetName(), State: info.GetState(), Status: info.GetStatus(), ParentID: parent, Messaging: peer != nil && peer.ctx.Err() == nil && info.GetState() == protocol.StateRunning}
}

func (d *Daemon) listProjectAgents(peer *agentPeer) (listAgentsResult, error) {
	s, err := d.projectCaller(peer)
	if err != nil {
		return listAgentsResult{}, err
	}
	info := s.Info()
	out := listAgentsResult{SelfID: peer.id, Project: info.GetProject(), Agents: []agentSummary{}, Available: []availableAgent{}}
	d.sessionMu.Lock()
	parents := map[string]string{}
	for id, saved := range d.saved {
		parents[id] = saved.ParentID
	}
	d.sessionMu.Unlock()
	for _, other := range d.listSessions() {
		if projectScope(other) == projectScope(info) {
			out.Agents = append(out.Agents, d.summarizeAgent(other, parents[other.GetId()]))
		}
	}
	configured := d.agents()
	// What an agent may spawn: those this node offers, installed or on.
	for _, name := range agentspec.Offered(configured) {
		a, _ := agentspec.Lookup(name, configured)
		if len(a.Command) == 0 {
			continue
		}
		driver, ok := agents.ByProgram(a.Command[0])
		if !ok {
			continue
		}
		if _, ok := driver.Driver.(agent.Servers); !ok {
			continue
		}
		if _, ok := driver.Driver.(agent.Prompter); !ok {
			continue
		}
		available := availableAgent{Name: name, Spawnable: d.agentMCPEnabled(nil), Profiles: []availableProfile{}}
		for _, p := range a.Profiles {
			available.Profiles = append(available.Profiles, availableProfile{
				Name: p.Name, Description: p.Description, Spawnable: d.agentMCPEnabled(p.ContextServers),
			})
		}
		out.Available = append(out.Available, available)
	}
	return out, nil
}

func (d *Daemon) spawnProjectAgent(ctx context.Context, peer *agentPeer, in spawnAgentArgs) (agentSummary, error) {
	caller, err := d.projectCaller(peer)
	if err != nil {
		return agentSummary{}, err
	}
	if strings.TrimSpace(in.Prompt) == "" || len(in.Prompt) > 64<<10 {
		return agentSummary{}, errors.New("prompt must be non-empty and at most 65536 bytes")
	}
	in.Name = strings.Join(strings.Fields(in.Name), " ")
	if len(in.Name) > maxSessionName {
		return agentSummary{}, fmt.Errorf("name must be at most %d bytes", maxSessionName)
	}
	info := caller.Info()
	if in.Agent == "" {
		in.Agent = info.GetAgent()
	}
	configured, ok := agentspec.Lookup(in.Agent, d.agents())
	if !ok || len(configured.Command) == 0 {
		return agentSummary{}, errors.New("unknown agent; use list_agents for available agents")
	}
	driver, ok := agents.ByProgram(configured.Command[0])
	if !ok {
		return agentSummary{}, errors.New("this agent cannot receive Mako MCP tools")
	}
	if _, ok := driver.Driver.(agent.Servers); !ok {
		return agentSummary{}, errors.New("this agent cannot receive Mako MCP tools")
	}
	var picks *[]string
	if in.Profile != "" {
		profile, ok := configured.Profile(in.Profile)
		if !ok {
			return agentSummary{}, errors.New("unknown agent profile")
		}
		picks = profile.ContextServers
	}
	if !d.agentMCPEnabled(picks) {
		return agentSummary{}, errors.New("the mako MCP server is disabled for this profile")
	}
	if err := ctx.Err(); err != nil {
		return agentSummary{}, err
	}
	prompt := fmt.Sprintf("[Mako project task: parent session %s. Use the mako MCP tools to coordinate and send_message to report results to the parent.]\n\n%s", peer.id, in.Prompt)
	prompter, ok := driver.Driver.(agent.Prompter)
	if !ok {
		return agentSummary{}, errors.New("this agent has no interactive startup prompt")
	}
	child, err := d.startSessionAs(ctx, newSessionID(), &protocol.OpenSession{Agent: in.Agent, Profile: in.Profile, Project: info.GetProject(), Worktree: info.GetWorktree(), Args: prompter.PromptArgs(prompt)}, peer.id)
	if err != nil {
		return agentSummary{}, err
	}
	if d.projectAgents.peer(child.ID()) == nil {
		_ = d.closeSession(child.ID())
		return agentSummary{}, errors.New("the new agent could not be configured with MCP")
	}
	if in.Name != "" {
		if _, err := d.renameSession(child.ID(), in.Name); err != nil {
			return agentSummary{}, fmt.Errorf("agent %s started but could not be named: %w", child.ID(), err)
		}
	}
	return d.summarizeAgent(child.Info(), peer.id), nil
}

// Project agents are sessions of their own. Keep the parent's aggregate
// activity in step with their turns, rather than their process lifetime.
func (d *Daemon) followProjectAgent(child *session.Session, parentID string) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		if d.reportProjectAgent(child, parentID) {
			return
		}
		select {
		case <-child.Done():
		case <-tick.C:
		case <-d.ctx.Done():
			return
		}
	}
}

func (d *Daemon) reportProjectAgent(child *session.Session, parentID string) bool {
	info := child.Info()
	running := info.GetState() == protocol.StateRunning && info.GetStatus() != protocol.AgentDone && info.GetStatus() != protocol.AgentIdle
	if parent, ok := d.sessions.get(parentID); ok {
		parent.ReportSubagent("", child.ID(), running, time.Now().UnixNano())
	}
	return info.GetState() == protocol.StateExited
}
