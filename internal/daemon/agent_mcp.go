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
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/session"
	"github.com/ironpark/mako/internal/transport"
)

const agentMCPName = "mako"
const agentMCPArg = "agent-mcp"
const agentMCPTokenEnv = "MAKO_AGENT_MCP_TOKEN"

type projectAgents struct {
	syncMu       sync.Mutex // serializes live MCP tool and file sync across peers
	mu           sync.Mutex
	socket       string // the stdio bridge's, for an agent that cannot reach url
	cliSocket    string // agent-cli.sock for mako-msg CLI & agy-sidecar
	sidecarToken string // shared secret required for sidecar_subscribe on agent-cli.sock
	url          string // where the agents reach the server over HTTP
	peers        map[string]*agentPeer
	messages     agentMessages
	waits        map[string]int       // the agents watched with watch_agent / mako-msg wait, how many times each
	watchSeq     uint64               // monotonic watch_agent ID counter
	watches      map[string]int       // active watch_agent count per caller session
	cliWaiters   map[string]int       // active `mako-msg wait` / mod long-poll waiters per session (suppress PTY fallback)
	modPollUntil map[string]time.Time // grace window between mod long-poll cycles so PTY fallback never races
	agySidecar   *agySidecarClient
}

func (p *projectAgents) addCLIWaiter(id string) func() {
	p.mu.Lock()
	if p.cliWaiters == nil {
		p.cliWaiters = map[string]int{}
	}
	p.cliWaiters[id]++
	p.mu.Unlock()
	return func() {
		p.mu.Lock()
		if p.cliWaiters[id]--; p.cliWaiters[id] <= 0 {
			delete(p.cliWaiters, id)
		}
		p.mu.Unlock()
	}
}

func (p *projectAgents) markModActive(id string, ttl time.Duration) {
	p.mu.Lock()
	if p.modPollUntil == nil {
		p.modPollUntil = map[string]time.Time{}
	}
	until := time.Now().Add(ttl)
	if until.After(p.modPollUntil[id]) {
		p.modPollUntil[id] = until
	}
	p.mu.Unlock()
}

func (p *projectAgents) hasCLIWaiter(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cliWaiters[id] > 0 {
		return true
	}
	if until, ok := p.modPollUntil[id]; ok {
		if time.Now().Before(until) {
			return true
		}
		delete(p.modPollUntil, id)
	}
	return false
}

const maxWatchesPerSession = 16

func (p *projectAgents) claimWatch(callerID string) (string, func(), bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.watches == nil {
		p.watches = map[string]int{}
	}
	if p.watches[callerID] >= maxWatchesPerSession {
		return "", nil, false
	}
	p.watchSeq++
	id := fmt.Sprintf("w-%d", p.watchSeq)
	p.watches[callerID]++
	released := false
	release := func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if released {
			return
		}
		released = true
		if p.watches[callerID]--; p.watches[callerID] <= 0 {
			delete(p.watches, callerID)
		}
	}
	return id, release, true
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
