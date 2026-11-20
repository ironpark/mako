package daemon

import (
	"bufio"
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/agents"
	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/layoutdoc"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
	"github.com/ironpark/mako/internal/transport"
)

const agentMCPName = "mako"
const agentMCPArg = "agent-mcp"
const agentMCPTokenEnv = "MAKO_AGENT_MCP_TOKEN"

type projectAgents struct {
	syncMu     sync.Mutex // serializes live MCP tool and file sync across peers
	mu         sync.Mutex
	socket     string // the stdio bridge's, for an agent that cannot reach url
	cliSocket  string // agent-cli.sock for mako-msg CLI & agy-sidecar
	url        string // where the agents reach the server over HTTP
	peers      map[string]*agentPeer
	messages   agentMessages
	waits      map[string]int // the agents waited on with wait_agent, how many times each
	agySidecar *agySidecarClient
}

type agentPeer struct {
	id, token, parent string
	ctx               context.Context
	cancel            context.CancelFunc
	ready             chan struct{}
	session           *session.Session                             // guarded by projectAgents.mu
	http              http.Handler                                 // its sessions over HTTP, made at the first; guarded by projectAgents.mu
	httpServer        *sdk.Server                                  // backing server for http; guarded by projectAgents.mu
	servers           map[*sdk.Server]string                       // live SDK servers -> last synced signature or "disabled"; guarded by projectAgents.mu
	refresh           func(context.Context, *protocol.SessionInfo) // rewrites per-session MCP files on config change; guarded by projectAgents.mu
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
	server := agent.Server{ID: agentMCPName, URL: p.url, BearerToken: peer.token}
	if p.socket != "" && binary != "" {
		server.Command, server.Args = binary, []string{agentMCPArg, p.socket}
		server.Env = map[string]string{"MAKO_SESSION": id, agentMCPTokenEnv: peer.token}
	}
	return server, func() {
		cancel()
		p.mu.Lock()
		if p.peers[id] == peer {
			delete(p.peers, id)
		}
		p.mu.Unlock()
	}
}

// activate gives s's peer its session, returning the peer, nil when s
// serves no MCP or already had it.
func (p *projectAgents) activate(s *session.Session) *agentPeer {
	p.mu.Lock()
	defer p.mu.Unlock()
	if peer := p.peers[s.ID()]; peer != nil && peer.session == nil {
		peer.session = s
		close(peer.ready)
		return peer
	}
	return nil
}

func (p *projectAgents) peer(id string) *agentPeer {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.peers[id]
}

func (p *projectAgents) setRefresh(id string, fn func(context.Context, *protocol.SessionInfo)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if peer := p.peers[id]; peer != nil && peer.ctx.Err() == nil {
		peer.refresh = fn
	}
}

func (p *projectAgents) trackServer(peer *agentPeer, srv *sdk.Server, sig string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if peer.servers == nil {
		peer.servers = map[*sdk.Server]string{}
	}
	peer.servers[srv] = sig
}

func (p *projectAgents) untrackServer(peer *agentPeer, srv *sdk.Server) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(peer.servers, srv)
}

// byToken is the live session whose token is token, nil for none.
func (p *projectAgents) byToken(token string) *agentPeer {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, peer := range p.peers {
		if subtle.ConstantTimeCompare([]byte(peer.token), []byte(token)) == 1 && peer.ctx.Err() == nil {
			return peer
		}
	}
	return nil
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

// listenAgentMCP serves the agents' MCP server over HTTP on loopback,
// which Claude Code and Codex reach without a process of their own, and,
// where this program can run as the bridge, on agents.sock, for agents
// that run their servers.
func (d *Daemon) listenAgentMCP(ctx context.Context, dir string) error {
	if err := d.listenAgentMCPHTTP(ctx); err != nil {
		return err
	}
	if err := d.listenAgentCLI(ctx, dir); err != nil {
		d.log.Warn("agent CLI: not listening", "err", err)
	}
	if d.hookBinary == "" {
		return nil
	}
	path := filepath.Join(dir, "agents.sock")
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("agent MCP: %w", err)
	}
	ln = transport.OwnListener(ln, path)
	d.projectAgents.socket = path
	context.AfterFunc(ctx, func() { _ = ln.Close() })
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

// agentMCPPath is where the agents' MCP server answers over HTTP.
const agentMCPPath = "/mcp"

func (d *Daemon) listenAgentMCPHTTP(ctx context.Context) error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("agent MCP over HTTP: %w", err)
	}
	mux := http.NewServeMux()
	mux.Handle(agentMCPPath, http.HandlerFunc(d.serveAgentMCPHTTP))
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	d.projectAgents.url = "http://" + ln.Addr().String() + agentMCPPath
	context.AfterFunc(ctx, func() { _ = srv.Close() })
	go func() { _ = srv.Serve(ln) }()
	return nil
}

// serveAgentMCPHTTP answers a request of a session's agent, which its
// bearer token names: every request, not only the first, so that one
// session's MCP session cannot be reached with another's token. Each
// session has a handler of its own, and its MCP sessions go with it.
func (d *Daemon) serveAgentMCPHTTP(w http.ResponseWriter, r *http.Request) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	peer := d.projectAgents.byToken(token)
	if !ok || peer == nil {
		http.Error(w, "unknown session", http.StatusUnauthorized)
		return
	}
	select {
	case <-peer.ready:
	case <-peer.ctx.Done():
		http.Error(w, "session ended", http.StatusUnauthorized)
		return
	case <-r.Context().Done():
		return
	}
	if _, err := d.peerRunningSession(peer); err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	h, err := d.ensureHTTPPeerHandler(peer)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	h.ServeHTTP(w, r)
}

func (d *Daemon) ensureHTTPPeerHandler(peer *agentPeer) (http.Handler, error) {
	s, err := d.peerRunningSession(peer)
	if err != nil {
		return nil, err
	}
	snap := d.agentMCPSnapshot()
	enabled := d.sessionAgentMCPEnabled(s.Info())

	d.projectAgents.syncMu.Lock()
	defer d.projectAgents.syncMu.Unlock()

	d.projectAgents.mu.Lock()
	h := peer.http
	srv := peer.httpServer
	d.projectAgents.mu.Unlock()

	if h != nil {
		d.syncPeerServerLocked(peer, srv, snap, enabled)
		return h, nil
	}
	srv, sig := d.newProjectAgentServer(peer, snap, enabled)
	h = sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv }, &sdk.StreamableHTTPOptions{SessionTimeout: agentMCPIdle})
	// The session's end ends its MCP sessions, as it closes a bridge's.
	context.AfterFunc(peer.ctx, func() {
		for ss := range srv.Sessions() {
			_ = ss.Close()
		}
	})
	d.projectAgents.mu.Lock()
	if peer.http == nil {
		peer.http = h
		peer.httpServer = srv
		if peer.servers == nil {
			peer.servers = map[*sdk.Server]string{}
		}
		peer.servers[srv] = sig
	} else {
		h = peer.http
	}
	d.projectAgents.mu.Unlock()
	return h, nil
}

// agentMCPIdle is how long an MCP session over HTTP lasts unused: an agent
// that went away without saying so leaves it behind.
const agentMCPIdle = 30 * time.Minute

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
	s, err := d.peerRunningSession(peer)
	if err != nil {
		return
	}
	snap := d.agentMCPSnapshot()
	enabled := d.sessionAgentMCPEnabled(s.Info())
	_ = conn.SetReadDeadline(time.Time{})

	d.projectAgents.syncMu.Lock()
	server, sig := d.newProjectAgentServer(peer, snap, enabled)
	d.projectAgents.trackServer(peer, server, sig)
	d.projectAgents.syncMu.Unlock()
	defer d.projectAgents.untrackServer(peer, server)

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
	// Folder is where it works: in a project of several folders, which
	// of them.
	Folder string `json:"folder,omitempty"`
	// Node names the node it is on when it is another's: an agent of the
	// project there.
	Node string `json:"node,omitempty"`
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
type spawnablePreset struct {
	Agent       string `json:"agent" jsonschema:"Agent name to pass to spawn_agent."`
	Profile     string `json:"profile,omitempty" jsonschema:"Profile name to pass to spawn_agent; omitted for the agent's default configuration."`
	Target      string `json:"target" jsonschema:"Target identifier: 'agent' for default or 'agent:profile' for a named profile."`
	Description string `json:"description,omitempty" jsonschema:"Specialties and guidance on when to choose this preset."`
	Model       string `json:"model,omitempty" jsonschema:"Configured model for this preset, if set."`
	Effort      string `json:"effort,omitempty" jsonschema:"Configured reasoning effort for this preset, if set."`
	Yolo        bool   `json:"yolo,omitempty" jsonschema:"Whether YOLO (auto-approve permissions) mode is enabled for this preset."`
}
type listAgentProfilesResult struct {
	Presets []spawnablePreset `json:"presets" jsonschema:"Spawnable agent configurations and profile presets ready to pass to spawn_agent."`
	Agents  []availableAgent  `json:"agents" jsonschema:"Spawnable agents grouped with their spawnable profiles."`
}
type listAgentsResult struct {
	SelfID  string `json:"self_id"`
	Project string `json:"project"`
	// ProjectName names the project of several folders the session is in,
	// whose agents in every folder it lists.
	ProjectName string           `json:"project_name,omitempty"`
	Agents      []agentSummary   `json:"agents"`
	Available   []availableAgent `json:"available"`
}
type spawnAgentArgs struct {
	Agent   string `json:"agent,omitempty" jsonschema:"Agent to launch; defaults to this session's agent. Use list_agent_profiles for spawnable choices."`
	Profile string `json:"profile,omitempty" jsonschema:"Exact profile name from list_agent_profiles. Choose a spawnable profile whose description fits the task, and set agent to its owning agent name. Omit for the agent's default settings."`
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
	WaitSeconds int    `json:"wait_seconds,omitempty" jsonschema:"Optional short wait (0 to 120 seconds); prefer ending your turn or running 'mako-msg wait' in the background instead of polling."`
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

const agentMCPSigDisabled = "disabled"

var agentMCPToolNames = []string{
	"list_agents", "list_agent_profiles", "spawn_agent", "send_message", "read_messages", "wait_agent", "stop_agent",
	toolListBlocks, toolGetLayout, toolSetLayout, toolPatchLayout, toolUndoLayout,
	toolShowUI, toolUpdateUI, toolCloseUI, toolSaveTemplate,
}

type agentMCPSnapshot struct {
	available             []availableAgent
	presets               []spawnablePreset
	blocks                []layoutdoc.Block
	listAgentsDesc        string
	listAgentProfilesDesc string
	spawnAgentDesc        string
	listBlocksDesc        string
	signature             string
}

func (d *Daemon) agentMCPSnapshot() agentMCPSnapshot {
	available := d.availableAgents()
	presets := d.spawnablePresets()
	blocks := d.blocks()
	sigPayload, _ := json.Marshal(struct {
		Available []availableAgent  `json:"available"`
		Presets   []spawnablePreset `json:"presets"`
		Blocks    []layoutdoc.Block `json:"blocks"`
	}{available, presets, blocks})
	sum := sha256.Sum256(sigPayload)
	agentSummaryText := summarizeAvailableForSchema(available)
	listAgentsDesc := "List active and saved agent sessions in this project, on this node and, for a project of several folders, on other nodes, with their IDs, status and messaging support, plus available agent types and profiles."
	listAgentProfilesDesc := "List only the spawnable agent configurations and profile presets available on this node, with each preset's target, description, model, effort and YOLO setting. Call this before spawn_agent to pick the best configuration for a task."
	spawnAgentDesc := "Start a new Mako agent session in the current project with a task. First inspect list_agent_profiles and choose a spawnable preset by its description and settings; supply its agent and profile names. The session is visible in Mako and has its own MCP mailbox. Returns its session ID; use send_message/read_messages to coordinate."
	if agentSummaryText != "" {
		listAgentsDesc += " Current agents: " + agentSummaryText
		spawnAgentDesc += " Current choices: " + agentSummaryText
	}
	if presetSummaryText := summarizePresetsForSchema(presets); presetSummaryText != "" {
		listAgentProfilesDesc += " Current presets: " + presetSummaryText
	}
	listBlocksDesc := "List the blocks a window layout document can place, with each one's props as a JSON Schema: the catalog to build a document from. A document's leaves are these blocks and the panes, which hold the session tabs."
	if blocksSummaryText := summarizeBlocksForSchema(blocks); blocksSummaryText != "" {
		listBlocksDesc += " Current blocks: " + blocksSummaryText
	}
	return agentMCPSnapshot{
		available:             available,
		presets:               presets,
		blocks:                blocks,
		listAgentsDesc:        listAgentsDesc,
		listAgentProfilesDesc: listAgentProfilesDesc,
		spawnAgentDesc:        spawnAgentDesc,
		listBlocksDesc:        listBlocksDesc,
		signature:             hex.EncodeToString(sum[:8]),
	}
}

func summarizePresetsForSchema(presets []spawnablePreset) string {
	var parts []string
	for _, p := range presets {
		var meta []string
		if p.Model != "" {
			meta = append(meta, "model="+p.Model)
		}
		if p.Effort != "" {
			meta = append(meta, "effort="+p.Effort)
		}
		if p.Yolo {
			meta = append(meta, "yolo")
		}
		entry := p.Target
		if len(meta) > 0 {
			entry += "(" + strings.Join(meta, ",") + ")"
		}
		if desc := strings.Join(strings.Fields(p.Description), " "); desc != "" {
			entry += ": " + clipRunes(desc, 60)
		}
		parts = append(parts, entry)
	}
	return clipRunes(strings.Join(parts, "; "), 1200)
}

func summarizeAvailableForSchema(list []availableAgent) string {
	var parts []string
	for _, a := range list {
		state := "disabled"
		if a.Spawnable {
			state = "spawnable"
		}
		entry := a.Name + "(" + state + ")"
		if len(a.Profiles) > 0 {
			var profiles []string
			for _, p := range a.Profiles {
				pState := "disabled"
				if p.Spawnable {
					pState = "spawnable"
				}
				pText := p.Name + "(" + pState + ")"
				if desc := strings.Join(strings.Fields(p.Description), " "); desc != "" {
					pText += ": " + clipRunes(desc, 60)
				}
				profiles = append(profiles, pText)
			}
			entry += "[" + strings.Join(profiles, "; ") + "]"
		}
		parts = append(parts, entry)
	}
	return clipRunes(strings.Join(parts, "; "), 1200)
}

func summarizeBlocksForSchema(blocks []layoutdoc.Block) string {
	names := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if b.Name != "" {
			names = append(names, b.Name)
		}
	}
	return clipRunes(strings.Join(names, ", "), 600)
}

func clipRunes(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes-1]) + "…"
}

func (d *Daemon) agentInstructions(peer *agentPeer) string {
	info := peer.session.Info()
	mode := "main"
	if peer.parent != "" {
		mode = "subagent"
	}
	var waitGuidance string
	switch info.GetAgent() {
	case "agy":
		waitGuidance = "Incoming Mako messages are pushed directly into your Antigravity conversation via agentapi/sidecar and wake you automatically. NEVER poll read_messages in a timeout loop: after spawning or messaging a teammate, continue other work or end your turn and you will be woken up when a reply arrives. You can also use `mako-msg wait` in a background shell command if you need to stay in-turn."
	default:
		waitGuidance = "NEVER poll read_messages in a repeated timeout loop. Messages are automatically delivered to you as a new prompt once your turn ends. If you want to wait asynchronously without blocking or timing out, run `mako-msg wait` (or `mako-msg wait --session <child-id>`) as a background shell command (e.g. Bash with run_in_background=true); it blocks event-driven with zero polling and exits with the message JSON as soon as a reply arrives or the child finishes. Use read_messages (wait_seconds=0) only for a quick non-blocking inbox check, and acknowledge processed messages with ack_through."
	}
	return fmt.Sprintf("You are Mako session %s (mode=%s, AGENT_MAKO_SESSION=%s, AGENT_MAKO_MODE=%s) in project %s. Parent session: %s. Before delegating, use list_agent_profiles to inspect spawnable agent configurations and profile presets (with descriptions, models, efforts and YOLO settings), or list_agents to inspect active project sessions. Choose a spawnable preset whose specialties match the task, then pass its agent and profile names to spawn_agent (omit profile for an agent default). Use send_message to communicate, with agents of this project on other nodes too, which list_agents lists with their node. %s Report results to the parent with send_message. Of an agent you spawned that waits on the user, or exits without reporting, you hear in a Mako note. stop_agent only stops agents you spawned. The mako window's layout is a document you read with get_layout and list_blocks; set_layout, patch_layout and undo_layout change it once the user allows, and only what the user keeps stays. show_ui shows a block of UI of yours in the window, a form say, and the user's taps on it come back to you as messages from the window; update_ui and close_ui change and close it. A UI to keep is a plugin: list_blocks names the folder to write one into.",
		peer.id, mode, peer.id, mode, info.GetProject(), peer.parent, waitGuidance)
}

func (d *Daemon) newProjectAgentServer(peer *agentPeer, snap agentMCPSnapshot, enabled bool) (*sdk.Server, string) {
	server := sdk.NewServer(&sdk.Implementation{Name: "mako", Version: config.Version}, &sdk.ServerOptions{
		Logger: d.log,
		Capabilities: &sdk.ServerCapabilities{
			Logging: &sdk.LoggingCapabilities{},
			Tools:   &sdk.ToolCapabilities{ListChanged: true},
		},
		SetCacheable: func(_ context.Context, _ sdk.Request, c *sdk.Cacheable) { c.TTLMs = 0 },
		Instructions: d.agentInstructions(peer),
	})
	if !enabled {
		return server, agentMCPSigDisabled
	}
	d.registerProjectAgentTools(server, peer, snap)
	return server, snap.signature
}

func (d *Daemon) addDynamicAgentTools(server *sdk.Server, peer *agentPeer, snap agentMCPSnapshot) {
	sdk.AddTool(server, &sdk.Tool{Name: "list_agents", Description: snap.listAgentsDesc, Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, _ *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, listAgentsResult, error) {
		out, err := d.listProjectAgents(peer)
		return nil, out, err
	})
	sdk.AddTool(server, &sdk.Tool{Name: "list_agent_profiles", Description: snap.listAgentProfilesDesc, Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, _ *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, listAgentProfilesResult, error) {
		out, err := d.listAgentProfiles(peer)
		return nil, out, err
	})
	sdk.AddTool(server, &sdk.Tool{Name: "spawn_agent", Description: snap.spawnAgentDesc}, func(ctx context.Context, _ *sdk.CallToolRequest, in spawnAgentArgs) (*sdk.CallToolResult, agentSummary, error) {
		out, err := d.spawnProjectAgent(ctx, peer, in)
		return nil, out, err
	})
	d.addListBlocksTool(server, peer, snap.listBlocksDesc)
}

func (d *Daemon) registerProjectAgentTools(server *sdk.Server, peer *agentPeer, snap agentMCPSnapshot) {
	d.addDynamicAgentTools(server, peer, snap)
	sdk.AddTool(server, &sdk.Tool{Name: "send_message", Description: "Send a message to another active agent in this project, on this node or another. It is typed into the recipient's terminal as a new prompt once that agent waits at its prompt, never interrupting a turn; until then it waits in the recipient's inbox, which read_messages reads."}, func(ctx context.Context, _ *sdk.CallToolRequest, in sendMessageArgs) (*sdk.CallToolResult, agentMessage, error) {
		if _, err := d.projectCaller(peer); err != nil {
			return nil, agentMessage{}, err
		}
		if err := checkAgentText(in.Text); err != nil {
			return nil, agentMessage{}, err
		}
		msg, err := d.sendAgentMessage(ctx, peer, in.To, in.Text)
		return nil, msg, err
	})
	sdk.AddTool(server, &sdk.Tool{Name: "read_messages", Description: "Read your own inbox. Prefer ending your turn (messages are pushed automatically) or running `mako-msg wait` in a background shell command instead of polling read_messages in a timeout loop. Use after_id for pagination and ack_through to acknowledge processed messages."}, func(ctx context.Context, _ *sdk.CallToolRequest, in readMessagesArgs) (*sdk.CallToolResult, readMessagesResult, error) {
		if _, err := d.projectCaller(peer); err != nil {
			return nil, readMessagesResult{}, err
		}
		if in.Limit == 0 {
			in.Limit = 50
		}
		if in.Limit < 1 || in.Limit > 100 || in.WaitSeconds < 0 || in.WaitSeconds > 120 {
			return nil, readMessagesResult{}, errors.New("limit must be 1..100 and wait_seconds 0..120")
		}
		messages, err := d.projectAgents.messages.read(ctx, peer.id, in.AfterID, in.AckThrough, in.Limit, time.Duration(in.WaitSeconds)*time.Second)
		next := max(in.AfterID, in.AckThrough)
		if len(messages) > 0 {
			next = messages[len(messages)-1].ID
		}
		return nil, readMessagesResult{Messages: messages, NextAfterID: next}, err
	})
	sdk.AddTool(server, &sdk.Tool{Name: "wait_agent", Description: "Wait until an agent of this project, on any node, ends its turn, or, with until=output, one of this node prints a text; the wait ends early when it waits on the user, exits, or a message reaches you. Right after send_message to it, this waits for the turn the message starts. A timeout is a check-in: wait again while it works.", Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, _ *sdk.CallToolRequest, in waitAgentArgs) (*sdk.CallToolResult, waitAgentResult, error) {
		out, err := d.waitProjectAgent(ctx, peer, in)
		return nil, out, err
	})
	sdk.AddTool(server, &sdk.Tool{Name: "stop_agent", Description: "Terminate and remove an agent session spawned directly by you. Other project sessions cannot be stopped with this tool."}, func(ctx context.Context, _ *sdk.CallToolRequest, in stopAgentArgs) (*sdk.CallToolResult, stopAgentResult, error) {
		if _, err := d.projectCaller(peer); err != nil {
			return nil, stopAgentResult{}, err
		}
		d.sessionMu.Lock()
		saved, exists := d.saved[in.SessionID]
		d.sessionMu.Unlock()
		// Scoping reads the folders, off the lock.
		owned := exists && saved.ParentID == peer.id && d.projectScope(saved.Info) == d.projectScope(peer.session.Info())
		if !owned {
			return nil, stopAgentResult{}, errors.New("only agents you spawned can be stopped")
		}
		err := d.closeSession(in.SessionID)
		return nil, stopAgentResult{Stopped: err == nil}, err
	})
	d.addStaticLayoutTools(server, peer)
	d.addUITools(server, peer)
	d.addTemplateTools(server, peer)
}

func (d *Daemon) syncPeerServerLocked(peer *agentPeer, srv *sdk.Server, snap agentMCPSnapshot, enabled bool) {
	d.projectAgents.mu.Lock()
	prev, tracked := peer.servers[srv]
	d.projectAgents.mu.Unlock()
	if !tracked {
		return
	}
	var nextSig string
	switch {
	case !enabled && prev == agentMCPSigDisabled:
		return
	case !enabled:
		srv.RemoveTools(agentMCPToolNames...)
		nextSig = agentMCPSigDisabled
	case prev == agentMCPSigDisabled:
		d.registerProjectAgentTools(srv, peer, snap)
		nextSig = snap.signature
	case prev != snap.signature:
		d.addDynamicAgentTools(srv, peer, snap)
		nextSig = snap.signature
	default:
		return
	}
	d.projectAgents.mu.Lock()
	if d.projectAgents.peers[peer.id] == peer && peer.ctx.Err() == nil {
		if _, ok := peer.servers[srv]; ok {
			peer.servers[srv] = nextSig
		}
	}
	d.projectAgents.mu.Unlock()
}

// syncProjectAgents updates every running session's built-in MCP server
// tools, descriptions and per-session MCP configuration files in real time.
func (d *Daemon) syncProjectAgents() {
	d.projectAgents.syncMu.Lock()
	defer d.projectAgents.syncMu.Unlock()

	snap := d.agentMCPSnapshot()
	type tracked struct {
		peer    *agentPeer
		session *session.Session
		servers []*sdk.Server
		refresh func(context.Context, *protocol.SessionInfo)
	}
	var list []tracked
	d.projectAgents.mu.Lock()
	for _, peer := range d.projectAgents.peers {
		if peer == nil || peer.ctx.Err() != nil || peer.session == nil {
			continue
		}
		if peer.session.Info().GetState() != protocol.StateRunning {
			continue
		}
		list = append(list, tracked{
			peer:    peer,
			session: peer.session,
			servers: slices.Collect(maps.Keys(peer.servers)),
			refresh: peer.refresh,
		})
	}
	d.projectAgents.mu.Unlock()

	baseCtx := d.ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	for _, item := range list {
		info := item.session.Info()
		enabled := d.sessionAgentMCPEnabled(info)
		for _, srv := range item.servers {
			d.syncPeerServerLocked(item.peer, srv, snap, enabled)
		}
		if item.refresh != nil {
			rctx, cancel := context.WithTimeout(baseCtx, 5*time.Second)
			item.refresh(rctx, info)
			cancel()
		}
	}
}

func (d *Daemon) peerRunningSession(peer *agentPeer) (*session.Session, error) {
	d.projectAgents.mu.Lock()
	s := peer.session
	current := d.projectAgents.peers[peer.id] == peer
	d.projectAgents.mu.Unlock()
	if !current || peer.ctx.Err() != nil || s == nil || s.Info().GetState() != protocol.StateRunning {
		return nil, errors.New("the calling session is no longer running")
	}
	return s, nil
}

func (d *Daemon) projectCaller(peer *agentPeer) (*session.Session, error) {
	s, err := d.peerRunningSession(peer)
	if err != nil {
		return nil, err
	}
	if !d.sessionAgentMCPEnabled(s.Info()) {
		return nil, errors.New("the mako MCP server is disabled for this session")
	}
	return s, nil
}

// projectTarget is the session of this daemon's whose agent id is, when it
// takes messages and is in scope.
func (d *Daemon) projectTarget(scope, id string) (*session.Session, error) {
	target := d.projectAgents.peer(id)
	if target == nil {
		return nil, errNotInProject
	}
	s, err := d.projectCaller(target)
	if err != nil || d.projectScope(s.Info()) != scope {
		return nil, errNotInProject
	}
	return s, nil
}

// inProject reports whether the session info is an agent of the project
// scope names.
func (d *Daemon) inProject(scope string, info *protocol.SessionInfo) bool {
	return isAgent(info) && d.projectScope(info) == scope
}

// summarizeAgent is a session as list_agents lists it; messaging says
// whether its agent takes messages.
func summarizeAgent(info *protocol.SessionInfo, parent string, messaging bool) agentSummary {
	return agentSummary{ID: info.GetId(), Agent: info.GetAgent(), Profile: info.GetProfile(), Name: info.GetName(), State: info.GetState(), Status: info.GetStatus(), ParentID: parent, Messaging: messaging, Folder: info.GetProject()}
}

func (d *Daemon) availableAgents() []availableAgent {
	configured := d.agents()
	var out []availableAgent
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
				Name: p.Name, Description: p.Description, Spawnable: p.AgentAllowed() && d.agentMCPEnabled(p.ContextServers),
			})
		}
		out = append(out, available)
	}
	return out
}

func (d *Daemon) spawnablePresets() []spawnablePreset {
	configured := d.agents()
	out := []spawnablePreset{}
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
		if d.agentMCPEnabled(nil) {
			out = append(out, spawnablePreset{
				Agent:  name,
				Target: agentspec.Target(name, ""),
				Yolo:   a.Yolo,
			})
		}
		for _, p := range a.Profiles {
			if !p.AgentAllowed() || !d.agentMCPEnabled(p.ContextServers) {
				continue
			}
			yolo := a.Yolo
			if p.Yolo != nil {
				yolo = *p.Yolo
			}
			out = append(out, spawnablePreset{
				Agent:       name,
				Profile:     p.Name,
				Target:      agentspec.Target(name, p.Name),
				Description: p.Description,
				Model:       p.Model,
				Effort:      p.Effort,
				Yolo:        yolo,
			})
		}
	}
	return out
}

func (d *Daemon) listAgentProfiles(peer *agentPeer) (listAgentProfilesResult, error) {
	if _, err := d.projectCaller(peer); err != nil {
		return listAgentProfilesResult{}, err
	}
	presets := d.spawnablePresets()
	grouped := []availableAgent{}
	for _, a := range d.availableAgents() {
		spawnableProfiles := make([]availableProfile, 0, len(a.Profiles))
		for _, p := range a.Profiles {
			if p.Spawnable {
				spawnableProfiles = append(spawnableProfiles, p)
			}
		}
		if !a.Spawnable && len(spawnableProfiles) == 0 {
			continue
		}
		grouped = append(grouped, availableAgent{
			Name:      a.Name,
			Spawnable: a.Spawnable,
			Profiles:  spawnableProfiles,
		})
	}
	return listAgentProfilesResult{
		Presets: presets,
		Agents:  grouped,
	}, nil
}

func (d *Daemon) listProjectAgents(peer *agentPeer) (listAgentsResult, error) {
	s, err := d.projectCaller(peer)
	if err != nil {
		return listAgentsResult{}, err
	}
	info := s.Info()
	out := listAgentsResult{SelfID: peer.id, Project: info.GetProject(), Agents: []agentSummary{}, Available: []availableAgent{}}
	self := d.projectScope(info)
	if s, _ := d.projectFor(info.GetProject(), cmp.Or(info.GetWorktree().GetRepo(), info.GetRepo())); s != nil {
		out.ProjectName = cmp.Or(s.GetName(), s.GetId())
	}
	d.sessionMu.Lock()
	parents := map[string]string{}
	for id, saved := range d.saved {
		parents[id] = saved.ParentID
	}
	d.sessionMu.Unlock()
	for _, other := range d.listSessions() {
		if d.inProject(self, other) {
			out.Agents = append(out.Agents, summarizeAgent(other, parents[other.GetId()], d.takesMessages(other)))
		}
	}
	for _, other := range d.remoteAgents(self) {
		out.Agents = append(out.Agents, d.remoteSummary(other))
	}
	out.Available = append(out.Available, d.availableAgents()...)
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
		if !profile.AgentAllowed() {
			return agentSummary{}, errors.New("this profile is not enabled for agent spawning")
		}
		picks = profile.ContextServers
	}
	if !d.agentMCPEnabled(picks) {
		return agentSummary{}, errors.New("the mako MCP server is disabled for this profile")
	}
	if err := ctx.Err(); err != nil {
		return agentSummary{}, err
	}
	prompt := projectTaskPrompt(peer.id, in.Prompt)
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
	info = child.Info()
	return summarizeAgent(info, peer.id, d.takesMessages(info)), nil
}

// Project agents are sessions of their own. Keep the parent's aggregate
// activity in step with their turns, rather than their process lifetime.
func (d *Daemon) followProjectAgent(child *session.Session, parentID string) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var watch childWatch
	for {
		changed := child.Changed()
		info := d.reportProjectAgent(child, parentID)
		if info.GetState() == protocol.StateExited {
			d.noteExit(info, parentID, watch.workSince)
			return
		}
		d.noteChild(info, parentID, &watch, time.Now())
		// The tick is for what changes as time passes: work gone stale, a
		// wait on the user that has lasted.
		select {
		case <-changed:
		case <-tick.C:
		case <-d.ctx.Done():
			return
		}
	}
}

// reportProjectAgent reports to the parent whether child is running a
// turn, and returns child's info.
func (d *Daemon) reportProjectAgent(child *session.Session, parentID string) *protocol.SessionInfo {
	info := child.Info()
	running := info.GetState() == protocol.StateRunning && info.GetStatus() != protocol.AgentDone && info.GetStatus() != protocol.AgentIdle
	if parent, ok := d.sessions.get(parentID); ok {
		parent.ReportSubagent("", child.ID(), running, time.Now().UnixNano())
	}
	return info
}
