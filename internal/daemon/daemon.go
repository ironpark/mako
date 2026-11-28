package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ironpark/mako/internal/agentspec"
	"github.com/ironpark/mako/internal/agentui"
	"github.com/ironpark/mako/internal/config"
	"github.com/ironpark/mako/internal/decision"
	"github.com/ironpark/mako/internal/device"
	"github.com/ironpark/mako/internal/mcp"
	"github.com/ironpark/mako/internal/p2p"
	"github.com/ironpark/mako/internal/plugin"
	"github.com/ironpark/mako/internal/plugin/store"
	"github.com/ironpark/mako/internal/protocol"
	"github.com/ironpark/mako/internal/skills"
	"github.com/ironpark/mako/internal/transport"
	"github.com/ironpark/mako/internal/usage"
)

// peerTTL drops a discovered node that has not been seen for this long.
const peerTTL = 5 * time.Minute

// Daemon serves front-ends on its Unix socket, and peers over libp2p.
type Daemon struct {
	cfgMu   sync.Mutex
	cfgSnap atomic.Pointer[config.Config]
	log     *slog.Logger
	dir     string // the configuration directory; Run sets config.Dir() when empty

	loggedCommands loggedCommands // the commands agents' logs name
	live           liveHub        // the replies streaming, by session
	liveSocket     string         // where they are posted; "" before it listens
	liveSecret     []byte         // what each session's posts are signed by

	sessionMu   sync.Mutex // protects saved launches and activation
	saved       map[string]savedSession
	sessionPath string
	// starting are the sessions whose launch is being made ready, each
	// closed as its start ends; guarded by sessionMu.
	starting map[string]chan struct{}

	sessions      *registry
	codexMu       sync.Mutex
	codexBridges  map[string]*codexBridge
	codexStartup  codexStartupCoordinator
	goalActions   goalActionGuard
	conversations conversations
	approvals     approvals
	layouts       windowLayouts // the window layouts' changes staged here, and what is node-local of them; see layouts.go
	templateAsks  templateAsks  // the templates agents ask the user to save; see agent_templates.go
	peers         *peerTable
	ssh           *sshManager

	// ln is the socket front-ends reach the daemon on, at socket; only
	// this user can.
	ln     net.Listener
	socket string
	ready  chan struct{} // closed once the listener is up; see Ready

	mu sync.Mutex

	// p2p links this daemon to its peers over libp2p; nil when it is off
	// or could not start, leaving the daemon to itself.
	p2p *p2p.Host
	// keyPrint and ownerPrint are the fingerprints of the host's own key
	// and of the SSH key vouching for it.
	keyPrint, ownerPrint string
	access               accessRequests            // the peers asking to be let in
	pairing              pairing                   // the pairing code waiting to be read
	heads                heads                     // the projects' branches
	remote               peerReports               // what peers say of their sessions and routes; see routes.go
	changes              notifier                  // tells watching front-ends what changed
	statusPoke           chan struct{}             // a session's status changed; see watchState
	workspace            workspaces                // the spaces, projects and layouts; see workspace.go
	pluginConfigSync     pluginConfigSync          // plugin registries, installs, disabled & non-secret settings; see plugin_config_sync.go
	agentSync            agentConfigSync           // agent definitions, MCP servers, skills & templates; see agent_sync.go
	configSyncWG         sync.WaitGroup            // background config-sync goroutines awaited on shutdown
	fetching             map[string]bool           // the peers whose workspace is being asked for
	syncingPluginDB      map[string]bool           // the peers whose plugin DB sync is in flight
	syncingPluginConfig  map[string]*peerSyncState // the peers whose plugin config sync is in flight
	syncingAgentConfig   map[string]*peerSyncState // the peers whose agent config sync is in flight
	projects             projectStore              // the projects of several folders; see projects.go
	linking              map[string]*linkTry       // peer ID -> how its link is being made, until one holds
	stateSenders         stateSenders              // coalesced per-peer state senders; see mesh.go
	ctx                  context.Context           // Run's, for work that outlasts a request

	kinds  map[string]device.Kind // the kinds of device; see devices.go
	claims claims                 // the devices of this node lent to others
	leases leases                 // the devices this node borrows

	agentFile     agentFile
	plugins       *plugin.Manager // the node's plugins; see plugins.go
	pluginsDir    string          // the folder they are read from
	store         *store.Store    // the registries the plugins are installed from; see pluginstore.go
	decisionsMu   sync.Mutex
	decisions     *decision.Store // provider API keys & decision models; see decisions.go
	usageMu       sync.Mutex
	usage         *usage.Collector // global account quota collector; see account_usage.go
	agentUI       *agentui.Store   // the blocks agents show; see agent_ui.go
	uiWatched     onceSet          // the sessions whose end closes their blocks
	mcp           *mcp.Resolver    // nil without a configuration directory
	projectAgents projectAgents

	hookBinary  string // the program agents run as their hook; see HookMain
	hookCommand string // the shell command agents run as their hook; "" for none
	// statusCommand is the one Claude runs as its status line, which
	// reports the account's limits; "" for none.
	statusCommand string
	// clipTools is the folder of the clipboard tools put first in a
	// session's PATH; see clipboard.go. "" for none.
	clipTools string

	fileWatcher FileWatcher
}

// New builds a daemon from configuration.
func New(cfg *config.Config, log *slog.Logger) *Daemon {
	if log == nil {
		log = slog.Default()
	}
	snap := cloneConfig(cfg)
	d := &Daemon{
		log:         log,
		sessions:    newRegistry(),
		peers:       newPeerTable(),
		linking:     map[string]*linkTry{},
		ctx:         context.Background(),
		ready:       make(chan struct{}),
		mcp:         newResolver(log),
		kinds:       deviceKinds(snap),
		fileWatcher: defaultFileWatcher(),
		// One poke waits however many sessions change before watchState
		// takes it.
		statusPoke: make(chan struct{}, 1),

		hookBinary: hookProgram,
	}
	d.cfgSnap.Store(snap)
	d.ssh = newSSHManager(d)
	d.agentUI = agentui.New(d.changes.fireAgentBlocks)
	return d
}

func (d *Daemon) config() *config.Config {
	if cfg := d.cfgSnap.Load(); cfg != nil {
		return cfg
	}
	return config.Default()
}

func (d *Daemon) updateConfig(fn func(*config.Config)) {
	d.cfgMu.Lock()
	defer d.cfgMu.Unlock()
	next := cloneConfig(d.cfgSnap.Load())
	fn(next)
	d.cfgSnap.Store(next)
}

// Ready is closed once Run is listening, after which Addr is valid. A Run
// that fails to listen returns its error without closing it.
func (d *Daemon) Ready() <-chan struct{} { return d.ready }

// Socket is the socket the daemon listens on. Valid after Ready.
func (d *Daemon) Socket() string { return d.socket }

// Self describes this daemon as a node record.
func (d *Daemon) Self() *protocol.Node {
	cfg := d.config()
	return &protocol.Node{
		Id:         cfg.NodeID,
		Name:       cfg.Name,
		Addr:       d.addr(),
		Os:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		Version:    config.Version,
		Source:     "self",
		LastSeen:   timestamppb.Now(),
		Revision:   protocol.Revision,
		Agents:     d.agentInfo(),
		PeerId:     d.peerID(),
		Multiaddrs: d.multiaddrs(),
		Key:        d.keyPrint,
		Owner:      d.ownerPrint,
		// What tells a node that reads the workspace here, without merging
		// it, that it changed: a phone, which lets no one in.
		WorkspaceDigest: d.workspace.currentDigest(),
		Projects:        d.projects.get(),
	}
}

// publicSelf is this daemon as it shows itself to a node it does not let
// in: without the agents it can launch, or its workspace's digest.
func (d *Daemon) publicSelf() *protocol.Node {
	self := d.Self()
	self.Agents = nil
	self.Projects = nil
	self.WorkspaceDigest = nil
	return self
}

// Run serves until ctx is cancelled.
func (d *Daemon) Run(ctx context.Context) error {
	dir := d.dir
	if dir == "" {
		var err error
		if dir, err = config.Dir(); err != nil {
			return err
		}
		// Kept, so what reads d.dir later reads the folder this daemon
		// runs on, not "", which reads as none.
		d.dir = dir
	}
	run, err := config.RunDir(dir)
	if err != nil {
		return err
	}
	socket := filepath.Join(run, "daemon.sock")
	d.ln, err = transport.Listen(socket)
	if err != nil {
		return err
	}
	d.socket = socket
	defer d.ln.Close()
	if err := d.loadSessions(dir); err != nil {
		return err
	}
	cfg := d.config()
	d.workspace.load(dir, cfg.NodeID)
	d.pluginConfigSync.load(dir, cfg.NodeID)
	d.agentSync.load(dir, cfg.NodeID)
	d.layouts.load(dir)
	d.projects.load(dir)
	d.syncWorkspaceProjects()
	defer func() {
		if d.agentUI != nil {
			d.agentUI.Shutdown()
		}
	}()
	defer d.uiWatched.stopAndWait()
	defer d.sessions.closeAll()
	defer d.closeAllCodexBridges()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	d.ctx = ctx
	d.listenHooks(ctx, run)
	d.listenLive(ctx, run)
	if err := d.projectAgents.messages.load(dir); err != nil {
		return err
	}
	d.projectAgents.messages.start(ctx)
	defer func() {
		if err := d.projectAgents.messages.close(); err != nil {
			d.log.Warn("agent messages flush failed", "err", err)
		}
	}()
	if err := d.listenAgentMCP(ctx, run); err != nil {
		return err
	}
	pluginsDone := d.startPlugins(ctx, dir)
	d.captureLocalPluginConfig()
	d.captureLocalAgentConfig()
	defer func() {
		cancel()
		<-pluginsDone
		d.configSyncWG.Wait()
		d.conversations.stopAndWait()
	}()
	if !cfg.P2P.Disabled {
		if err := d.startP2P(ctx, dir); err != nil {
			d.log.Warn("libp2p did not start; this daemon has no peers", "err", err)
		} else {
			defer d.p2p.Close()
		}
	}
	d.ssh.start(ctx, run, cfg.SSHHosts)
	defer d.ssh.close()
	d.log.Info("daemon listening", "socket", socket, "node", cfg.NodeID, "name", cfg.Name)
	close(d.ready)

	var wg sync.WaitGroup
	wg.Go(func() { d.syncBuiltinSkills(ctx) })
	wg.Go(func() { d.warmContextServers(ctx) })
	wg.Go(func() { d.serve(ctx) })
	wg.Go(func() { d.discover(ctx) })
	wg.Go(func() { d.maintain(ctx) })
	wg.Go(func() { d.watchState(ctx) })

	<-ctx.Done()
	_ = d.ln.Close()
	wg.Wait()
	return nil
}

// serve takes front-ends' connections on the socket, one request stream
// to a connection.
func (d *Daemon) serve(ctx context.Context) {
	for {
		conn, err := d.ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			d.log.Warn("accept failed", "err", err)
			continue
		}
		go func() {
			stream := transport.NewStream(conn)
			defer stream.Close()
			if err := d.serveStream(ctx, stream, false); err != nil && !isClosed(err) {
				d.log.Debug("stream ended", "err", err)
			}
		}()
	}
}

// serveStream handles one request stream: from a front-end on the socket,
// which only this user can reach, or from a peer this daemon lets in, on
// libp2p. The first frame must be a hello.
func (d *Daemon) serveStream(ctx context.Context, stream *transport.Stream, fromPeer bool) error {
	env, err := stream.Recv()
	if err != nil {
		return err
	}
	if env.Type() != protocol.TypeHello {
		return protocol.WriteError(stream, env.GetId(), errors.New("expected hello"))
	}
	hello, err := protocol.Body[*protocol.Hello](env)
	if err != nil {
		return protocol.WriteError(stream, env.GetId(), err)
	}
	if fromPeer {
		ctx = withHops(ctx, hello.GetHops()+1)
	}
	if err := stream.Send(protocol.TypeWelcome, env.GetId(), &protocol.Welcome{Node: d.Self()}); err != nil {
		return err
	}

	for {
		env, err := stream.Recv()
		if err != nil {
			return err
		}
		if err := d.dispatch(ctx, stream, env, fromPeer); err != nil {
			if isClosed(err) {
				return err
			}
			if writeErr := protocol.WriteError(stream, env.GetId(), err); writeErr != nil {
				return writeErr
			}
		}
	}
}

// fromPeer marks a stream from another daemon, on libp2p, rather than from
// a front-end.
func (d *Daemon) dispatch(ctx context.Context, stream *transport.Stream, env *protocol.Envelope, fromPeer bool) error {
	switch env.Type() {
	case protocol.TypeListNodes:
		return stream.Send(protocol.TypeNodes, env.GetId(), &protocol.Nodes{Nodes: d.nodes()})

	case protocol.TypeAccess:
		return d.handleAccess(ctx, stream, env, fromPeer)

	case protocol.TypeGetWorkspace, protocol.TypeWorkspace:
		return d.handleWorkspace(ctx, stream, env, fromPeer)
	case protocol.TypeLayoutDecision:
		return serve(d, ctx, stream, env, protocol.TypeWorkspace, func(*protocol.LayoutDecision) (*protocol.Workspace, error) {
			return d.handleLayoutDecision(ctx, env, fromPeer)
		})

	case protocol.TypeSetProjects:
		return d.handleSetProjects(ctx, stream, env)
	case protocol.TypeRelayAgentMessage:
		return d.handleRelayAgentMessage(ctx, stream, env)
	case protocol.TypeRelaySpawnAgent:
		return d.handleRelaySpawnAgent(ctx, stream, env)
	case protocol.TypeRelayStopAgent:
		return d.handleRelayStopAgent(ctx, stream, env)

	case protocol.TypeListSess:
		return d.handleListSessions(ctx, stream, env, fromPeer)

	case protocol.TypeWatch:
		return d.handleWatch(ctx, stream, env, fromPeer)

	case protocol.TypeWatchChat:
		return d.handleWatchChat(ctx, stream, env)

	case protocol.TypeOpenSession:
		return d.handleOpenSession(ctx, stream, env)

	case protocol.TypeCloseSess:
		return d.handleCloseSession(ctx, stream, env)

	case protocol.TypeRenameSess:
		return d.handleRenameSession(ctx, stream, env)

	case protocol.TypeListCommands:
		return d.handleListCommands(ctx, stream, env)

	case protocol.TypeAttach:
		return d.handleAttach(ctx, stream, env)

	case protocol.TypeWatchFiles:
		return d.handleWatchFiles(ctx, stream, env)
	case protocol.TypeListDir:
		return d.handleListDir(ctx, stream, env)

	case protocol.TypeReadFile:
		return d.handleReadFile(ctx, stream, env)

	case protocol.TypeWriteFile:
		return d.handleWriteFile(ctx, stream, env)

	case protocol.TypeStageFile:
		return d.handleStageFile(ctx, stream, env)

	case protocol.TypeTunnel:
		return d.handleTunnel(ctx, stream, env)

	case protocol.TypeListDevices:
		return d.handleListDevices(ctx, stream, env, fromPeer)

	case protocol.TypeBorrow:
		return d.handleBorrow(ctx, stream, env)

	case protocol.TypeGiveBack:
		return d.handleGiveBack(ctx, stream, env)

	case protocol.TypeListLeases:
		return d.handleListLeases(ctx, stream, env)

	case protocol.TypeClaim:
		return d.handleClaim(ctx, stream, env)

	case protocol.TypeDeviceConn:
		return d.handleDeviceConn(ctx, stream, env)

	case protocol.TypeDeviceListen:
		return d.handleDeviceListen(ctx, stream, env)

	case protocol.TypeDeviceAccept:
		return d.handleDeviceAccept(ctx, stream, env)

	case protocol.TypeGitStatus:
		return d.handleGitStatus(ctx, stream, env)

	case protocol.TypeGitDiff:
		return d.handleGitDiff(ctx, stream, env)

	case protocol.TypeFileOp:
		return d.handleFileOp(ctx, stream, env)

	case protocol.TypeGitLog:
		return d.handleGitLog(ctx, stream, env)

	case protocol.TypeGitShow:
		return d.handleGitShow(ctx, stream, env)

	case protocol.TypeGitRun:
		return d.handleGitRun(ctx, stream, env)

	case protocol.TypeForgeList:
		return d.handleGitHubList(ctx, stream, env)

	case protocol.TypeForgeView:
		return d.handleGitHubView(ctx, stream, env)

	case protocol.TypeForgeRun:
		return d.handleGitHubRun(ctx, stream, env)

	case protocol.TypeReadChat:
		return d.handleReadChat(ctx, stream, env)
	case protocol.TypeReadProjectAgents:
		return d.handleReadProjectAgents(ctx, stream, env)

	case protocol.TypeAnswerApproval:
		return d.handleAnswerApproval(ctx, stream, env, fromPeer)

	case protocol.TypeSend:
		return d.handleSend(ctx, stream, env)
	case protocol.TypeAnswerQuestion:
		return d.handleAnswerQuestion(ctx, stream, env)
	case protocol.TypeAnswerDialog:
		return d.handleAnswerDialog(ctx, stream, env)
	case protocol.TypeSeeSession:
		return d.handleSeeSession(ctx, stream, env)
	case protocol.TypeConfigureAgent:
		return d.handleConfigureAgent(ctx, stream, env)
	case protocol.TypeSleepSession:
		return d.handleSleepSession(ctx, stream, env)
	case protocol.TypeListAgentModels:
		return d.handleListAgentModels(ctx, stream, env)
	case protocol.TypeReadAccountUsage:
		return d.handleReadAccountUsage(ctx, stream, env)
	case protocol.TypeRewindSession:
		return d.handleRewindSession(ctx, stream, env)
	case protocol.TypeGoalAction:
		return d.handleGoalAction(ctx, stream, env)
	case protocol.TypeInspectAgentHome:
		return d.handleInspectAgentHome(ctx, stream, env)
	case protocol.TypeMigrateAgentHome:
		return d.handleMigrateAgentHome(ctx, stream, env)
	case protocol.TypeRunAgentPrompt:
		return d.handleRunAgentPrompt(ctx, stream, env)
	case protocol.TypeListProviders:
		return d.handleListProviders(ctx, stream, env, fromPeer)
	case protocol.TypeProviderAction:
		return d.handleProviderAction(ctx, stream, env, fromPeer)
	case protocol.TypeRunDecision:
		return d.handleRunDecision(ctx, stream, env, fromPeer)
	case protocol.TypeListPolicies:
		return d.handleListPolicies(stream, env, fromPeer)
	case protocol.TypeSetPolicies:
		return d.handleSetPolicies(stream, env, fromPeer)

	case protocol.TypeSearch:
		return d.handleSearch(ctx, stream, env)

	case protocol.TypeListPlugins:
		return d.handleListPlugins(ctx, stream, env, fromPeer)
	case protocol.TypePluginAction:
		return d.handlePluginAction(ctx, stream, env, fromPeer)
	case protocol.TypeWatchBlock:
		return d.handleWatchBlock(ctx, stream, env, fromPeer)
	case protocol.TypeBlockEvent:
		return d.handleBlockEvent(ctx, stream, env, fromPeer)
	case protocol.TypeListAgentBlocks:
		return d.handleListAgentBlocks(ctx, stream, env, fromPeer)
	case protocol.TypeListPanels:
		return d.handleListPanels(ctx, stream, env)
	case protocol.TypeListRegistries, protocol.TypeRegistryAction, protocol.TypeSearchPlugins, protocol.TypeInstallPlugin:
		return d.handlePluginStore(ctx, stream, env, fromPeer)
	case protocol.TypeSyncPluginDB:
		return d.handleSyncPluginDB(ctx, stream, env, fromPeer)
	case protocol.TypeSyncPluginConfig:
		return d.handleSyncPluginConfig(ctx, stream, env, fromPeer)
	case protocol.TypeSyncAgentConfig:
		return d.handleSyncAgentConfig(ctx, stream, env, fromPeer)

	case protocol.TypeResize:
		payload, err := protocol.Body[*protocol.Resize](env)
		if err != nil {
			return err
		}
		s, ok := d.sessions.get(payload.GetSessionId())
		if !ok {
			return fmt.Errorf("unknown session %q", payload.GetSessionId())
		}
		if err := resize(s, payload); err != nil {
			return err
		}
		return stream.Send(protocol.TypeSessionInfo, env.GetId(), s.Info())

	case protocol.TypeInput:
		payload, err := protocol.Body[*protocol.Input](env)
		if err != nil {
			return err
		}
		s, ok := d.sessions.get(payload.GetSessionId())
		if !ok {
			return fmt.Errorf("unknown session %q", payload.GetSessionId())
		}
		_, err = s.Write(payload.GetData())
		return err

	default:
		return &protocol.CodedError{Code: protocol.CodeUnsupported, Message: fmt.Sprintf("unsupported message %q", env.Type())}
	}
}

func isClosed(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, context.Canceled) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, network.ErrReset)
}

// syncBuiltinSkills registers Mako's built-in authoring skills (mako-template,
// mako-plugin) into the skill directories of agents already configured or
// isolated on this node so slash commands and skill loaders find them at once.
func (d *Daemon) syncBuiltinSkills(ctx context.Context) {
	agentsMap := d.agents()
	isolate := d.isolateHome()
	for _, name := range skills.SupportedAgents {
		if ctx.Err() != nil {
			return
		}
		env := agentspec.IsolatedHomeEnvForAgent(name, isolate, agentsMap)
		for _, dir := range skills.AgentSkillsDirs(name, env) {
			if ctx.Err() != nil {
				return
			}
			parent := filepath.Dir(dir)
			if _, err := os.Stat(parent); err != nil {
				continue // skip agents not initialized on this machine
			}
			if err := skills.SyncFromOverridesDir(d.skillsDir(), dir); err != nil {
				d.log.Warn("failed to sync mako built-in skills", "agent", name, "dir", dir, "err", err)
			}
		}
	}
}
